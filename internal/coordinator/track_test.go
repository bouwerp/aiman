package coordinator

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type mutation struct {
	session string
	author  domain.TodoAuthor
}

type fakeTodos struct {
	mu        sync.Mutex
	lists     map[string]domain.TodoList
	mutations []mutation
}

func (f *fakeTodos) Get(id string) (domain.TodoList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.lists[id]
	l.Items = append([]domain.TodoItem(nil), l.Items...)
	return l, nil
}

func (f *fakeTodos) Mutate(_ context.Context, id string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.lists[id]
	l.Items = append([]domain.TodoItem(nil), l.Items...)
	if err := fn(&l); err != nil {
		return err
	}
	f.lists[id] = l
	f.mutations = append(f.mutations, mutation{session: id, author: author})
	return nil
}

// t3 returns the parent p's item t3.
func (f *fakeTodos) t3(t *testing.T) domain.TodoItem {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.lists["p"].Items {
		if it.ID == "t3" {
			return it
		}
	}
	t.Fatal("item p/t3 not found")
	return domain.TodoItem{}
}

type emitted struct {
	session string
	kind    domain.NotifyKind
	detail  string
}

type fakeNotify struct {
	mu     sync.Mutex
	events []emitted
}

func (f *fakeNotify) Emit(_ context.Context, sessionID, _ string, kind domain.NotifyKind, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, emitted{session: sessionID, kind: kind, detail: detail})
	return nil
}

type steerNote struct {
	session string
	text    string
}

// fixture is a parent "p" on branch feat/x whose item t3 links to child c1.
// c1's report cites a file and a test command unless a test replaces it.
type fixture struct {
	todos    *fakeTodos
	notify   *fakeNotify
	probed   []string
	steered  []steerNote
	report   func() (string, error)
	sessions []domain.Session
	kept     map[string]string
	tracker  *Tracker
}

const evidenceReport = "## What changed\n\nParser.\n\n## Evidence\n\n- internal/parse/list.go\n- go test ./... exit 0\n\n## Surprises\n\nnone\n"

func newFixture(ref string, pr PRState) *fixture {
	f := &fixture{
		todos: &fakeTodos{lists: map[string]domain.TodoList{
			"p": {SessionID: "p", Items: []domain.TodoItem{
				{ID: "t3", Text: "Add the parser", State: domain.TodoPending, Ref: ref},
			}},
		}},
		notify: &fakeNotify{},
		report: func() (string, error) { return evidenceReport, nil },
	}
	sessions := []domain.Session{
		{ID: "p", Name: "parent", Branch: "feat/x"},
		{ID: "c1", Name: "child", Branch: "feat/x--t3", BaseBranch: "feat/x", ParentID: "p"},
	}
	f.tracker = &Tracker{
		Todos:    f.todos,
		Sessions: func() ([]domain.Session, error) { return sessions, nil },
		PR: func(_ context.Context, child domain.Session) (PRState, error) {
			f.probed = append(f.probed, child.ID)
			return pr, nil
		},
		Report: func(_ context.Context, child domain.Session) (string, error) {
			if child.ID != "c1" {
				return "", errors.New("unexpected child " + child.ID)
			}
			return f.report()
		},
		Steer:  func(id, text string) { f.steered = append(f.steered, steerNote{session: id, text: text}) },
		Notify: f.notify,
	}
	return f
}

func TestTrackMarksMergedChild(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
	if len(f.todos.mutations) != 1 || f.todos.mutations[0].author != domain.AuthorAiman {
		t.Fatalf("mutations = %+v, want one by aiman", f.todos.mutations)
	}

	f.tracker.TrackOnce(context.Background())
	if len(f.todos.mutations) != 1 {
		t.Fatalf("a done item was written again: %+v", f.todos.mutations)
	}
}

