package usage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// CodexUnknownModel is the model id when a token_count arrives before any turn_context.
const CodexUnknownModel = "codex-unknown"

// CodexTotals is cumulative usage as Codex reports it.
// Input is input_tokens, which includes cached tokens.
type CodexTotals struct {
	Input      int64
	Cached     int64
	CacheWrite int64
	Output     int64
}

// CodexState is the model, the last field snapshot, and the high watermark.
// Seen is the greatest reported total already accepted. total_tokens can
// disagree with input_tokens+output_tokens, so Last alone cannot remember it.
type CodexState struct {
	Model string      `json:"model"`
	Last  CodexTotals `json:"last"`
	Seen  int64       `json:"seen"`
}

// ParseCodex reads a Codex rollout JSONL transcript.
// st is updated as totals increase. A nil st parses against a discarded zero
// state and does not panic. consumed counts bytes through the last complete
// line, including its newline.
func ParseCodex(r io.Reader, st *CodexState) ([]domain.UsageRecord, int64, error) {
	recs, n, _, err := parseCodex(r, st)
	return recs, n, err
}

// CodexContextTokens is the last counted token_count turn's occupancy.
// Occupancy is last_token_usage.input_tokens. That field already includes
// cached input, so cached_input_tokens is not added again. A snapshot that
// does not increase the cumulative total is not counted. ok is false when no
// counted turn carried last_token_usage.
func CodexContextTokens(r io.Reader) (int64, bool, error) {
	_, _, sample, err := parseCodex(r, nil)
	if err != nil {
		return 0, false, err
	}
	return sample.Tokens, sample.OK, nil
}

func parseCodex(r io.Reader, st *CodexState) ([]domain.UsageRecord, int64, contextSample, error) {
	if st == nil {
		st = &CodexState{}
	}
	var recs []domain.UsageRecord
	var sample contextSample
	consumed, err := readLines(r, func(line []byte) error {
		rec, turn, ok := applyCodexLine(line, st)
		if !ok {
			return nil
		}
		recs = append(recs, rec)
		if turn.OK {
			sample = turn
		}
		return nil
	})
	return recs, consumed, sample, err
}

// CodexFingerprint identifies a rollout from its head bytes.
// The first session_meta payload id returns "thread:<id>".
// With no id, it returns "sha256:" plus the lowercase hex of the first
// complete line without its newline. An empty head, or a head with no
// complete line, returns "".
func CodexFingerprint(head []byte) string {
	var first []byte
	var thread string
	seen := false
	_, err := readLines(bytes.NewReader(head), func(line []byte) error {
		if !seen {
			seen = true
			first = append([]byte(nil), line...)
		}
		if thread == "" {
			thread = sessionThreadID(line)
		}
		return nil
	})
	if err != nil || !seen {
		return ""
	}
	if thread != "" {
		return "thread:" + thread
	}
	sum := sha256.Sum256(first)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type codexLine struct {
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Payload   codexPayload `json:"payload"`
}

type codexPayload struct {
	Type  string     `json:"type"`
	Model string     `json:"model"`
	Info  *codexInfo `json:"info"`
}

type codexInfo struct {
	Total *codexUsage `json:"total_token_usage"`
	Last  *codexUsage `json:"last_token_usage"`
}

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func applyCodexLine(line []byte, st *CodexState) (domain.UsageRecord, contextSample, bool) {
	if !bytes.Contains(line, []byte("turn_context")) && !bytes.Contains(line, []byte("token_count")) {
		return domain.UsageRecord{}, contextSample{}, false
	}
	var row codexLine
	if err := json.Unmarshal(line, &row); err != nil {
		return domain.UsageRecord{}, contextSample{}, false
	}
	if row.Type == "turn_context" {
		st.Model = row.Payload.Model
		return domain.UsageRecord{}, contextSample{}, false
	}
	if row.Payload.Type != "token_count" {
		return domain.UsageRecord{}, contextSample{}, false
	}
	return codexRecord(row, st)
}

func codexRecord(row codexLine, st *CodexState) (domain.UsageRecord, contextSample, bool) {
	usage := row.totalUsage()
	if usage == nil {
		return domain.UsageRecord{}, contextSample{}, false
	}
	reported := usage.reportedTotal()
	cur := usage.totals()
	if !countCodexSnapshot(st, reported, cur) {
		return domain.UsageRecord{}, contextSample{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return domain.UsageRecord{}, contextSample{}, false
	}
	rec := domain.UsageRecord{
		Model:  codexModel(st.Model),
		At:     at,
		Tokens: cur.delta(st.Last),
	}
	st.Last = cur
	if reported > st.Seen {
		st.Seen = reported
	}
	return rec, codexTurn(row, rec.Model), true
}

// codexTurn reads occupancy from last_token_usage.input_tokens.
// The cumulative total_token_usage snapshot is the billing delta, not the window fill.
func codexTurn(row codexLine, model string) contextSample {
	if row.Payload.Info == nil || row.Payload.Info.Last == nil {
		return contextSample{}
	}
	return contextSample{Tokens: row.Payload.Info.Last.InputTokens, Model: model, OK: true}
}

// countCodexSnapshot is true when the field snapshot moved forward.
// Seen stays the greatest reported total. A later event whose reported total
// is lower can still count when that total covers the new snapshot, so an
// inflated total_tokens cannot rewind Last or hide the increase.
func countCodexSnapshot(st *CodexState, reported int64, cur CodexTotals) bool {
	if cur.sum() <= st.Last.sum() {
		return false
	}
	if reported > st.Seen {
		return true
	}
	return reported >= cur.sum()
}

func (row codexLine) totalUsage() *codexUsage {
	if row.Payload.Info == nil || row.Payload.Info.Total == nil {
		return nil
	}
	return row.Payload.Info.Total
}

func (u codexUsage) totals() CodexTotals {
	return CodexTotals{
		Input:      u.InputTokens,
		Cached:     u.CachedInputTokens,
		CacheWrite: u.CacheWriteInputTokens,
		Output:     u.OutputTokens,
	}
}

// reportedTotal is total_tokens when that field is non-zero.
// Zero and absent both fall back to input_tokens + output_tokens.
func (u codexUsage) reportedTotal() int64 {
	if u.TotalTokens != 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}

func (t CodexTotals) sum() int64 {
	return t.Input + t.Output
}

// delta clamps every field at zero. Input is what remains after cached and
// cache-write tokens, which Codex already counts inside input_tokens.
func (cur CodexTotals) delta(last CodexTotals) domain.TokenCounts {
	dInput := max(int64(0), cur.Input-last.Input)
	dCached := max(int64(0), cur.Cached-last.Cached)
	dWrite := max(int64(0), cur.CacheWrite-last.CacheWrite)
	dOutput := max(int64(0), cur.Output-last.Output)
	return domain.TokenCounts{
		Input:        max(int64(0), dInput-dCached-dWrite),
		Output:       dOutput,
		CacheRead:    dCached,
		CacheWrite5m: dWrite,
	}
}

func codexModel(model string) string {
	if model == "" {
		return CodexUnknownModel
	}
	return model
}

func sessionThreadID(line []byte) string {
	if !bytes.Contains(line, []byte(`"session_meta"`)) {
		return ""
	}
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &row); err != nil || row.Type != "session_meta" {
		return ""
	}
	return row.Payload.ID
}
