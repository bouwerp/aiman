package handoverchild

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestPickAgentPrefersTarget(t *testing.T) {
	m := NewManager(Deps{
		Installed: func(a string) bool { return a == "claude" || a == "grok" },
		Candidates: func(worker string) []config.PreferredAgent {
			return []config.PreferredAgent{{Agent: "claude"}, {Agent: worker}}
		},
	})
	t.Cleanup(m.Close)
	got := m.pickAgent("claude", "grok")
	if got.Agent != "grok" {
		t.Fatalf("pickAgent(claude, grok) = %+v, want grok first", got)
	}
}

func TestPickAgentResolvesDisplayName(t *testing.T) {
	m := NewManager(Deps{
		Installed: func(a string) bool { return a == "claude" || a == "grok" },
		Candidates: func(worker string) []config.PreferredAgent {
			return []config.PreferredAgent{{Agent: worker}}
		},
	})
	t.Cleanup(m.Close)
	got := m.pickAgent("Claude Code", "Grok Build CLI")
	if got.Agent != "grok" {
		t.Fatalf("pickAgent display names = %+v, want grok", got)
	}
}

func TestPickAgentEmptyPreferredKeepsCandidateOrder(t *testing.T) {
	m := NewManager(Deps{
		Installed: func(a string) bool { return a == "claude" || a == "grok" },
		Candidates: func(string) []config.PreferredAgent {
			return []config.PreferredAgent{{Agent: "claude"}, {Agent: "grok"}}
		},
	})
	t.Cleanup(m.Close)
	got := m.pickAgent("claude", "")
	if got.Agent != "claude" {
		t.Fatalf("empty preferred = %+v, want the first installed candidate", got)
	}
}

func TestPickAgentSkipsSourceWhenTargetMissing(t *testing.T) {
	m := NewManager(Deps{
		Installed: func(a string) bool { return a == "codex" },
		Candidates: func(string) []config.PreferredAgent {
			return []config.PreferredAgent{{Agent: "claude"}, {Agent: "grok"}, {Agent: "codex"}}
		},
	})
	t.Cleanup(m.Close)
	got := m.pickAgent("claude", "grok")
	if got.Agent != "codex" {
		t.Fatalf("missing target = %+v, want the next installed agent, not the source", got)
	}
}

func TestPickAgentKeepsTargetEntryModel(t *testing.T) {
	m := NewManager(Deps{
		Installed: func(a string) bool { return a == "grok" },
		Candidates: func(string) []config.PreferredAgent {
			return []config.PreferredAgent{
				{Agent: "claude"},
				{Agent: "grok", Model: "grok-4", Effort: "high"},
			}
		},
	})
	t.Cleanup(m.Close)
	got := m.pickAgent("claude", "grok")
	want := config.PreferredAgent{Agent: "grok", Model: "grok-4", Effort: "high"}
	if got != want {
		t.Fatalf("pickAgent = %+v, want %+v", got, want)
	}
}

func TestStartLaunchesPreferredAgent(t *testing.T) {
	r := newRig(t)
	r.m.Close()
	r.m = NewManager(Deps{
		Store: r.store, Sessions: r.sessions, Terminal: r.term, Steer: r.steer, Ledger: r.ledger,
		Context: r.ctx, Jobs: r.jobs,
		Installed: func(a string) bool { return a == "claude" || a == "grok" },
		Candidates: func(worker string) []config.PreferredAgent {
			return []config.PreferredAgent{{Agent: worker}, {Agent: "grok"}}
		},
		BaseDir: r.base, Now: r.clock.Now, Poll: time.Millisecond, Timeout: 10 * time.Minute,
	})
	t.Cleanup(r.m.Close)
	id, started, err := r.m.Start(context.Background(), StartRequest{
		Worker: r.worker, Reason: "agent_switch", Agent: "grok",
		Mechanical: mechanicalRender(), MechanicalSHA: "sha-1",
		Label: "w1", Abstract: "mechanical abstract",
	})
	if err != nil || !started {
		t.Fatalf("start: id %q started %v err %v", id, started, err)
	}
	r.waitRunning(t, id)
	cfgs := r.sessions.createdConfigs()
	if len(cfgs) != 1 || cfgs[0].Agent == nil {
		t.Fatalf("created %+v", cfgs)
	}
	if cmd := cfgs[0].Agent.Command; cmd != "grok" {
		t.Fatalf("child agent %q, want grok (the switch target)", cmd)
	}
}

func TestStartJoinsUncleanedRun(t *testing.T) {
	r := newRig(t)
	r.term.sticky = true
	id := r.start(t, "agent_switch")
	r.waitRunning(t, id)
	r.clock.advance(11 * time.Minute)
	r.waitRun(t, id, func(x Run) bool { return x.Status == StatusTimedOut && !x.CleanedUp })

	id2, started, err := r.m.Start(context.Background(), StartRequest{
		Worker: r.worker, Reason: "agent_switch", Mechanical: mechanicalRender(), MechanicalSHA: "sha-2",
		Label: "w1", Abstract: "later",
	})
	if err != nil {
		t.Fatal(err)
	}
	if started || id2 != id {
		t.Fatalf("second start id %q started %v, want join of %q", id2, started, id)
	}
	if n := len(r.sessions.createdConfigs()); n != 1 {
		t.Fatalf("%d children, want 1 (the uncleaned run must not stack)", n)
	}
	run, err := r.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.LaterReasons) != 1 || run.LaterReasons[0] != "agent_switch" {
		t.Fatalf("later reasons %+v", run.LaterReasons)
	}
}

func TestCleanupDropsExtraHandoverSiblings(t *testing.T) {
	r := newRig(t)
	r.sessions.put(domain.Session{ID: "stale-a", ParentID: "w1", Role: domain.SessionRoleHandover})
	r.sessions.put(domain.Session{ID: "stale-b", ParentID: "w1", Role: domain.SessionRoleHandover})
	r.sessions.put(domain.Session{ID: "consult", ParentID: "w1", Role: domain.SessionRoleConsult})
	id := r.start(t, "agent_switch")
	run := r.waitRunning(t, id)
	writeOutputs(t, run.Dir, validChildHandover(), childMemories)
	r.waitCleaned(t, id)
	got := map[string]bool{}
	for _, id := range r.sessions.forgottenIDs() {
		got[id] = true
	}
	for _, want := range []string{"child-1", "stale-a", "stale-b"} {
		if !got[want] {
			t.Fatalf("forgotten %v, missing %s", r.sessions.forgottenIDs(), want)
		}
	}
	if got["consult"] {
		t.Fatalf("forgot consult child %v", r.sessions.forgottenIDs())
	}
}

func TestResumeDropsStaleHandoverChildren(t *testing.T) {
	r := newRig(t)
	r.sessions.put(domain.Session{ID: "stale", ParentID: "w1", Role: domain.SessionRoleHandover})
	r.putRun(t, Run{
		ID: "old", SessionID: "w1", CleanedUp: true, Status: StatusComplete,
		StartedAt: r.clock.Now(),
	})
	r.m.Resume(context.Background())
	if got := r.sessions.forgottenIDs(); len(got) != 1 || got[0] != "stale" {
		t.Fatalf("forgotten %v, want [stale]", got)
	}
}
