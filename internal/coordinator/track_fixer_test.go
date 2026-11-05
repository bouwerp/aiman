package coordinator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// fixerFixture links item t3 of parent p to fixer c1, created 5 minutes ago.
func fixerFixture() (*fixture, *[]ledgerRow, *[]string) {
	f := newFixture("fixer:c1", PRState{})
	f.sessions = []domain.Session{
		{ID: "p", Name: "review", ReviewPR: "o/r#7"},
		{ID: "c1", Name: "fixer", ParentID: "p", Role: domain.SessionRoleFixer, FixPR: "o/r#7", CreatedAt: consultNow.Add(-5 * time.Minute)},
	}
	f.tracker.Sessions = func() ([]domain.Session, error) { return f.sessions, nil }
	f.tracker.Now = func() time.Time { return consultNow }
	f.tracker.FixTimeout = 60 * time.Minute
	f.tracker.Alive = func(context.Context, domain.Session) bool { return true }
	rows := &[]ledgerRow{}
	retired := &[]string{}
	f.tracker.Ledger = func(_ context.Context, id string, kind domain.EventKind, payload map[string]any) {
		*rows = append(*rows, ledgerRow{session: id, kind: kind, payload: payload})
	}
	f.tracker.Retire = func(_ context.Context, child domain.Session) {
		*retired = append(*retired, child.ID)
		f.sessions = slices.DeleteFunc(f.sessions, func(s domain.Session) bool { return s.ID == child.ID })
	}
	return f, rows, retired
}

func TestTrackFixerRunningStaysLinked(t *testing.T) {
	f, rows, retired := fixerFixture()
	f.tracker.TrackOnce(context.Background())
	it := f.todos.t3(t)
	if it.Ref != "fixer:c1" || it.State != domain.TodoPending {
		t.Fatalf("item %+v", it)
	}
	if len(*rows) != 0 || len(*retired) != 0 || len(f.steered) != 0 {
		t.Fatalf("rows %v retired %v steered %v", *rows, *retired, f.steered)
	}
	if len(f.probed) != 0 {
		t.Fatalf("a fixer has no PR of its own, but %v were probed", f.probed)
	}
}

func TestTrackFixerGivenUp(t *testing.T) {
	cases := map[string]func(f *fixture){
		"agent ended":   func(f *fixture) { f.sessions[1].AgentEnded = true },
		"terminal gone": func(f *fixture) { f.tracker.Alive = func(context.Context, domain.Session) bool { return false } },
		"timed out":     func(f *fixture) { f.sessions[1].CreatedAt = consultNow.Add(-61 * time.Minute) },
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			f, rows, retired := fixerFixture()
			prepare(f)
			f.tracker.TrackOnce(context.Background())
			it := f.todos.t3(t)
			if it.Ref != "" || it.State != domain.TodoPending {
				t.Fatalf("item %+v, want unlinked and pending", it)
			}
			if len(*rows) != 1 || (*rows)[0].session != "p" || (*rows)[0].kind != domain.EventKind("review.fix_failed") || (*rows)[0].payload["fixer"] != "c1" {
				t.Fatalf("ledger %+v", *rows)
			}
			if !slices.Equal(*retired, []string{"c1"}) {
				t.Fatalf("retired %v", *retired)
			}
			if len(f.steered) != 1 || f.steered[0].session != "p" || !strings.Contains(f.steered[0].text, "c1") {
				t.Fatalf("steered %+v", f.steered)
			}
			f.tracker.TrackOnce(context.Background())
			if len(*rows) != 1 || len(f.steered) != 1 {
				t.Fatalf("a second pass repeated the failure: %+v %+v", *rows, f.steered)
			}
		})
	}
}

func TestTrackFixerRowGoneUnlinks(t *testing.T) {
	f, rows, retired := fixerFixture()
	f.sessions = f.sessions[:1]
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.Ref != "" || it.State != domain.TodoPending {
		t.Fatalf("item %+v", it)
	}
	if len(*rows) != 1 || (*rows)[0].kind != domain.EventKind("review.fix_failed") {
		t.Fatalf("ledger %+v", *rows)
	}
	if len(*retired) != 0 {
		t.Fatalf("retired %v", *retired)
	}
}