func TestTrackIgnoresOtherBase(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "main", Merged: true})
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
		t.Fatalf("t3 = %+v, want pending and still linked", it)
	}
	if len(f.todos.mutations) != 0 {
		t.Fatalf("mutations = %+v, want none", f.todos.mutations)
	}
}

func TestTrackOpenPRWhenBeforePR(t *testing.T) {
	open := PRState{Base: "feat/x", Open: true}
	cases := []struct {
		name     string
		beforePR bool
		passed   bool
		want     domain.TodoState
	}{
		{"before_pr and passed", true, true, domain.TodoDone},
		{"before_pr and not passed", true, false, domain.TodoPending},
		{"no before_pr", false, true, domain.TodoPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture("child:c1", open)
			var passedFor, beforeFor string
			f.tracker.BeforePR = func(id string) bool { beforeFor = id; return c.beforePR }
			f.tracker.Passed = func(id string) bool { passedFor = id; return c.passed }
			f.tracker.TrackOnce(context.Background())

			if it := f.todos.t3(t); it.State != c.want {
				t.Fatalf("t3 state = %s, want %s", it.State, c.want)
			}
			if beforeFor != "p" {
				t.Fatalf("BeforePR asked for %q, want the parent p", beforeFor)
			}
			if c.beforePR && passedFor != "c1" {
				t.Fatalf("Passed asked for %q, want the child c1", passedFor)
			}
		})
	}
}

func TestTrackOpenPRNilHooksStayOpen(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Open: true})
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoPending {
		t.Fatalf("t3 state = %s, want pending", it.State)
	}
}

func TestTrackResumesFromRefs(t *testing.T) {
	first := newFixture("child:c1", PRState{Base: "feat/x", Open: true})
	first.tracker.TrackOnce(context.Background())

	// A restarted serve builds a fresh Tracker over the same stored lists.
	restarted := &Tracker{
		Todos:    first.todos,
		Sessions: first.tracker.Sessions,
		PR: func(context.Context, domain.Session) (PRState, error) {
			return PRState{Base: "feat/x", Merged: true}, nil
		},
		Report: first.tracker.Report,
	}
	restarted.TrackOnce(context.Background())

	if it := first.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
}

func TestTrackClearsRefOfMissingChild(t *testing.T) {
	f := newFixture("child:gone-42", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())

	it := f.todos.t3(t)
	if it.Ref != "" || it.State != domain.TodoPending {
		t.Fatalf("t3 = %+v, want pending with no ref", it)
	}
	if len(f.probed) != 0 {
		t.Fatalf("probed %v, want no PR read", f.probed)
	}
	if len(f.notify.events) != 1 {
		t.Fatalf("events = %+v, want one", f.notify.events)
	}
	ev := f.notify.events[0]
	if ev.kind != domain.NotifyError || ev.session != "p" || !strings.Contains(ev.detail, "gone-42") {
		t.Fatalf("event = %+v, want NotifyError on p naming gone-42", ev)
	}

	f.tracker.TrackOnce(context.Background())
	if len(f.notify.events) != 1 {
		t.Fatalf("an unlinked item notified again: %+v", f.notify.events)
	}
}

func TestTrackIgnoresOtherRefs(t *testing.T) {
	f := newFixture("check:lint", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())

	if len(f.probed) != 0 {
		t.Fatalf("probed %v, want none", f.probed)
	}
	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "check:lint" {
		t.Fatalf("t3 = %+v, want unchanged", it)
	}
}

func TestTrackSkipsChildOnPRError(t *testing.T) {
	f := newFixture("child:c1", PRState{})
	f.tracker.PR = func(context.Context, domain.Session) (PRState, error) {
		return PRState{}, errors.New("gh: boom")
	}
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
		t.Fatalf("t3 = %+v, want unchanged", it)
	}
	if len(f.notify.events) != 0 {
		t.Fatalf("events = %+v, want none", f.notify.events)
	}
}

