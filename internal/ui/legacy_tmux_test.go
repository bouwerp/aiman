package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func legacyRow() domain.Session {
	return domain.Session{
		ID: "legacy-1", RemoteHost: "devbox", TerminalName: "PB-1", RepoName: "org/repo",
		Status: domain.SessionStatusInactive, AgentName: "Codex CLI",
		Backend: domain.BackendLegacyTmux,
	}
}

func legacyStopCommand(s domain.Session) string {
	var lt *domain.LegacyTmuxError
	if !errors.As(domain.NewLegacyTmuxError(s), &lt) {
		panic("NewLegacyTmuxError did not return *LegacyTmuxError")
	}
	return lt.StopCommand()
}

func TestRunTargetPickerHasNoBackendToggle(t *testing.T) {
	m := &Model{cfg: twoRemoteCfg(), state: viewStateRunTargetPicker}
	m = pickKey(m, "1")
	before := m.sessionCfg
	m = pickKey(m, "b")
	if m.state != viewStateRunTargetPicker {
		t.Fatalf("b must do nothing on the picker, state = %v", m.state)
	}
	if m.runTargetSelected != 1 {
		t.Fatalf("b changed the selection: %d", m.runTargetSelected)
	}
	if m.sessionCfg.RemoteHost != before.RemoteHost {
		t.Fatalf("b changed the session config")
	}
	view := strings.ToLower(m.renderRunTargetPicker())
	for _, banned := range []string{"backend", "tmux", "b: use"} {
		if strings.Contains(view, banned) {
			t.Errorf("run-target picker still mentions %q:\n%s", banned, view)
		}
	}
}

func TestSummaryHasNoBackendLine(t *testing.T) {
	m := NewSummaryModel("PB-1", "feature", domain.Repo{Name: "org/repo"}, "")
	if strings.Contains(m.viewString(), "Backend:") {
		t.Fatalf("summary still shows a Backend line:\n%s", m.viewString())
	}
}

func TestAttachLegacyRowShowsMoveHint(t *testing.T) {
	s := legacyRow()
	m := newTestModelWithSession(t, s)

	newModel, _, handled := m.handleSessionActionKey(pressKey("a"))
	if !handled {
		t.Fatal("expected a to be handled")
	}
	got := newModel.(*Model)
	if got.state == viewStateLoading {
		t.Fatal("attach to a legacy row must not start connecting")
	}
	if !strings.Contains(got.snapshotToast, legacyStopCommand(s)) {
		t.Fatalf("toast %q does not carry the stop command %q", got.snapshotToast, legacyStopCommand(s))
	}
	if !strings.Contains(got.snapshotToast, "devbox") {
		t.Fatalf("toast %q does not name the host", got.snapshotToast)
	}
}

func TestRestartLegacyRowConfirmsStopFirst(t *testing.T) {
	s := legacyRow()
	m := newTestModelWithSession(t, s)

	newModel, _, handled := m.handleSessionManageKey(pressKey("s"))
	if !handled {
		t.Fatal("expected s to be handled")
	}
	m = newModel.(*Model)
	if m.state != viewStateRestartConfirm {
		t.Fatalf("legacy restart must confirm first, state = %v", m.state)
	}
	if _, started := m.creatingSessions[s.ID]; started {
		t.Fatal("restart started before the user confirmed the tmux session is stopped")
	}
	view := m.renderRestartConfirm()
	for _, want := range []string{legacyStopCommand(s), "devbox"} {
		if !strings.Contains(view, want) {
			t.Errorf("restart confirm lacks %q:\n%s", want, view)
		}
	}

	updated, cmd := m.handleRestartConfirmUpdate(pressKey("y"))
	m = updated.(*Model)
	if cmd == nil {
		t.Fatal("confirming must start the restart")
	}
	if m.legacyStoppedID != s.ID {
		t.Fatalf("legacyStoppedID = %q, want %q", m.legacyStoppedID, s.ID)
	}
	if _, started := m.creatingSessions[s.ID]; !started {
		t.Fatal("confirming must start a background restart")
	}
}

