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

// CodexState is the model and last cumulative totals for one Codex transcript.
type CodexState struct {
	Model string      `json:"model"`
	Last  CodexTotals `json:"last"`
}

// ParseCodex reads a Codex rollout JSONL transcript.
// st is updated as totals increase. A nil st parses against a discarded zero
// state and does not panic. consumed counts bytes through the last complete
// line, including its newline.
func ParseCodex(r io.Reader, st *CodexState) ([]domain.UsageRecord, int64, error) {
	if st == nil {
		st = &CodexState{}
	}
	var recs []domain.UsageRecord
	consumed, err := readLines(r, func(line []byte) error {
		rec, ok := applyCodexLine(line, st)
		if ok {
			recs = append(recs, rec)
		}
		return nil
	})
	return recs, consumed, err
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
}

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func applyCodexLine(line []byte, st *CodexState) (domain.UsageRecord, bool) {
	if !bytes.Contains(line, []byte("turn_context")) && !bytes.Contains(line, []byte("token_count")) {
		return domain.UsageRecord{}, false
	}
	var row codexLine
	if err := json.Unmarshal(line, &row); err != nil {
		return domain.UsageRecord{}, false
	}
	if row.Type == "turn_context" {
		st.Model = row.Payload.Model
		return domain.UsageRecord{}, false
	}
	if row.Payload.Type != "token_count" {
		return domain.UsageRecord{}, false
	}
	return codexRecord(row, st)
}

func codexRecord(row codexLine, st *CodexState) (domain.UsageRecord, bool) {
	usage := row.totalUsage()
	if usage == nil || usage.reportedTotal() <= st.Last.sum() {
		return domain.UsageRecord{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return domain.UsageRecord{}, false
	}
	cur := usage.totals()
	rec := domain.UsageRecord{
		Model:  codexModel(st.Model),
		At:     at,
		Tokens: cur.delta(st.Last),
	}
	st.Last = cur
	return rec, true
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