func TestTrackSessionsErrorUnlinksNothing(t *testing.T) {
	f := newFixture("child:c1", PRState{})
	f.tracker.Sessions = func() ([]domain.Session, error) { return nil, errors.New("db down") }
	f.tracker.TrackOnce(context.Background())

	if len(f.todos.mutations) != 0 || len(f.notify.events) != 0 {
		t.Fatalf("mutations %+v events %+v, want none", f.todos.mutations, f.notify.events)
	}
}

func TestParsePRState(t *testing.T) {
	cases := []struct {
		raw  string
		want PRState
	}{
		{`{"state":"MERGED","mergedAt":"2026-10-20T10:00:00Z","baseRefName":"feat/x"}`, PRState{Base: "feat/x", Merged: true}},
		{`{"state":"OPEN","mergedAt":null,"baseRefName":"feat/x"}`, PRState{Base: "feat/x", Open: true}},
		{`{"state":"CLOSED","mergedAt":null,"baseRefName":"main"}`, PRState{Base: "main"}},
	}
	for _, c := range cases {
		got, err := ParsePRState([]byte(c.raw))
		if err != nil {
			t.Fatalf("ParsePRState(%s): %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("ParsePRState(%s) = %+v, want %+v", c.raw, got, c.want)
		}
	}
	if _, err := ParsePRState([]byte("not json")); err == nil {
		t.Fatal("ParsePRState(not json) = nil error")
	}
}

func TestChildWithoutReportKeepsParentItemOpen(t *testing.T) {
	for name, report := range map[string]func() (string, error){
		"missing":       func() (string, error) { return "", errors.New("no such file") },
		"evidence-free": func() (string, error) { return "## What changed\n\nIt works now.\n", nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
			f.report = report
			f.tracker.TrackOnce(context.Background())
			f.tracker.TrackOnce(context.Background())

			if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
				t.Fatalf("t3 = %+v, want pending and still linked", it)
			}
			if len(f.todos.mutations) != 0 {
				t.Fatalf("mutations = %+v, want none", f.todos.mutations)
			}
			if len(f.steered) != 1 {
				t.Fatalf("steered %+v, want one note", f.steered)
			}
			n := f.steered[0]
			want := "[aiman] child child returned no evidence; ask it for .aiman_report.md before marking t3 done."
			if n.session != "p" || n.text != want {
				t.Fatalf("note = %+v, want %q to p", n, want)
			}
		})
	}
}

func TestChildWithReportMarksDone(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
	if len(f.todos.mutations) != 1 || f.todos.mutations[0].author != domain.AuthorAiman {
		t.Fatalf("mutations = %+v, want one by aiman", f.todos.mutations)
	}
	if len(f.steered) != 0 {
		t.Fatalf("steered %+v, want none", f.steered)
	}
}

