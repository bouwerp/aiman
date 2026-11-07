package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	// codexHeadMax bounds the session_meta line, which holds base_instructions.
	codexHeadMax = 1 << 20
	// codexTailBytes is how much of a rollout's end is read for its state. A
	// child's last lifecycle event is among its final lines, and a rollout can
	// run to tens of megabytes, so the middle is never read.
	codexTailBytes = 256 << 10
)

type codexChildMeta struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		ID             string          `json:"id"`
		ParentThreadID string          `json:"parent_thread_id"`
		Timestamp      string          `json:"timestamp"`
		AgentNickname  string          `json:"agent_nickname"`
		Source         json.RawMessage `json:"source"`
	} `json:"payload"`
}

// CodexChildHead reads the head of a Codex child rollout: the parent's
// thread id from session_meta.payload.parent_thread_id, and a Subagent whose
// ID is the child's thread id, Name the spawn nickname (else the
// source.subagent string), StartedAt the session_meta time, and State done
// with EndedAt set when the rollout holds task_complete (or ends on a final
// AgentMessage), else running. A turn_aborted as the last lifecycle event is
// failed. A head without parent_thread_id is an error.
//
// Only the first line and a bounded tail of the named file are read.
// ItemCount stays zero: counting items takes the whole file.
func CodexChildHead(path string) (parentThreadID string, sa Subagent, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: the locator names a rollout it found under the Codex sessions tree.
	if err != nil {
		return "", Subagent{}, fmt.Errorf("codex child head: %w", err)
	}
	defer func() { _ = f.Close() }()

	meta, err := codexReadMeta(f)
	if err != nil {
		return "", Subagent{}, fmt.Errorf("codex child head %s: %w", path, err)
	}
	tail, err := codexReadTail(f)
	if err != nil {
		return "", Subagent{}, fmt.Errorf("codex child head %s: %w", path, err)
	}
	sa = Subagent{ID: meta.Payload.ID, Name: codexChildName(meta), State: SubagentRunning}
	start := codexTime(meta.Payload.Timestamp)
	if start.IsZero() {
		start = codexTime(meta.Timestamp)
	}
	if !start.IsZero() {
		sa.StartedAt = &start
	}
	if state, at := codexChildState(tail); state != SubagentRunning {
		sa.State, sa.EndedAt = state, &at
	}
	return meta.Payload.ParentThreadID, sa, nil
}

func codexReadMeta(f io.Reader) (codexChildMeta, error) {
	var meta codexChildMeta
	line, err := bufio.NewReader(io.LimitReader(f, codexHeadMax)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return meta, err
	}
	if err := json.Unmarshal(line, &meta); err != nil {
		return meta, fmt.Errorf("first line: %w", err)
	}
	if meta.Type != "session_meta" || meta.Payload.ParentThreadID == "" {
		return meta, errors.New("no parent_thread_id in session_meta")
	}
	return meta, nil
}

// codexReadTail returns the complete lines in the last codexTailBytes.
func codexReadTail(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(info.Size()-codexTailBytes, 0)
	buf := make([]byte, info.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if off > 0 {
		_, buf, _ = bytes.Cut(buf, []byte("\n"))
	}
	return buf, nil
}

// codexChildName is the spawn nickname, else source.subagent when it is a
// plain string such as "review" or {"other": "guardian"}.
func codexChildName(meta codexChildMeta) string {
	var src struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(meta.Payload.Source, &src) != nil {
		return meta.Payload.AgentNickname
	}
	var kind string
	if json.Unmarshal(src.Subagent, &kind) == nil {
		return kind
	}
	var spawn struct {
		ThreadSpawn struct {
			AgentNickname string `json:"agent_nickname"`
		} `json:"thread_spawn"`
		Other string `json:"other"`
	}
	if json.Unmarshal(src.Subagent, &spawn) == nil {
		if spawn.ThreadSpawn.AgentNickname != "" {
			return spawn.ThreadSpawn.AgentNickname
		}
		if spawn.Other != "" {
			return spawn.Other
		}
	}
	return meta.Payload.AgentNickname
}

// codexChildState reads lifecycle events in order: the last one decides, so a
// new turn after task_complete means the child is running again.
func codexChildState(tail []byte) (string, time.Time) {
	state, at := SubagentRunning, time.Time{}
	for line := range bytes.SplitSeq(tail, []byte("\n")) {
		var env codexEnvelope
		if json.Unmarshal(line, &env) != nil || env.Type != "event_msg" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type  string `json:"type"`
				Phase string `json:"phase"`
			} `json:"item"`
		}
		if json.Unmarshal(env.Payload, &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "task_complete", ev.Item.Type == "AgentMessage" && ev.Item.Phase == "final_answer":
			state, at = SubagentDone, codexTime(env.Timestamp)
		case ev.Type == "turn_aborted":
			state, at = SubagentFailed, codexTime(env.Timestamp)
		case ev.Type == "task_started", ev.Item.Type == "UserMessage":
			state, at = SubagentRunning, time.Time{}
		}
	}
	return state, at
}
