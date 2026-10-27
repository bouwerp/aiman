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

// ParseClaude reads a Claude Code JSONL transcript.
// It returns one UsageRecord per assistant message id. consumed counts bytes
// through the last complete line, including its newline.
func ParseClaude(r io.Reader) ([]domain.UsageRecord, int64, error) {
	recs, n, _, err := parseClaude(r)
	return recs, n, err
}

// ClaudeContextTokens is the last counted assistant turn's occupancy.
// Occupancy is input_tokens + cache_read_input_tokens. Cache creation is a
// write on this turn, so it is not part of the window the next turn reads.
// A model of "<synthetic>" never counts. ok is false when no turn counted.
func ClaudeContextTokens(r io.Reader) (int64, bool, error) {
	_, _, sample, err := parseClaude(r)
	if err != nil {
		return 0, false, err
	}
	return sample.Tokens, sample.OK, nil
}

func parseClaude(r io.Reader) ([]domain.UsageRecord, int64, contextSample, error) {
	recs, n, sample, _, err := parseClaudeHealth(r, "", nil, newHealthAcc())
	return recs, n, sample, err
}

func parseClaudeHealth(r io.Reader, worktree string, cmds []string, acc *healthAcc) ([]domain.UsageRecord, int64, contextSample, *healthAcc, error) {
	if acc == nil {
		acc = newHealthAcc()
	}
	var recs []domain.UsageRecord
	var sample contextSample
	seen := make(map[string]struct{})
	consumed, err := readLines(r, func(line []byte) error {
		acc.claudeLine(line, worktree, cmds)
		rec, ok := parseClaudeLine(line)
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
		sample = contextSample{
			Tokens: rec.Tokens.Input + rec.Tokens.CacheRead,
			Model:  rec.Model,
			OK:     true,
		}
		return nil
	})
	return recs, consumed, sample, acc, err
}

// ClaudeFingerprint identifies a transcript from its head bytes.
// The first uuid field on any complete line returns "uuid:<value>".
// With no uuid, it returns "sha256:" plus the lowercase hex of the first
// complete line without its newline. An empty head, or a head with no
// complete line, returns "".
func ClaudeFingerprint(head []byte) string {
	var first []byte
	var uuid string
	seen := false
	_, err := readLines(bytes.NewReader(head), func(line []byte) error {
		if !seen {
			seen = true
			first = append([]byte(nil), line...)
		}
		if uuid == "" {
			uuid = lineUUID(line)
		}
		return nil
	})
	if err != nil {
		return ""
	}
	if uuid != "" {
		return "uuid:" + uuid
	}
	if !seen {
		return ""
	}
	sum := sha256.Sum256(first)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type claudeLine struct {
	Type      string    `json:"type"`
	Timestamp string    `json:"timestamp"`
	Message   claudeMsg `json:"message"`
}

type claudeMsg struct {
	ID    string       `json:"id"`
	Model string       `json:"model"`
	Usage *claudeUsage `json:"usage"`
}

type claudeUsage struct {
	InputTokens              int64        `json:"input_tokens"`
	OutputTokens             int64        `json:"output_tokens"`
	CacheReadInputTokens     int64        `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64        `json:"cache_creation_input_tokens"`
	CacheCreation            *claudeCache `json:"cache_creation"`
	Speed                    string       `json:"speed"`
}

type claudeCache struct {
	Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
}

func parseClaudeLine(line []byte) (domain.UsageRecord, bool) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return domain.UsageRecord{}, false
	}
	var row claudeLine
	if err := json.Unmarshal(line, &row); err != nil {
		return domain.UsageRecord{}, false
	}
	if row.Type != "assistant" || row.Message.Model == "<synthetic>" || row.Message.Usage == nil {
		return domain.UsageRecord{}, false
	}
	tokens := row.Message.Usage.counts()
	if tokens.IsZero() {
		return domain.UsageRecord{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return domain.UsageRecord{}, false
	}
	return domain.UsageRecord{
		ID:     row.Message.ID,
		Model:  claudeModel(row.Message.Model, row.Message.Usage.Speed),
		At:     at,
		Tokens: tokens,
	}, true
}

// counts uses the cache_creation split when that object is present.
// cache_creation_input_tokens is 5m only when the object is absent.
func (u claudeUsage) counts() domain.TokenCounts {
	tokens := domain.TokenCounts{
		Input:     u.InputTokens,
		Output:    u.OutputTokens,
		CacheRead: u.CacheReadInputTokens,
	}
	if u.CacheCreation != nil {
		tokens.CacheWrite5m = u.CacheCreation.Ephemeral5m
		tokens.CacheWrite1h = u.CacheCreation.Ephemeral1h
		return tokens
	}
	tokens.CacheWrite5m = u.CacheCreationInputTokens
	return tokens
}

// claudeModel appends "@fast" only for speed "fast".
// "standard" and empty keep the published model id.
func claudeModel(model, speed string) string {
	if speed == "fast" {
		return model + "@fast"
	}
	return model
}

func lineUUID(line []byte) string {
	var probe struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return ""
	}
	return probe.UUID
}