func TestReadReportFromWorkingDirectory(t *testing.T) {
	x := &fakeExec{out: "noise\n" + reportMarker + "\n" + evidenceReport}
	read := ExecReport(x)
	got, err := read(context.Background(), domain.Session{WorkingDirectory: "/w/child", WorktreePath: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	if got != evidenceReport {
		t.Fatalf("report = %q", got)
	}
	if !strings.Contains(x.cmd, "/w/child/.aiman_report.md") {
		t.Fatalf("cmd %q must read the working directory's report", x.cmd)
	}
	if _, err := read(context.Background(), domain.Session{}); err == nil {
		t.Fatal("a session with no directory must be no evidence")
	}
}

type fakeExec struct {
	out string
	cmd string
}

func (f *fakeExec) Execute(_ context.Context, cmd string) (string, error) {
	f.cmd = cmd
	return f.out, nil
}

type ledgerRow struct {
	session string
	kind    domain.EventKind
	payload map[string]any
}

// consultNow is the fixture clock; the consult was created 5 minutes earlier.
var consultNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// consultFixture turns the fixture's child c1 into a consult with no PR.
// Kept reports land in kept, keyed by child ID.
func consultFixture(report string) (*fixture, *[]ledgerRow, *[]string) {
	f := newFixture("child:c1", PRState{})
	f.report = func() (string, error) { return report, nil }
	f.sessions = []domain.Session{
		{ID: "p", Name: "parent", Branch: "feat/x"},
		{ID: "c1", Name: "consult", ParentID: "p", Role: domain.SessionRoleConsult, CreatedAt: consultNow.Add(-5 * time.Minute)},
	}
	f.tracker.Sessions = func() ([]domain.Session, error) { return f.sessions, nil }
	f.tracker.Now = func() time.Time { return consultNow }
	f.tracker.ConsultTimeout = 10 * time.Minute
	f.kept = map[string]string{}
	f.tracker.KeepReport = func(_ context.Context, child domain.Session, md string) (string, error) {
		f.kept[child.ID] = md
		return "/home/u/.aiman/consults/" + child.ID + ".report.md", nil
	}
	rows := &[]ledgerRow{}
	retired := &[]string{}
	f.tracker.Ledger = func(_ context.Context, id string, kind domain.EventKind, payload map[string]any) {
		*rows = append(*rows, ledgerRow{session: id, kind: kind, payload: payload})
	}
	// Like RetireConsult, retiring forgets the consult's row.
	f.tracker.Retire = func(_ context.Context, child domain.Session) {
		*retired = append(*retired, child.ID)
		f.sessions = slices.DeleteFunc(f.sessions, func(s domain.Session) bool { return s.ID == child.ID })
	}
	return f, rows, retired
}

const rejectReport = "## Objections\n\n- [high] a\n- [medium] b\n- [low] c\n- [low] d\n\n## Verdict\n\nreject\n"

func TestConsultSettlesOnReport(t *testing.T) {
	f, rows, retired := consultFixture(rejectReport)
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
	if len(f.probed) != 0 {
		t.Fatalf("the tracker asked for a consult's PR: %v", f.probed)
	}
	if len(*rows) != 1 || (*rows)[0].session != "p" || (*rows)[0].kind != domain.EventConsultFinished {
		t.Fatalf("ledger = %+v, want one consult.finished on p", *rows)
	}
	want := map[string]any{"child": "c1", "todo": "t3", "high": 1, "medium": 1, "low": 2, "verdict": "reject"}
	for k, v := range want {
		if (*rows)[0].payload[k] != v {
			t.Fatalf("payload[%s] = %v, want %v", k, (*rows)[0].payload[k], v)
		}
	}
	if len(*retired) != 1 || (*retired)[0] != "c1" {
		t.Fatalf("retired = %v, want c1", *retired)
	}
	if f.kept["c1"] != rejectReport {
		t.Fatalf("kept report = %q, want the consult's report", f.kept["c1"])
	}
	note := "[aiman] consult c1 for t3: verdict reject, 1 high, 1 medium, 2 low. Report: /home/u/.aiman/consults/c1.report.md. Triage every objection in the design's Decisions table."
	if len(f.steered) != 1 || f.steered[0].session != "p" || f.steered[0].text != note {
		t.Fatalf("steered %+v, want one note %q", f.steered, note)
	}
}

func TestConsultKeepReportFailureWaits(t *testing.T) {
	f, rows, retired := consultFixture(rejectReport)
	f.tracker.KeepReport = func(context.Context, domain.Session, string) (string, error) {
		return "", errors.New("disk full")
	}
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
		t.Fatalf("t3 = %+v, want pending and linked", it)
	}
	if len(*rows)+len(*retired)+len(f.steered) != 0 {
		t.Fatalf("rows %v retired %v steered %v", *rows, *retired, f.steered)
	}
}

func TestConsultWaitsForReport(t *testing.T) {
	f, rows, retired := consultFixture("")
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
		t.Fatalf("t3 = %+v, want pending and linked", it)
	}
	if len(*rows)+len(*retired)+len(f.steered) != 0 {
		t.Fatalf("a missing report did something: rows %v retired %v steered %v", *rows, *retired, f.steered)
	}
}

