package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func twoRemoteCfg() *config.Config {
	return &config.Config{Remotes: []config.Remote{
		{Name: "dev-box", Host: "10.0.1.5", User: "ubuntu", Root: "/home/ubuntu"},
		{Name: "build-2", Host: "10.0.1.9", User: "ec2-user", Root: "/home/ec2-user"},
	}}
}

func pressMainKey(m *Model, r rune) *Model {
	updated, _, _ := m.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	if mm, ok := updated.(*Model); ok {
		return mm
	}
	return m
}

// The run-target picker must be reachable no matter how many remotes are configured,
// because it is the only way to start an EC2 loop.
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
	updated, _ := m.handleRunTargetPickerUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
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
	if got.sessionCfg.IsEC2Loop {
		t.Error("a remote target is not an EC2 loop")
	}
}

func TestRunTargetPickerIgnoresOutOfRangeIndex(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker}
	updated, _ := m.handleRunTargetPickerUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	if got := updated.(*Model); got.state != viewStateRunTargetPicker {
		t.Fatalf("expected to stay on the picker, got %v", got.state)
	}
}

func TestRunTargetPickerSelectsEC2Loop(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker,
		selectedRemote: config.Remote{Host: "stale.example", User: "ubuntu"}}
	updated, cmd := m.handleRunTargetPickerUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	got := updated.(*Model)

	if got.state != viewStateIssuePicker {
		t.Fatalf("expected the issue picker, got %v", got.state)
	}
	if !got.sessionCfg.IsEC2Loop {
		t.Error("expected IsEC2Loop set")
	}
	if got.selectedRemote.Host != "" {
		t.Errorf("an EC2 loop uses no remote server, got %q", got.selectedRemote.Host)
	}
	if got.sessionCfg.RemoteHost != "" {
		t.Errorf("an EC2 loop must not carry a RemoteHost, got %q", got.sessionCfg.RemoteHost)
	}
	if cmd == nil {
		t.Error("expected the Jira search to be kicked off")
	}
}

func TestRunTargetPickerEC2LoopWithNoRemotes(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateRunTargetPicker}
	updated, _ := m.handleRunTargetPickerUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if got := updated.(*Model); !got.sessionCfg.IsEC2Loop || got.state != viewStateIssuePicker {
		t.Fatalf("EC2 must be selectable with no remotes configured, got state %v", got.state)
	}
}

func TestRunTargetPickerEscCancels(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker}
	updated, _ := m.handleRunTargetPickerUpdate(tea.KeyMsg{Type: tea.KeyEsc})
	if got := updated.(*Model); got.state != viewStateMain {
		t.Fatalf("expected esc to return to the dashboard, got %v", got.state)
	}
}

func TestRunTargetPickerViewListsRemotesAndEC2(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker, width: 100, height: 40}
	out := m.renderView()
	for _, want := range []string{"dev-box", "build-2", "[1]", "[2]", "[e]", "EC2"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the picker, got:\n%s", want, out)
		}
	}
}

func TestRunTargetPickerViewWithNoRemotes(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateRunTargetPicker, width: 100, height: 40}
	out := m.renderView()
	if !strings.Contains(out, "[e]") {
		t.Error("EC2 must still be offered with no remotes configured")
	}
	if !strings.Contains(out, "No remote servers configured") {
		t.Errorf("expected a note about adding a remote, got:\n%s", out)
	}
}

// EC2 is chosen up front now, so the mode picker must not offer it a second time.
func TestModePickerNoLongerOffersEC2(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateModePicker, width: 100, height: 40,
		selectedRemote: twoRemoteCfg().Remotes[0]}
	if out := m.renderView(); strings.Contains(out, "EC2") {
		t.Errorf("the mode picker must not mention EC2 any more, got:\n%s", out)
	}

	updated, _ := m.handleModePickerUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
	if got := updated.(*Model); got.sessionCfg.IsEC2Loop {
		t.Error("'6' must no longer start an EC2 loop")
	}
}

// The mode picker resets sessionCfg per branch, so the chosen remote has to be reapplied
// or the session would be created with an empty RemoteHost.
func TestModePickerKeepsSelectedRemote(t *testing.T) {
	remote := twoRemoteCfg().Remotes[0]
	m := &Model{cfg: twoRemoteCfg(), state: viewStateModePicker, selectedRemote: remote,
		sessionCfg: domain.SessionConfig{RemoteHost: remote.Host}}
	updated, _ := m.handleModePickerUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if got := updated.(*Model); got.sessionCfg.RemoteHost != remote.Host {
		t.Errorf("expected RemoteHost %q preserved, got %q", remote.Host, got.sessionCfg.RemoteHost)
	}
}

// Going back from a screen must land on the one the flow actually came from, and the EC2
// flow skips the mode picker entirely.
func TestEscFromIssuePickerRespectsEC2Flow(t *testing.T) {
	for _, tc := range []struct {
		name string
		ec2  bool
		want viewState
	}{
		{"ec2 loop", true, viewStateRunTargetPicker},
		{"remote session", false, viewStateModePicker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{cfg: twoRemoteCfg(), state: viewStateIssuePicker,
				issuePicker: NewIssuePickerModel(nil),
				sessionCfg:  domain.SessionConfig{IsEC2Loop: tc.ec2}}
			updated, _ := m.handleIssuePickerUpdate(tea.KeyMsg{Type: tea.KeyEsc})
			if got := updated.(*Model); got.state != tc.want {
				t.Fatalf("expected state %v, got %v", tc.want, got.state)
			}
		})
	}
}

func TestEscFromAgentPickerRespectsEC2Flow(t *testing.T) {
	for _, tc := range []struct {
		name string
		ec2  bool
		want viewState
	}{
		{"ec2 loop", true, viewStateRepoPicker},
		{"remote session", false, viewStateDirPicker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{cfg: twoRemoteCfg(), state: viewStateAgentPicker,
				agentPicker: NewAgentPickerModel(nil),
				sessionCfg:  domain.SessionConfig{IsEC2Loop: tc.ec2}}
			updated, _ := m.handleAgentPickerUpdate(tea.KeyMsg{Type: tea.KeyEsc})
			if got := updated.(*Model); got.state != tc.want {
				t.Fatalf("expected state %v, got %v", tc.want, got.state)
			}
		})
	}
}

// An EC2 instance is provisioned from scratch, so the agent list cannot come from an SSH
// scan of some other machine.
func TestFetchAgentsUsesKnownAgentsForEC2Loop(t *testing.T) {
	m := &Model{cfg: &config.Config{}, sessionCfg: domain.SessionConfig{IsEC2Loop: true}}
	msg, ok := m.fetchAgents()().(agent.ScanAgentsMsg)
	if !ok {
		t.Fatal("expected a ScanAgentsMsg")
	}
	if msg.Err != nil {
		t.Fatalf("unexpected error: %v", msg.Err)
	}
	if len(msg.Agents) != len(agent.KnownAgents()) {
		t.Errorf("expected all %d known agents, got %d", len(agent.KnownAgents()), len(msg.Agents))
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
