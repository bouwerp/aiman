package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

func TestVerifyKeyStartsReview(t *testing.T) {
	m, _ := promptModel(domain.Session{ID: "w1", Name: "impl", RemoteHost: "regent0"})
	_, cmd, handled := m.handleSessionManageKey(pressRune('V'))
	if !handled || cmd == nil {
		t.Fatalf("handled=%v cmd=%v", handled, cmd)
	}
	m.applyVerifyStarted(verifyStartedMsg{sessionID: "w1", status: "starting"})
	if !strings.Contains(m.snapshotToast, "Review starting") {
		t.Fatalf("toast = %q", m.snapshotToast)
	}
}

func TestVerifyKeyIgnoredOnDaemonsTab(t *testing.T) {
	m, _ := promptModel(domain.Session{ID: "w1", Name: "impl", RemoteHost: "regent0"})
	m.currentTab = tabDaemons
	if _, _, handled := m.handleSessionManageKey(pressRune('V')); handled {
		t.Fatal("V was handled on the daemons tab")
	}
}

func TestVerifyKeyBlockedWhileCreating(t *testing.T) {
	m, _ := promptModel(domain.Session{ID: "w1", Name: "impl", RemoteHost: "regent0"})
	m.creatingSessions["w1"] = &creatingSession{placeholder: domain.Session{ID: "w1"}}
	next, _ := m.Update(pressRune('V'))
	m = next.(*Model)
	if !strings.Contains(m.snapshotToast, "still being created") {
		t.Fatalf("toast = %q", m.snapshotToast)
	}
}

func TestReviewerRunning(t *testing.T) {
	worker := domain.Session{ID: "w1"}
	live := domain.Session{ID: "r1", Role: domain.SessionRoleReviewer, ParentID: "w1"}
	ended := live
	ended.ID, ended.AgentEnded = "r2", true
	other := live
	other.ID, other.ParentID = "r3", "w2"
	cases := []struct {
		name string
		all  []domain.Session
		want bool
	}{
		{"live reviewer child", []domain.Session{worker, live}, true},
		{"ended reviewer", []domain.Session{worker, ended}, false},
		{"other parent", []domain.Session{worker, other}, false},
		{"no child", []domain.Session{worker}, false},
	}
	for _, c := range cases {
		if got := reviewerRunning("w1", c.all); got != c.want {
			t.Fatalf("%s: %v", c.name, got)
		}
	}
}

func TestMetaLineShowsReviewing(t *testing.T) {
	worker := domain.Session{ID: "w1", Name: "impl", RemoteHost: "regent0"}
	reviewer := domain.Session{ID: "r1", Role: domain.SessionRoleReviewer, ParentID: "w1", RemoteHost: "regent0"}
	m, _ := promptModel(worker, reviewer)
	if got := m.renderSessionDetail(m.makeItem(worker), 120); !strings.Contains(got, "reviewing…") {
		t.Fatalf("detail = %q", got)
	}
}

func TestVerifyEventDropsReviewerRows(t *testing.T) {
	worker := domain.Session{ID: "w1", Name: "impl", RemoteHost: "regent0"}
	reviewer := domain.Session{ID: "r1", Role: domain.SessionRoleReviewer, ParentID: "w1", RemoteHost: "regent0"}
	m, _ := promptModel(worker, reviewer)
	m.daemonList = list.New(nil, list.NewDefaultDelegate(), 40, 20)
	m.streams["regent0"] = &sessionStream{host: "regent0", events: make(chan server.SessionEvent, 1)}
	m.applySessionEvent(sessionEventMsg{host: "regent0", event: server.SessionEvent{Type: "verify", ID: "w1"}})
	for _, s := range m.allSessions {
		if s.ID == "r1" {
			t.Fatal("reviewer row was not dropped")
		}
	}
}

func TestApplyRemoteFilterHidesReviewers(t *testing.T) {
	worker := domain.Session{ID: "w1", Name: "impl", RemoteHost: "regent0"}
	reviewer := domain.Session{ID: "r1", Role: domain.SessionRoleReviewer, ParentID: "w1", RemoteHost: "regent0"}
	m, _ := promptModel(worker, reviewer)
	m.daemonList = list.New(nil, list.NewDefaultDelegate(), 40, 20)
	m.applyRemoteFilter()
	for _, it := range m.list.Items() {
		if row, ok := it.(item); ok && row.session.ID == "r1" {
			t.Fatal("reviewer row is visible")
		}
	}
}
