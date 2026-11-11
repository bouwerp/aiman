package coordinator

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// ci-minutes rev 3, piece B: with coordinator.child_prs false a child is
// landed when its merge reached the parent's branch, never through a PR.

func TestLandedLocalChildIsDoneWithoutAPR(t *testing.T) {
	f := newFixture("child:c1", PRState{})
	f.tracker.ChildPRs = func(domain.Session) bool { return false }
	f.tracker.LandedLocal = func(_ context.Context, parent, child domain.Session) (bool, error) {
		return parent.ID == "p" && child.ID == "c1", nil
	}
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done once the child landed locally", it.State)
	}
	if len(f.probed) != 0 {
		t.Fatalf("PR probed %v; local landing must not ask GitHub", f.probed)
	}
}

func TestLandedLocalIgnoresAMergedPR(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.ChildPRs = func(domain.Session) bool { return false }
	f.tracker.LandedLocal = func(context.Context, domain.Session, domain.Session) (bool, error) { return false, nil }
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State == domain.TodoDone {
		t.Fatal("t3 done from a merged PR while child_prs is false")
	}
}

func TestChildPRsTrueKeepsThePRPath(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.ChildPRs = func(domain.Session) bool { return true }
	f.tracker.LandedLocal = func(context.Context, domain.Session, domain.Session) (bool, error) {
		t.Fatal("LandedLocal called while child_prs is true")
		return false, nil
	}
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done from the merged PR", it.State)
	}
}
