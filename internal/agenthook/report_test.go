package agenthook

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestExtractReportSessionEnd(t *testing.T) {
	r := ExtractReport([]byte(`{"hook_event_name":"SessionEnd","session_id":"abc"}`))
	if !r.Ended || r.ID != "abc" || r.State != domain.AgentStateIdle || r.Source != SourceSessionEnd {
		t.Fatalf("%+v", r)
	}
}

func TestExtractReportIdlePrompt(t *testing.T) {
	r := ExtractReport([]byte(`{"hookEventName":"Notification","notification_type":"idle_prompt","sessionId":"s1"}`))
	if r.State != domain.AgentStateIdle || r.Source != SourceIdlePrompt || r.ID != "s1" {
		t.Fatalf("%+v", r)
	}
}

func TestExtractReportBlockedTitle(t *testing.T) {
	r := ExtractReport([]byte(`{"state":"blocked","message":"git push","title":"fix auth","session_id":"n9","source":"lifecycle"}`))
	if r.State != domain.AgentStateWaitingInput || r.Message != "git push" || r.Title != "fix auth" {
		t.Fatalf("%+v", r)
	}
}

func TestApplyReportSeqAndIdentityOnly(t *testing.T) {
	s := domain.Session{HookState: domain.AgentStateWorking, HookStateSeq: 5, HookStateSource: SourceLifecycle}
	ApplyReport(&s, Report{Native: Native{ID: "n1"}, Seq: 4, State: domain.AgentStateIdle, Source: SourceLifecycle}, time.Now())
	if s.HookState != domain.AgentStateWorking {
		t.Fatal("stale seq rewound state")
	}
	if s.AgentSessionID != "n1" {
		t.Fatal("identity not applied")
	}
	ApplyReport(&s, Report{State: domain.AgentStateIdle, Source: SourceIdlePrompt, Seq: 6}, time.Now())
	if s.HookState != domain.AgentStateIdle {
		t.Fatalf("seq 6: %s", s.HookState)
	}
}

func TestResolveHookStateTTL(t *testing.T) {
	now := time.Now()
	s := domain.Session{
		HookState: domain.AgentStateIdle, HookStateSource: SourceIdlePrompt,
		HookStateAt: now.Add(-3 * time.Minute),
	}
	if _, ok := ResolveHookState(s, now); ok {
		t.Fatal("expired")
	}
	s.HookStateAt = now
	st, ok := ResolveHookState(s, now)
	if !ok || st != domain.AgentStateIdle {
		t.Fatalf("%s %v", st, ok)
	}
	s.AgentEnded = true
	st, ok = ResolveHookState(s, now)
	if !ok || st != domain.AgentStateIdle {
		t.Fatal("ended")
	}
}
