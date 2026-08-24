package contextstore

import (
	"encoding/json"
	"math"
	"os"
	"path"
	"sort"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	statsFileName = ".stats.json"
	sampleCap     = 64
)

type persistedOp struct {
	Count   int64     `json:"count"`
	Errors  int64     `json:"errors"`
	TotalNs int64     `json:"total_ns"`
	LastNs  int64     `json:"last_ns"`
	LastAt  time.Time `json:"last_at"`
	Samples []int64   `json:"samples"`
}

type persistedStats struct {
	Ops map[string]persistedOp `json:"ops"`
}

func statsPath(root string) string {
	return path.Join(root, statsFileName)
}

func loadPersisted(root string) persistedStats {
	raw, err := os.ReadFile(statsPath(root))
	if err != nil {
		return persistedStats{Ops: map[string]persistedOp{}}
	}
	var s persistedStats
	if json.Unmarshal(raw, &s) != nil || s.Ops == nil {
		return persistedStats{Ops: map[string]persistedOp{}}
	}
	return s
}

func (s *Files) observe(op string, start time.Time, err error, n int) {
	_ = n
	if s == nil {
		return
	}
	ns := time.Since(start).Nanoseconds()
	if ns < 0 {
		ns = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ops.Ops == nil {
		s.ops.Ops = map[string]persistedOp{}
	}
	cur := s.ops.Ops[op]
	cur.Count++
	if err != nil {
		cur.Errors++
	}
	cur.TotalNs += ns
	cur.LastNs = ns
	cur.LastAt = time.Now().UTC()
	cur.Samples = append(cur.Samples, ns)
	if len(cur.Samples) > sampleCap {
		cur.Samples = cur.Samples[len(cur.Samples)-sampleCap:]
	}
	s.ops.Ops[op] = cur
	s.saveOpsLocked()
}

func (s *Files) saveOpsLocked() {
	raw, err := json.Marshal(s.ops)
	if err != nil {
		return
	}
	p := statsPath(s.root)
	tmp := p + ".tmp"
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
	}
}

func (s *Files) snapshotOps() map[string]domain.ContextOpStat {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]domain.ContextOpStat, len(s.ops.Ops))
	for name, op := range s.ops.Ops {
		st := domain.ContextOpStat{Count: op.Count, Errors: op.Errors}
		if op.LastNs > 0 {
			st.LastMs = ms(op.LastNs)
		}
		if op.Count > 0 {
			st.AvgMs = ms(op.TotalNs / op.Count)
		}
		st.P50Ms = percentileMs(op.Samples, 0.50)
		st.P95Ms = percentileMs(op.Samples, 0.95)
		if !op.LastAt.IsZero() {
			st.LastAt = op.LastAt.UTC().Format(time.RFC3339)
		}
		out[name] = st
	}
	return out
}

func ms(ns int64) float64 {
	return math.Round(float64(ns)/1e4) / 100 // 0.01ms
}

// ParseStatsJSON extracts ContextStats from `aiman context stats` JSON.
func ParseStatsJSON(raw []byte) (domain.ContextStats, error) {
	var wrap struct {
		Type  string              `json:"type"`
		Stats domain.ContextStats `json:"stats"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Type == "context_stats" {
		return wrap.Stats, nil
	}
	var direct domain.ContextStats
	if err := json.Unmarshal(raw, &direct); err != nil {
		return domain.ContextStats{}, err
	}
	return direct, nil
}

func percentileMs(samples []int64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	cp := append([]int64(nil), samples...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return ms(cp[idx])
}
