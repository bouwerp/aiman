package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func twoRemoteCfg() *config.Config {
	return &config.Config{Remotes: []config.Remote{
		{Name: "dev-box", Host: "10.0.1.5", User: "ubuntu", Root: "/home/ubuntu"},
		{Name: "build-2", Host: "10.0.1.9", User: "ec2-user", Root: "/home/ec2-user"},
	}}
}

func pressMainKey(m *Model, r rune) *Model {
	updated, _, _ := m.handleMainKeyMsg(pressRune(r))
	if mm, ok := updated.(*Model); ok {
		return mm
	}
	return m
}

// The run-target picker must be reachable no matter how many remotes are
// configured, so a fresh install with zero remotes still has somewhere obvious to go.
func TestNewSessionAlwaysOpensRunTargetPicker(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"two remotes", twoRemoteCfg()},
		{"one remote", &config.Config{Remotes: twoRemoteCfg().Remotes[:1]}},
		{"no remotes", &config.Config{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pressMainKey(&Model{cfg: tc.cfg, state: viewStateMain}, 'n')
			if m.state != viewStateRunTargetPicker {
				t.Fatalf("expected the run-target picker, got state %v", m.state)
			}
			if m.lastError != "" {
				t.Fatalf("no remotes is not an error any more, got %q", m.lastError)
			}
		})
	}
}

func TestRunTargetPickerSelectsRemote(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker}
	updated, _ := m.handleRunTargetPickerUpdate(pressKey("2"))
	got := updated.(*Model)

	if got.state != viewStateModePicker {
		t.Fatalf("expected the mode picker after choosing a remote, got %v", got.state)
	}
	if got.selectedRemote.Host != "10.0.1.9" {
		t.Errorf("expected the second remote selected, got %q", got.selectedRemote.Host)
	}
	if got.sessionCfg.RemoteHost != "10.0.1.9" {
		t.Errorf("expected RemoteHost carried into sessionCfg, got %q", got.sessionCfg.RemoteHost)
	}
}

func TestRunTargetPickerIgnoresOutOfRangeIndex(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker}
	updated, _ := m.handleRunTargetPickerUpdate(pressKey("7"))
	if got := updated.(*Model); got.state != viewStateRunTargetPicker {
		t.Fatalf("expected to stay on the picker, got %v", got.state)
	}
}

func TestRunTargetPickerEscCancels(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker}
	updated, _ := m.handleRunTargetPickerUpdate(pressKey("esc"))
	if got := updated.(*Model); got.state != viewStateMain {
		t.Fatalf("expected esc to return to the dashboard, got %v", got.state)
	}
}

func TestRunTargetPickerViewListsRemotes(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker, width: 100, height: 40}
	out := m.renderView()
	for _, want := range []string{"dev-box", "build-2", "[1]", "[2]"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the picker, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[e]") || strings.Contains(out, "EC2") {
		t.Errorf("the EC2 loop option must be gone, got:\n%s", out)
	}
}

func TestRunTargetPickerViewWithNoRemotes(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateRunTargetPicker, width: 100, height: 40}
	out := m.renderView()
	if !strings.Contains(out, "No remote servers configured") {
		t.Errorf("expected a note about adding a remote, got:\n%s", out)
	}
}

// The mode picker must not offer an EC2 loop target any more.
func TestModePickerDoesNotOfferEC2(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateModePicker, width: 100, height: 40,
		selectedRemote: twoRemoteCfg().Remotes[0]}
	if out := m.renderView(); strings.Contains(out, "EC2") {
		t.Errorf("the mode picker must not mention EC2 any more, got:\n%s", out)
	}

	updated, _ := m.handleModePickerUpdate(pressKey("6"))
	updatedModel, cmd := updated.Update(pressKey("enter"))
	_ = updatedModel
	if cmd != nil {
		t.Log("'6' still produces a command; verify it is not an EC2 loop launch")
	}
}

// The mode picker resets sessionCfg per branch, so the chosen remote has to be reapplied
// or the session would be created with an empty RemoteHost.
func TestModePickerKeepsSelectedRemote(t *testing.T) {
	remote := twoRemoteCfg().Remotes[0]
	m := &Model{cfg: twoRemoteCfg(), state: viewStateModePicker, selectedRemote: remote,
		sessionCfg: domain.SessionConfig{RemoteHost: remote.Host}}
	updated, _ := m.handleModePickerUpdate(pressKey("2"))
	if got := updated.(*Model); got.sessionCfg.RemoteHost != remote.Host {
		t.Errorf("expected RemoteHost %q preserved, got %q", remote.Host, got.sessionCfg.RemoteHost)
	}
}

// Going back from the issue picker lands on the mode picker for remote sessions.
func TestEscFromIssuePickerReturnsToModePicker(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateIssuePicker,
		issuePicker: NewIssuePickerModel(nil),
		sessionCfg:  domain.SessionConfig{RemoteHost: "10.0.1.5"}}
	updated, _ := m.handleIssuePickerUpdate(pressKey("esc"))
	if got := updated.(*Model); got.state != viewStateModePicker {
		t.Fatalf("expected state %v, got %v", viewStateModePicker, got.state)
	}
}

func TestKnownAgentsIsACopy(t *testing.T) {
	first := agent.KnownAgents()
	if len(first) == 0 {
		t.Fatal("expected known agents")
	}
	original := first[0].Name
	first[0].Name = "mutated"
	if agent.KnownAgents()[0].Name != original {
		t.Error("KnownAgents must return a copy callers cannot corrupt")
	}
}
