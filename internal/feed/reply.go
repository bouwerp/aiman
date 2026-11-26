package feed

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// Reply is the agent's answer to one batch: the last JSON object it printed.
type Reply struct {
	Actions   []ReplyAction `json:"actions"`
	Digest    []DigestLine  `json:"digest"`
	MemoryOps []MemoryOp    `json:"memory_ops"`
	Summary   string        `json:"summary"`
}

// ReplyAction is one proposed action as the agent wrote it.
type ReplyAction struct {
	Kind      domain.FeedActionKind `json:"kind"`
	Priority  domain.FeedPriority   `json:"priority"`
	Rationale string                `json:"rationale"`
	Answers   []int64               `json:"answers"`
	Cites     Cites                 `json:"cites"`
	Options   []domain.FeedOption   `json:"options"`
}

// Cites are the ids an action names beside the events it answers.
type Cites struct {
	Events    []int64  `json:"events"`
	Memory    []string `json:"memory"`
	Decisions []int64  `json:"decisions"`
}

// DigestLine is the agent's one line for an event it did not act on.
type DigestLine struct {
	Event int64  `json:"event"`
	Line  string `json:"line"`
}

// MemoryOp adds or removes one memory bullet.
type MemoryOp struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`
}

// ParseReply reads the last JSON object in stdout. It fails only when stdout
// holds no JSON object or that object does not have the reply's shape. An
// action that does not decode, or has 0 or more than MaxOptions options, is
// dropped and the rest of the reply stands.
func ParseReply(stdout string) (Reply, error) {
	raw := lastObject(stdout)
	if raw == nil {
		return Reply{}, errors.New("the reply holds no JSON object")
	}
	var top struct {
		Actions   []json.RawMessage `json:"actions"`
		Digest    []DigestLine      `json:"digest"`
		MemoryOps []MemoryOp        `json:"memory_ops"`
		Summary   string            `json:"summary"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return Reply{}, fmt.Errorf("the reply does not parse: %w", err)
	}
	r := Reply{Digest: top.Digest, MemoryOps: top.MemoryOps, Summary: top.Summary}
	for _, a := range top.Actions {
		var act ReplyAction
		if err := json.Unmarshal(a, &act); err != nil {
			continue
		}
		if len(act.Options) == 0 || len(act.Options) > MaxOptions {
			continue
		}
		r.Actions = append(r.Actions, act)
	}
	return r, nil
}

// lastObject returns the last top-level JSON object in s: it scans forward,
// and each '{' that starts a complete object is taken whole and skipped, so
// objects nested inside it are never candidates.
func lastObject(s string) json.RawMessage {
	var last json.RawMessage
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], '{')
		if j < 0 {
			break
		}
		i += j
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var obj json.RawMessage
		if err := dec.Decode(&obj); err == nil && len(obj) > 0 && obj[0] == '{' {
			last = obj
			i += int(dec.InputOffset())
			continue
		}
		i++
	}
	return last
}
