package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// A landed child whose item is done is terminated once, and the parent's
// ledger records it.
func TestTrackFinishesALandedChild(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	var finished []string
	f.tracker.Finish = func(_ context.Context, child domain.Session) error {
		finished = append(finished, child.ID)
		return nil
	}
	var rows []string
	f.tracker.Ledger = func(_ context.Context, id string, kind domain.EventKind, payload map[string]any) {
		rows = append(rows, id+" "+string(kind)+" "+payload["child"].(string))
	}
	f.tracker.TrackOnce(context.Background())
	f.tracker.TrackOnce(context.Background())

	if len(finished) != 1 || finished[0] != "c1" {
		t.Fatalf("finished %v, want [c1] once", finished)
	}
	if len(rows) != 1 || rows[0] != "p child.finished c1" {
		t.Fatalf("ledger %v", rows)
	}
}

// A child that has not landed, or has no evidence, keeps running.
func TestTrackLeavesAnUnfinishedChild(t *testing.T) {
	for name, f := range map[string]*fixture{
		"not landed":  newFixture("child:c1", PRState{Base: "feat/x", Open: true}),
		"no evidence": newFixture("child:c1", PRState{Base: "feat/x", Merged: true}),
	} {
		if name == "no evidence" {
			f.report = func() (string, error) { return "", errors.New("no report") }
		}
		f.tracker.Finish = func(context.Context, domain.Session) error {
			t.Fatalf("%s: Finish called", name)
			return nil
		}
		f.tracker.TrackOnce(context.Background())
	}
}

// A child aiman cannot terminate stays, and the coordinator hears it once.
func TestTrackTellsTheCoordinatorWhenFinishFails(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.Finish = func(context.Context, domain.Session) error {
		return errors.New("worktree has 2 changed files")
	}
	f.tracker.TrackOnce(context.Background())
	f.tracker.TrackOnce(context.Background())

	if len(f.steered) != 1 || f.steered[0].session != "p" ||
		!strings.Contains(f.steered[0].text, "could not terminate it: worktree has 2 changed files") ||
		!strings.Contains(f.steered[0].text, "aiman session terminate child") {
		t.Fatalf("steered %+v", f.steered)
	}
}
