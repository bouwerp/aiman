package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Two review children of one parent share the terminal name the older
// minute-granular quick-session names produced. They are still two sessions.
const sharedTerminal = "adhoc-20261022-1130"

func siblingSessions() []domain.Session {
	at := time.Date(2026, 10, 22, 11, 30, 0, 0, time.UTC)
	return []domain.Session{
		{ID: "parent", Name: "nl", TmuxSession: "nl", RemoteHost: "10.0.1.5", Backend: domain.BackendPTY,
			Status: domain.SessionStatusActive, CreatedAt: at},
		{ID: "child-grok", Name: "nl-review-grok", ParentID: "parent", TmuxSession: sharedTerminal,
			RemoteHost: "10.0.1.5", Backend: domain.BackendPTY, Status: domain.SessionStatusActive,
			WorkingDirectory: "/repos/app@nl", CreatedAt: at.Add(time.Minute)},
		{ID: "child-muse", Name: "nl-review-muse", ParentID: "parent", TmuxSession: sharedTerminal,
			RemoteHost: "10.0.1.5", Backend: domain.BackendPTY, Status: domain.SessionStatusActive,
			WorkingDirectory: "/repos/app@nl", CreatedAt: at.Add(2 * time.Minute)},
	}
}

func siblingModel(t *testing.T) *Model {
	t.Helper()
	cfg := twoRemoteCfg()
	cfg.Features.InputPromptDetection = true
	sessions := siblingSessions()
	repo := &savingSessionRepo{startupSessionRepo: startupSessionRepo{sessions: append([]domain.Session(nil), sessions...)}}
	m := NewModel(cfg, nil, append([]domain.Session(nil), sessions...), repo, nil, nil, nil)
	m.list.SetSize(80, 40)
	m.state = viewStateMain
	m.initialLoad = false
	m.applyRemoteFilter()
	return m
}

func selectMuse(t *testing.T, m *Model) {
	const id = "child-muse"
	t.Helper()
	for i, it := range m.list.Items() {
		if si, ok := it.(item); ok && !si.header && si.session.ID == id {
			m.list.Select(i)
			_, _ = m.applyTmuxTick(tmuxTickMsg(time.Now()), nil)
			return
		}
	}
	t.Fatalf("no row for %s", id)
}

func selectedID(m *Model) string {
	it, ok := m.selectedSessionItem()
	if !ok {
		return ""
	}
	return it.session.ID
}

func discoverAll(m *Model, sessions []domain.Session) {
	m.applyDiscoveryResult(discoveryResultMsg{
		scannedHosts: map[string]bool{"10.0.1.5": true},
		sessions:     sessions,
	})
}

// Discovery merges by terminal name. Two children sharing one dropped the
// second, so the selected child vanished and the selection fell back.
func TestDiscoveryKeepsChildrenThatShareATerminalName(t *testing.T) {
	m := siblingModel(t)
	selectMuse(t, m)

	discoverAll(m, siblingSessions())

	seen := map[string]bool{}
	for _, s := range m.allSessions {
		seen[s.ID] = true
	}
	if !seen["child-grok"] || !seen["child-muse"] {
		t.Fatalf("sibling dropped by the merge: %+v", m.allSessions)
	}
	if got := selectedID(m); got != "child-muse" {
		t.Fatalf("selected = %q, want child-muse", got)
	}
}

// A sibling's capture carries the same terminal name. Matching on the name put
// the sibling's screen in the selected child's preview.
func TestSiblingCaptureDoesNotReplaceTheSelectedPreview(t *testing.T) {
	m := siblingModel(t)
	selectMuse(t, m)
	_ = m.applyPreviewRefresh(previewRefreshMsg{
		session: sharedTerminal, id: "child-muse", text: "muse screen",
		cache: usecase.PaneCache{Text: "muse screen", Hash: "m"},
	})

	_ = m.applyPreviewRefresh(previewRefreshMsg{
		session: sharedTerminal, id: "child-grok", text: "grok screen",
		cache: usecase.PaneCache{Text: "grok screen", Hash: "g"},
	})

	if m.tmuxOutput != "muse screen" {
		t.Fatalf("preview = %q, want the selected child's screen", m.tmuxOutput)
	}
}

// Activity for one sibling marked the first row with that terminal name.
func TestSiblingActivityLandsOnItsOwnRow(t *testing.T) {
	m := siblingModel(t)
	m.applyInputHint(inputHintMsg{session: sharedTerminal, id: "child-muse", needsInput: true, activity: "idle"}, nil)

	for _, it := range m.list.Items() {
		si, ok := it.(item)
		if !ok || si.header {
			continue
		}
		switch si.session.ID {
		case "child-muse":
			if !si.needsInput {
				t.Errorf("child-muse lost its own activity")
			}
		case "child-grok":
			if si.needsInput {
				t.Errorf("child-grok took its sibling's activity")
			}
		}
	}
}

// One scan that misses the selected child must not move the selection.
func TestSelectedChildSurvivesATransientMiss(t *testing.T) {
	m := siblingModel(t)
	all := siblingSessions()
	discoverAll(m, all)
	selectMuse(t, m)

	discoverAll(m, all[:2])
	if got := selectedID(m); got != "child-muse" {
		t.Fatalf("after a missed scan selected = %q, want child-muse", got)
	}
	discoverAll(m, all)
	if got := selectedID(m); got != "child-muse" {
		t.Fatalf("after it reappeared selected = %q, want child-muse", got)
	}
}

// A session gone for good still loses its row after the grace scans.
func TestSelectedChildGoneForGoodIsDropped(t *testing.T) {
	m := siblingModel(t)
	all := siblingSessions()
	discoverAll(m, all)
	selectMuse(t, m)

	for range selectedMissGrace + 1 {
		discoverAll(m, all[:2])
	}
	for _, s := range m.allSessions {
		if s.ID == "child-muse" {
			t.Fatalf("a session missing from %d scans is still listed", selectedMissGrace+1)
		}
	}
}

// A rebuild that hides the selected child under a collapsed parent used to
// land on the group header, which previews nothing.
func TestSelectedChildUnderCollapsedParentStaysSelected(t *testing.T) {
	m := siblingModel(t)
	for i := range m.allSessions {
		if m.allSessions[i].ID == "child-muse" {
			m.allSessions[i].ParentID = ""
		}
	}
	m.applyRemoteFilter()
	selectMuse(t, m)
	m.collapsedSessions["parent"] = true

	for i := range m.allSessions {
		if m.allSessions[i].ID == "child-muse" {
			m.allSessions[i].ParentID = "parent"
		}
	}
	m.applyRemoteFilter()

	if got := selectedID(m); got != "child-muse" {
		t.Fatalf("selected = %q, want child-muse", got)
	}
}
