package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func piPreferredAgent() *domain.Agent {
	return &domain.Agent{Name: "Pi", Command: "pi", LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high"}
}

func TestCreateSessionStoresLaunchModel(t *testing.T) {
	remote := &adHocCreateRemote{}
	m := &FlowManager{}
	sess, err := m.CreateSession(context.Background(), domain.SessionConfig{
		AdHoc:      true,
		Branch:     "adhoc-pi",
		Agent:      piPreferredAgent(),
		SSHManager: remote,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.LaunchModel != "openrouter/qwen/qwen3-coder" || sess.LaunchEffort != "high" {
		t.Fatalf("launch = %q/%q, want the preferred entry's", sess.LaunchModel, sess.LaunchEffort)
	}
	if sess.AgentModel != "openrouter/qwen/qwen3-coder" {
		t.Fatalf("AgentModel = %q, want the launch model", sess.AgentModel)
	}
}

func TestReviveRelaunchesWithStoredLaunchModel(t *testing.T) {
	r := &terminalRemote{output: map[string]string{
		"aiman pty get":   `{"error":{"code":"not_found","message":"pty session not found"}}`,
		"native-sessions": "",
	}}
	s := domain.Session{ID: "p", Backend: domain.BackendPTY, AgentName: "pi", AgentSessionID: "conv-1",
		WorkingDirectory: "/w", LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high"}
	if _, err := ReviveIfNeeded(context.Background(), r, &s, ReviveOptions{}); err != nil {
		t.Fatalf("revive: %v", err)
	}
	var payload string
	for path, body := range r.written {
		if strings.HasPrefix(path, "/tmp/aiman-pty-p") {
			payload += string(body)
		}
	}
	for _, want := range []string{"--session conv-1", "--model openrouter/qwen/qwen3-coder --thinking high"} {
		if !strings.Contains(payload, want) {
			t.Errorf("revive payload lacks %q: %s", want, payload)
		}
	}
}
