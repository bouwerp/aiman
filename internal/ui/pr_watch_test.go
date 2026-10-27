package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// fakeCIClient records ci.watch calls and answers with status.
type fakeCIClient struct {
	status  usecase.CIStatus
	err     error
	gets    []string
	watches []bool
}

func (f *fakeCIClient) Get(_ context.Context, _ config.Remote, id string) (usecase.CIStatus, error) {
	f.gets = append(f.gets, id)
	return f.status, f.err
}

func (f *fakeCIClient) SetWatch(_ context.Context, _ config.Remote, _ string, on bool) (usecase.CIStatus, error) {
	f.watches = append(f.watches, on)
	st := f.status
	st.Watch = on
	return st, f.err
}

func TestWatchingPRNumber(t *testing.T) {
	cases := []struct {
		st   usecase.CIStatus
		ok   bool
		want int
	}{
		{usecase.CIStatus{Watch: true, PRNumber: 12}, true, 12},
		{usecase.CIStatus{Watch: true}, true, 0},
		{usecase.CIStatus{PRNumber: 12}, true, 0},
		{usecase.CIStatus{Watch: true, PRNumber: 12}, false, 0},
	}
	for _, c := range cases {
		if got := watchingPRNumber(c.st, c.ok); got != c.want {
			t.Errorf("watchingPRNumber(%+v, %v) = %d, want %d", c.st, c.ok, got, c.want)
		}
	}

	m, _ := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	m.applyCIStatus(ciStatusMsg{id: "s1", status: usecase.CIStatus{Watch: true, PRNumber: 12}})
	if plain := ansi.Strip(m.renderMainView()); !strings.Contains(plain, "watching PR #12") {
		t.Fatalf("meta line missing watching PR #12:\n%s", plain)
	}
}

func TestCINeedsUser(t *testing.T) {
	if !ciNeedsUser(usecase.CIStatus{Escalated: []string{"lint"}}, true) {
		t.Error("escalated check does not need the user")
	}
	if ciNeedsUser(usecase.CIStatus{}, true) {
		t.Error("no escalation needs the user")
	}
	if ciNeedsUser(usecase.CIStatus{Escalated: []string{"lint"}}, false) {
		t.Error("missing status needs the user")
	}

	m, _ := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	m.applyCIStatus(ciStatusMsg{id: "s1", status: usecase.CIStatus{Watch: true, Escalated: []string{"lint"}}})
	if it, ok := m.selectedSessionItem(); !ok || !it.escalated {
		t.Fatalf("row not escalated: %+v", it)
	}
}

func TestNextPRWatch(t *testing.T) {
	auto := domain.Session{Mode: domain.SessionModeAutonomous}
	interactive := domain.Session{}
	cases := []struct {
		name string
		s    domain.Session
		st   usecase.CIStatus
		ok   bool
		want bool
	}{
		{"autonomous, no cache", auto, usecase.CIStatus{}, false, false},
		{"interactive, no cache", interactive, usecase.CIStatus{}, false, true},
		{"cache off on autonomous", auto, usecase.CIStatus{Watch: false}, true, true},
		{"cache on", interactive, usecase.CIStatus{Watch: true}, true, false},
	}
	for _, c := range cases {
		if got := nextPRWatch(c.s, c.st, c.ok); got != c.want {
			t.Errorf("%s: nextPRWatch = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWKeyReturnsToggleCmd(t *testing.T) {
	m, _ := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	fake := &fakeCIClient{status: usecase.CIStatus{PRNumber: 12}}
	m.ciClient = fake

	next, cmd := m.Update(pressRune('w'))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("w returned no command")
	}
	msg, ok := cmd().(ciStatusMsg)
	if !ok {
		t.Fatalf("w command returned %T", msg)
	}
	if len(fake.watches) != 1 || !fake.watches[0] {
		t.Fatalf("ci.watch calls = %v, want [true]", fake.watches)
	}
	next, _ = m.Update(msg)
	m = next.(*Model)
	if m.snapshotToast != "Watching PR for impl" {
		t.Fatalf("toast = %q", m.snapshotToast)
	}
	if got := m.metaFor("s1").WatchingPR; got != 12 {
		t.Fatalf("WatchingPR = %d", got)
	}
}

func TestWKeyBlockedWhileCreating(t *testing.T) {
	sess := domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"}
	m, _ := promptModel(sess)
	m.ciClient = &fakeCIClient{}
	m.creatingSessions["s1"] = &creatingSession{placeholder: sess}

	next, _ := m.Update(pressRune('w'))
	m = next.(*Model)
	if !strings.Contains(m.snapshotToast, "still being created") {
		t.Fatalf("toast = %q", m.snapshotToast)
	}
}

// A ci_escalation notification reads ci.get for its session, selected or
// not, and still queues no preview. Other kinds do not read it.
func TestCIEscalationNotifyFetchesStatus(t *testing.T) {
	m := eventModel(domain.Session{ID: "s1", IssueKey: "PROJ-1", RemoteHost: "regent0"})
	m.ciClient = &fakeCIClient{}
	m.ensureSessionStreams()
	m.applySessionEvent(sessionEventMsg{host: "regent0", event: server.SessionEvent{Type: "notify", ID: "s1", Kind: "ci_escalation"}})
	if !m.ciInFlight["s1"] {
		t.Fatal("ci_escalation did not start a ci.get")
	}
	if !m.eventSeen["s1"].lastRefresh.IsZero() {
		t.Fatal("a notify event queued a preview refresh")
	}

	delete(m.ciInFlight, "s1")
	m.applySessionEvent(sessionEventMsg{host: "regent0", event: server.SessionEvent{Type: "notify", ID: "s1", Kind: "error"}})
	if m.ciInFlight["s1"] {
		t.Fatal("a non-CI notification started a ci.get")
	}
}
