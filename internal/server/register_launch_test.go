package server

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestRegisterCarriesLaunchModel(t *testing.T) {
	repo := logRepo(t)
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.register", map[string]any{
		"sessions": []domain.Session{{ID: "s1", Name: "n", AgentName: "pi",
			LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("register: %+v", resp.Error)
	}
	got, err := repo.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LaunchModel != "openrouter/qwen/qwen3-coder" || got.LaunchEffort != "high" {
		t.Fatalf("registered launch = %q/%q", got.LaunchModel, got.LaunchEffort)
	}
	info := sessionInfo(*got, "")
	if info.LaunchModel != got.LaunchModel || info.LaunchEffort != got.LaunchEffort {
		t.Fatalf("session info launch = %q/%q", info.LaunchModel, info.LaunchEffort)
	}
}

func TestRegisterAfterAgentSwitchDropsLaunchModel(t *testing.T) {
	repo := logRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{ID: "s1", Name: "n", AgentName: "pi",
		LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.register", map[string]any{
		"sessions": []domain.Session{{ID: "s1", Name: "n", AgentName: "claude"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("register: %+v", resp.Error)
	}
	got, err := repo.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentName != "claude" || got.LaunchModel != "" || got.LaunchEffort != "" {
		t.Fatalf("after switch: agent %q launch %q/%q", got.AgentName, got.LaunchModel, got.LaunchEffort)
	}
}
