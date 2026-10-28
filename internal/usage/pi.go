package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// ParsePi reads a Pi session JSONL file (~/.pi/agent/sessions/--<cwd>--/*.jsonl).
// Each assistant message entry carries its own usage, so there is one record
// per entry id. consumed counts bytes through the last complete line.
func ParsePi(r io.Reader) ([]domain.UsageRecord, int64, error) {
	recs, n, _, err := parsePi(r)
	return recs, n, err
}

// parsePi also returns the last turn's occupancy: input + cacheRead, the
// window the next request reads. Pi has no tool-health signals aiman reads.
func parsePi(r io.Reader) ([]domain.UsageRecord, int64, contextSample, error) {
	var recs []domain.UsageRecord
	var sample contextSample
	seen := make(map[string]struct{})
	consumed, err := readLines(r, func(line []byte) error {
		rec, ok := parsePiLine(line)
		if !ok {
			return nil
		}
		if rec.ID != "" {
			if _, dup := seen[rec.ID]; dup {
				return nil
			}
			seen[rec.ID] = struct{}{}
		}
		recs = append(recs, rec)
		sample = contextSample{Tokens: rec.Tokens.Input + rec.Tokens.CacheRead, Model: rec.Model, OK: true}
		return nil
	})
	return recs, consumed, sample, err
}

type piLine struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role  string   `json:"role"`
		Model string   `json:"model"`
		Usage *piUsage `json:"usage"`
	} `json:"message"`
}

// piUsage is Pi's normalized Usage. output already includes reasoning, and
// cacheWrite1h is the part of cacheWrite written with one-hour retention.
type piUsage struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheRead    int64 `json:"cacheRead"`
	CacheWrite   int64 `json:"cacheWrite"`
	CacheWrite1h int64 `json:"cacheWrite1h"`
}

func parsePiLine(line []byte) (domain.UsageRecord, bool) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return domain.UsageRecord{}, false
	}
	var row piLine
	if err := json.Unmarshal(line, &row); err != nil {
		return domain.UsageRecord{}, false
	}
	if row.Type != "message" || row.Message.Role != "assistant" || row.Message.Usage == nil {
		return domain.UsageRecord{}, false
	}
	u := row.Message.Usage
	h1 := min(max(u.CacheWrite1h, 0), u.CacheWrite)
	tokens := domain.TokenCounts{
		Input:        u.Input,
		Output:       u.Output,
		CacheRead:    u.CacheRead,
		CacheWrite5m: u.CacheWrite - h1,
		CacheWrite1h: h1,
	}
	if tokens.IsZero() {
		return domain.UsageRecord{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return domain.UsageRecord{}, false
	}
	return domain.UsageRecord{ID: row.ID, Model: row.Message.Model, At: at, Tokens: tokens}, true
}
