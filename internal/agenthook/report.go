package agenthook

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	SourceLifecycle  = "lifecycle"
	SourceIdlePrompt = "idle_prompt"
	SourceSessionEnd = "session_end"

	hookTTL = 2 * time.Minute
)

// Report is identity plus optional lifecycle facts extracted from a hook payload.
type Report struct {
	Native
	Event   string
	State   domain.AgentState
	Source  string
	Message string
	Title   string
	Ended   bool
	Seq     int64
}

// ExtractReport pulls identity, title, ended, and (when present) semantic state.
func ExtractReport(raw []byte) Report {
	r := Report{Native: ExtractNative(raw)}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return r
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return r
	}
	fillReport(&r, v, 0)
	inferFromEvent(&r)
	r.State = normalizeState(string(r.State))
	if r.Ended && r.State == "" {
		r.State = domain.AgentStateIdle
	}
	if r.Ended && r.Source == "" {
		r.Source = SourceSessionEnd
	}
	return r
}

func fillReport(r *Report, v any, depth int) {
	if depth > 4 || r == nil {
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if r.Event == "" {
		r.Event = firstString(m, []string{"hook_event_name", "hookEventName", "event"})
	}
	if r.Title == "" {
		r.Title = firstString(m, []string{"session_title", "sessionTitle", "title", "custom_status", "customStatus"})
	}
	if r.Message == "" {
		r.Message = firstString(m, []string{"message", "reason", "label"})
	}
	if r.Source == "" {
		r.Source = firstString(m, []string{"source", "state_source", "stateSource"})
	}
	if r.State == "" {
		r.State = normalizeState(firstString(m, []string{"state", "agent_state", "agentState"}))
	}
	if r.Seq == 0 {
		r.Seq = firstInt(m, []string{"seq", "sequence"})
	}
	if !r.Ended {
		r.Ended = firstBool(m, []string{"ended", "agent_ended", "agentEnded"})
	}
	if nt := firstString(m, []string{"notification_type", "notificationType"}); strings.Contains(strings.ToLower(nt), "idle") {
		r.State = domain.AgentStateIdle
		r.Source = SourceIdlePrompt
		if r.Event == "" {
			r.Event = "Notification"
		}
	}
	for _, key := range []string{"session", "conversation", "thread", "data", "payload", "properties", "info"} {
		if nested, exists := m[key]; exists {
			fillReport(r, nested, depth+1)
		}
	}
}

func inferFromEvent(r *Report) {
	ev := strings.ToLower(strings.ReplaceAll(r.Event, "-", ""))
	ev = strings.ReplaceAll(ev, "_", "")
	switch {
	case strings.Contains(ev, "sessionend") || ev == "session.deleted" || ev == "sessiondeleted":
		r.Ended = true
		if r.Source == "" {
			r.Source = SourceSessionEnd
		}
		if r.State == "" {
			r.State = domain.AgentStateIdle
		}
	case strings.Contains(ev, "notification"):
		nt := strings.ToLower(r.Message)
		if strings.Contains(nt, "idle") {
			r.State = domain.AgentStateIdle
			r.Source = SourceIdlePrompt
			r.Message = ""
		}
	}
}

func normalizeState(v string) domain.AgentState {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "idle":
		return domain.AgentStateIdle
	case "working", "busy":
		return domain.AgentStateWorking
	case "waiting_input", "blocked", "waitinginput":
		return domain.AgentStateWaitingInput
	case "errored", "error":
		return domain.AgentStateErrored
	default:
		return ""
	}
}

func firstInt(m map[string]any, keys []string) int64 {
	for _, k := range keys {
		switch t := m[k].(type) {
		case float64:
			return int64(t)
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
			if err == nil {
				return n
			}
		}
	}
	return 0
}

func firstBool(m map[string]any, keys []string) bool {
	for _, k := range keys {
		switch t := m[k].(type) {
		case bool:
			return t
		case string:
			s := strings.ToLower(strings.TrimSpace(t))
			if s == "1" || s == "true" || s == "yes" {
				return true
			}
		}
	}
	return false
}

// ApplyReport copies non-empty facts onto the session. A lower seq cannot
// rewind state. Identity-only reports leave an existing lifecycle state.
func ApplyReport(s *domain.Session, r Report, now time.Time) {
	if s == nil {
		return
	}
	r.State = normalizeState(string(r.State))
	if r.ID != "" {
		s.AgentSessionID = r.ID
	}
	if r.Path != "" {
		s.AgentSessionPath = r.Path
	}
	if r.Title != "" {
		s.AgentTitle = r.Title
	}
	if r.Ended {
		s.AgentEnded = true
		if r.State == "" {
			r.State = domain.AgentStateIdle
		}
		if r.Source == "" {
			r.Source = SourceSessionEnd
		}
	}
	if r.State == "" {
		return
	}
	if r.Seq > 0 && s.HookStateSeq > 0 && r.Seq < s.HookStateSeq {
		return
	}
	s.HookState = r.State
	s.HookStateSource = r.Source
	s.HookStateMessage = r.Message
	s.HookStateSeq = r.Seq
	s.HookStateAt = now
}

// ResolveHookState returns a high-confidence state when a fresh hook report
// should beat screen classification. Expired reports fall through.
func ResolveHookState(s domain.Session, now time.Time) (domain.AgentState, bool) {
	if s.AgentEnded {
		return domain.AgentStateIdle, true
	}
	if s.HookState == "" {
		return "", false
	}
	if !s.HookStateAt.IsZero() && now.Sub(s.HookStateAt) > hookTTL {
		return "", false
	}
	switch s.HookStateSource {
	case SourceLifecycle, SourceIdlePrompt, SourceSessionEnd:
		return s.HookState, true
	default:
		return "", false
	}
}

func (r Report) empty() bool {
	return r.ID == "" && r.Path == "" && r.State == "" && r.Title == "" && !r.Ended
}
