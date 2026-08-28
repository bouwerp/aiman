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

// SessionEnd fires for reasons that do not stop the agent — Claude Code emits it
// on /clear and on /compact — so the flag has to be retractable. It used to
// latch, and a session the user was still working in rendered as "exited" for
// the rest of its life.
func TestApplyReportClearsEndedOnALaterLiveReport(t *testing.T) {
	now := time.Now()
	s := &domain.Session{}
	ApplyReport(s, Report{Ended: true, Seq: 1}, now)
	if !s.AgentEnded {
		t.Fatal("SessionEnd should mark the session ended")
	}
	ApplyReport(s, Report{
		State:  domain.AgentStateIdle,
		Source: SourceIdlePrompt,
		Seq:    2,
	}, now)
	if s.AgentEnded {
		t.Error("a later live report must retract the end")
	}
	if st, ok := ResolveHookState(*s, now); !ok || st != domain.AgentStateIdle {
		t.Errorf("got %q ok=%v, want the live idle state", st, ok)
	}
}

// An identity-only report says nothing about the lifecycle, so it must not
// resurrect a session that really did end.
func TestApplyReportKeepsEndedForIdentityOnlyReports(t *testing.T) {
	now := time.Now()
	s := &domain.Session{}
	ApplyReport(s, Report{Ended: true, Seq: 1}, now)
	ApplyReport(s, Report{Native: Native{ID: "abc"}, Seq: 2}, now)
	if !s.AgentEnded {
		t.Error("an identity-only report must not clear AgentEnded")
	}
}

// A stale report loses to the seq check before it can retract anything.
func TestApplyReportIgnoresAnOutOfOrderLiveReport(t *testing.T) {
	now := time.Now()
	s := &domain.Session{}
	ApplyReport(s, Report{State: domain.AgentStateWorking, Seq: 5}, now)
	ApplyReport(s, Report{Ended: true, State: domain.AgentStateIdle, Seq: 6}, now)
	ApplyReport(s, Report{State: domain.AgentStateWorking, Seq: 4}, now)
	if !s.AgentEnded {
		t.Error("an out-of-order report must not retract the end")
	}
}