func TestConsultUnparsableReportAsksOnce(t *testing.T) {
	f, rows, retired := consultFixture("I looked; it seems fine.\n")
	f.tracker.TrackOnce(context.Background())
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoPending {
		t.Fatalf("t3 state = %s, want pending", it.State)
	}
	if len(f.steered) != 1 || f.steered[0].session != "p" {
		t.Fatalf("steered = %+v, want one note to p", f.steered)
	}
	text := f.steered[0].text
	for _, want := range []string{"consult c1 for t3", "## Objections", "## Verdict"} {
		if !strings.Contains(text, want) {
			t.Fatalf("note %q missing %q", text, want)
		}
	}
	if strings.Contains(text, "returned no evidence") {
		t.Fatalf("note %q uses the PR-child wording", text)
	}
	if len(*rows)+len(*retired) != 0 {
		t.Fatalf("rows %v retired %v", *rows, *retired)
	}
}

// TestConsultGivesUp covers every way a consult ends without a usable
// report: the item is unlinked and stays pending, the consult is retired and
// the coordinator is told once.
func TestConsultGivesUp(t *testing.T) {
	cases := []struct {
		name   string
		report string
		change func(f *fixture)
	}{
		{"timed out", "", func(f *fixture) { f.sessions[1].CreatedAt = consultNow.Add(-11 * time.Minute) }},
		{"agent ended", "", func(f *fixture) { f.sessions[1].AgentEnded = true }},
		{"holder gone", "", func(f *fixture) {
			f.tracker.Alive = func(context.Context, domain.Session) bool { return false }
		}},
		{"ended with a bad report", "no verdict here\n", func(f *fixture) { f.sessions[1].AgentEnded = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, rows, retired := consultFixture(tc.report)
			tc.change(f)
			f.tracker.TrackOnce(context.Background())
			f.tracker.TrackOnce(context.Background())

			if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "" {
				t.Fatalf("t3 = %+v, want pending and unlinked", it)
			}
			if len(*retired) != 1 || (*retired)[0] != "c1" {
				t.Fatalf("retired = %v, want c1 once", *retired)
			}
			note := "[aiman] consult c1 for t3 produced no report; ask again or proceed without it."
			if len(f.steered) != 1 || f.steered[0].session != "p" || f.steered[0].text != note {
				t.Fatalf("steered %+v, want one note %q", f.steered, note)
			}
			if len(*rows) != 1 || (*rows)[0].kind != domain.EventConsultAbandoned {
				t.Fatalf("ledger %v, want one consult.abandoned", *rows)
			}
		})
	}
}

// TestAbandonRecordsLedger records consult.abandoned with the child and the
// to-do on the parent's ledger, so review reveal can tell the consult ended.
func TestAbandonRecordsLedger(t *testing.T) {
	f, rows, _ := consultFixture("")
	f.sessions[1].AgentEnded = true
	f.tracker.TrackOnce(context.Background())
	if len(*rows) != 1 {
		t.Fatalf("ledger %+v, want one row", *rows)
	}
	row := (*rows)[0]
	if row.session != "p" || row.kind != domain.EventConsultAbandoned || row.payload["child"] != "c1" || row.payload["todo"] != "t3" {
		t.Fatalf("row %+v, want consult.abandoned {child c1, todo t3} on p", row)
	}
}

