package ui

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
)

// agentRecordingEngine records the agent each PrepareSession call receives.
type agentRecordingEngine struct{ agents []domain.Agent }

func (e *agentRecordingEngine) Sync(context.Context) error          { return nil }
func (e *agentRecordingEngine) ListSkills() ([]domain.Skill, error) { return nil, nil }
func (e *agentRecordingEngine) PrepareSession(_ context.Context, _ domain.RemoteExecutor, _ string, a domain.Agent, _ []domain.Skill, _ bool, _ *domain.Issue, _ *domain.SessionSnapshot, _ domain.QualityKit) (domain.PreparedSession, error) {
	e.agents = append(e.agents, a)
	return domain.PreparedSession{Command: a.Command}, nil
}

func piLaunchSession() *domain.Session {
	return &domain.Session{ID: "s1", AgentName: "pi", WorkingDirectory: "/w",
		LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high"}
}

func restartAgent(t *testing.T, s domain.Session, agent domain.Agent) domain.Agent {
	t.Helper()
	eng := &agentRecordingEngine{}
	_, _, err := prepareRestartSession(context.Background(), local.NewExecutor(""), eng, restartPrep{
		Session:    s,
		WorkingDir: "/w",
		SessionCfg: domain.SessionConfig{Agent: &agent},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.agents) != 1 {
		t.Fatalf("PrepareSession calls %d, want 1", len(eng.agents))
	}
	return eng.agents[0]
}

func TestSameAgentRestartKeepsLaunchModel(t *testing.T) {
	s := piLaunchSession()
	if adoptRestartAgent(s, "pi") {
		t.Fatal("same agent reported as a switch")
	}
	a := restartAgent(t, *s, domain.Agent{Name: "pi", Command: "pi"})
	if a.LaunchModel != "openrouter/qwen/qwen3-coder" || a.LaunchEffort != "high" {
		t.Fatalf("restart agent = %+v, want the stored launch model and effort", a)
	}
}

func TestAgentSwitchDropsLaunchModel(t *testing.T) {
	s := piLaunchSession()
	if !adoptRestartAgent(s, "claude") {
		t.Fatal("switch not reported")
	}
	if s.LaunchModel != "" || s.LaunchEffort != "" {
		t.Fatalf("switch kept launch %q/%q", s.LaunchModel, s.LaunchEffort)
	}
	a := restartAgent(t, *s, domain.Agent{Name: "claude", Command: "claude"})
	if a.LaunchModel != "" || a.LaunchEffort != "" {
		t.Fatalf("switched agent = %+v, want no launch model", a)
	}
}