func TestTerminateLegacyShowsStopCommand(t *testing.T) {
	s := legacyRow()
	m := newTestModelWithSession(t, s)

	newModel, _, _ := m.handleSessionManageKey(pressKey("ctrl+k"))
	m = newModel.(*Model)
	if m.state != viewStateTerminateConfirm {
		t.Fatalf("state = %v, want terminate confirm", m.state)
	}
	view := m.renderTerminateConfirm()
	for _, want := range []string{legacyStopCommand(s), "devbox"} {
		if !strings.Contains(view, want) {
			t.Errorf("terminate confirm lacks %q:\n%s", want, view)
		}
	}

	// aiman stops nothing in tmux: the terminal step is a skip.
	err := m.terminateStopTerminal(context.Background(), s)
	var skip skipReason
	if !errors.As(err, &skip) {
		t.Fatalf("terminal step on a legacy row: err = %v, want a skip", err)
	}
}

func TestApplyDiscoveryKeepsLegacyRowInactiveOnScannedHost(t *testing.T) {
	legacy := legacyRow()
	legacy.RemoteHost = "regent0"
	legacy.Status = domain.SessionStatusActive
	repo := &savingSessionRepo{startupSessionRepo: startupSessionRepo{sessions: []domain.Session{legacy}}}
	m := NewModel(testCfg(), nil, []domain.Session{legacy}, repo, nil, nil, nil)

	m.applyDiscoveryResult(discoveryResultMsg{scannedHosts: map[string]bool{"regent0": true}})

	var found *domain.Session
	for i := range m.allSessions {
		if m.allSessions[i].ID == legacy.ID {
			found = &m.allSessions[i]
		}
	}
	if found == nil {
		t.Fatalf("legacy row dropped from a scanned host: %v", m.allSessions)
	}
	if found.Status != domain.SessionStatusInactive {
		t.Fatalf("legacy row status = %q, want inactive", found.Status)
	}
	for _, id := range repo.saved {
		if id == legacy.ID {
			t.Fatal("the in-memory inactive status of a legacy row must not be saved")
		}
	}
}

func TestApplyDiscoveryStillDropsDeadPTYRowOnScannedHost(t *testing.T) {
	for _, backend := range []string{domain.BackendPTY, ""} {
		dead := domain.Session{ID: "dead-1", RemoteHost: "regent0", TerminalName: "PB-9",
			Status: domain.SessionStatusActive, Backend: backend}
		repo := &savingSessionRepo{startupSessionRepo: startupSessionRepo{sessions: []domain.Session{dead}}}
		m := NewModel(testCfg(), nil, []domain.Session{dead}, repo, nil, nil, nil)

		m.applyDiscoveryResult(discoveryResultMsg{scannedHosts: map[string]bool{"regent0": true}})

		for _, s := range m.allSessions {
			if s.ID == dead.ID {
				t.Fatalf("backend %q: dead row kept on a scanned host", backend)
			}
		}
	}
}

func TestItemDescriptionMarksLegacyTmux(t *testing.T) {
	legacy := item{session: legacyRow()}
	if d := legacy.Description(); !strings.Contains(d, "| tmux (legacy)") {
		t.Fatalf("legacy description %q lacks the legacy badge", d)
	}
	pty := legacyRow()
	pty.Backend = domain.BackendPTY
	if d := (item{session: pty}).Description(); strings.Contains(d, "tmux") || strings.Contains(d, "| pty") {
		t.Fatalf("pty description %q carries a backend badge", d)
	}
}

func TestStartupToastCountsLegacySessions(t *testing.T) {
	a, b := legacyRow(), legacyRow()
	b.ID, b.TerminalName = "legacy-2", "PB-2"
	pty := legacyRow()
	pty.ID, pty.Backend = "pty-1", domain.BackendPTY
	cfg := &config.Config{Remotes: []config.Remote{{Host: "devbox"}}}
	m := NewModel(cfg, nil, []domain.Session{a, b, pty}, &mockSessionRepo{}, nil, nil, nil)
	_ = m.Init()
	if !strings.Contains(m.snapshotToast, "2 sessions still use tmux") {
		t.Fatalf("startup toast = %q, want the legacy count", m.snapshotToast)
	}

	clean := NewModel(cfg, nil, []domain.Session{pty}, &mockSessionRepo{}, nil, nil, nil)
	_ = clean.Init()
	if strings.Contains(clean.snapshotToast, "tmux") {
		t.Fatalf("no legacy rows, yet toast = %q", clean.snapshotToast)
	}
}

func TestStartupToastShowsConfigWarnings(t *testing.T) {
	cfg := &config.Config{Remotes: []config.Remote{{Host: "devbox"}}, Warnings: []string{"remotes[0].session_backend is ignored"}}
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	_ = m.Init()
	if !strings.Contains(m.snapshotToast, "session_backend is ignored") {
		t.Fatalf("startup toast = %q, want the config warning", m.snapshotToast)
	}
}