// TestConsultTimeoutForReview gives a review parent's consult
// review.consult_timeout (30 minutes by default) and any other parent's the
// handover timeout.
func TestConsultTimeoutForReview(t *testing.T) {
	timeouts := ConsultTimeouts(config.ReviewConfig{}.ConsultTimeoutOrDefault(), config.HandoverConfig{}.ChildTimeout())
	if got := timeouts(domain.Session{ReviewPR: "o/r#7"}); got != 30*time.Minute {
		t.Fatalf("review parent timeout %v, want 30m", got)
	}
	if got := timeouts(domain.Session{}); got != config.DefaultHandoverTimeout {
		t.Fatalf("other parent timeout %v, want the handover timeout", got)
	}
	cases := []struct {
		name    string
		review  string
		age     time.Duration
		abandon bool
	}{
		{"review consult within 30m", "o/r#7", 20 * time.Minute, false},
		{"review consult past 30m", "o/r#7", 31 * time.Minute, true},
		{"other consult past 10m", "", 11 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, retired := consultFixture("")
			f.sessions[0].ReviewPR = tc.review
			f.sessions[1].CreatedAt = consultNow.Add(-tc.age)
			f.tracker.ConsultTimeout = 0
			f.tracker.ConsultTimeoutFor = timeouts
			f.tracker.TrackOnce(context.Background())
			if got := len(*retired) == 1; got != tc.abandon {
				t.Fatalf("retired %v, want abandoned=%v", *retired, tc.abandon)
			}
		})
	}
}

func TestConsultAliveWithinTimeoutWaits(t *testing.T) {
	f, _, retired := consultFixture("")
	f.tracker.Alive = func(context.Context, domain.Session) bool { return true }
	f.tracker.TrackOnce(context.Background())
	if len(*retired) != 0 || len(f.steered) != 0 {
		t.Fatalf("retired %v steered %v, want nothing", *retired, f.steered)
	}
}

// TestConsultSweepRetiresOrphans retires a consult no open item links: its
// item is done or dropped (serve stopped between settle and retire), or
// nothing links it (serve stopped between start and link). A consult younger
// than the link grace is left alone.
func TestConsultSweepRetiresOrphans(t *testing.T) {
	cases := []struct {
		name    string
		item    domain.TodoItem
		created time.Time
		want    bool
	}{
		{"item done", domain.TodoItem{ID: "t3", State: domain.TodoDone, Ref: "child:c1"}, consultNow.Add(-5 * time.Minute), true},
		{"item dropped", domain.TodoItem{ID: "t3", State: domain.TodoDropped, Ref: "child:c1"}, consultNow.Add(-5 * time.Minute), true},
		{"never linked", domain.TodoItem{ID: "t3", State: domain.TodoPending}, consultNow.Add(-5 * time.Minute), true},
		{"just started", domain.TodoItem{ID: "t3", State: domain.TodoPending}, consultNow.Add(-10 * time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, retired := consultFixture("")
			f.todos.lists["p"] = domain.TodoList{SessionID: "p", Items: []domain.TodoItem{tc.item}}
			f.sessions[1].CreatedAt = tc.created
			f.tracker.TrackOnce(context.Background())
			got := len(*retired) == 1 && (*retired)[0] == "c1"
			if got != tc.want || len(*retired) > 1 {
				t.Fatalf("retired = %v, want retired %v", *retired, tc.want)
			}
			if len(f.steered) != 0 {
				t.Fatalf("steered %+v, want none", f.steered)
			}
		})
	}
}

func TestTrackerSteersSurprisesOnce(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.report = func() (string, error) {
		return "## Evidence\n\ngo test ./... exit 0\n\n## Surprises\n\nThe Store interface gained Close.\n", nil
	}
	f.tracker.TrackOnce(context.Background())
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
	want := "[aiman] child c1 reported surprises that may affect other pieces; re-read the design before starting dependent pieces:\nThe Store interface gained Close."
	if len(f.steered) != 1 || f.steered[0].session != "p" || f.steered[0].text != want {
		t.Fatalf("steered %+v, want one note %q", f.steered, want)
	}
}

func TestTrackerNoSteerWhenSurprisesNone(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())
	if len(f.steered) != 0 {
		t.Fatalf("steered %+v, want none", f.steered)
	}
}
