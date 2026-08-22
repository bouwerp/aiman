package agenthook

import (
	"encoding/json"
	"strings"
)

// Native is a vendor conversation reference reported by an agent hook.
type Native struct {
	ID   string
	Path string
}

var idKeys = []string{
	"session_id", "sessionId", "sessionID",
	"conversation_id", "conversationId",
	"thread_id", "threadId",
	"agent_session_id", "agentSessionId",
}

// Nested objects often use a bare "id" (conversation.id). That is too ambiguous
// at the top level of a hook payload, so it is only accepted one level down.
var nestedIDKeys = append([]string{"id"}, idKeys...)

var pathKeys = []string{
	"transcript_path", "transcriptPath",
	"session_path", "sessionPath",
	"session_file", "sessionFile",
}

var nestedPathKeys = append([]string{"path"}, pathKeys...)

// ExtractNative pulls a session id and optional transcript path from hook JSON.
func ExtractNative(raw []byte) Native {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return Native{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return Native{}
	}
	return walkNative(v, 0)
}

func walkNative(v any, depth int) Native {
	if depth > 4 {
		return Native{}
	}
	t, ok := v.(map[string]any)
	if !ok {
		return Native{}
	}
	ids, paths := idKeys, pathKeys
	if depth > 0 {
		ids, paths = nestedIDKeys, nestedPathKeys
	}
	n := Native{
		ID:   firstString(t, ids),
		Path: firstString(t, paths),
	}
	if n.ID != "" {
		return n
	}
	for _, key := range []string{"session", "conversation", "thread", "data", "payload"} {
		nested, exists := t[key]
		if !exists {
			continue
		}
		got := walkNative(nested, depth+1)
		if got.ID == "" {
			continue
		}
		n.ID = got.ID
		if n.Path == "" {
			n.Path = got.Path
		}
		return n
	}
	return n
}

func firstString(m map[string]any, keys []string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}
