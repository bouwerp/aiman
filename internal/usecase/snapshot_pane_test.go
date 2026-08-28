package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type snapshotRepo struct {
	domain.SessionRepository
	saved []*domain.SessionSnapshot
}

func (r *snapshotRepo) SaveSnapshot(_ context.Context, s *domain.SessionSnapshot) error {
	r.saved = append(r.saved, s)
	return nil
}

// Teardown has to keep the pane before the terminal is killed, and cannot wait
// on a model to do it — so the summary fields stay empty and the transcript is
// what gets preserved.
func TestSavePaneSnapshotStoresThePaneWithoutSummarising(t *testing.T) {
	repo := &snapshotRepo{}
	// A nil intelligence provider proves no AI call is made: one would panic.
	m := NewSnapshotManager(repo, nil)

	sess := &domain.Session{ID: "sess-1", Branch: "fix/thing", RepoName: "org/repo"}
	snap, err := m.SavePaneSnapshot(context.Background(), sess, "agent output worth keeping")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("expected one snapshot, got %d", len(repo.saved))
	}
	if snap.Summary != "" || len(snap.Overview) != 0 || len(snap.NextSteps) != 0 {
		t.Errorf("no AI summary should be recorded: %+v", snap)
	}
	if snap.SessionID != "sess-1" || snap.Branch != "fix/thing" {
		t.Errorf("session identity not carried onto the snapshot: %+v", snap)
	}
	got, err := DecompressPaneContent(snap.PaneContent)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !strings.Contains(got, "agent output worth keeping") {
		t.Errorf("pane content not preserved, got %q", got)
	}
}

// A session whose agent died on creation has an empty pane. Recording that
// would only fill the snapshot table with noise.
func TestSavePaneSnapshotSkipsAnEmptyPane(t *testing.T) {
	for _, raw := range []string{"", "   \n\n  \t "} {
		repo := &snapshotRepo{}
		m := NewSnapshotManager(repo, nil)
		_, err := m.SavePaneSnapshot(context.Background(), &domain.Session{ID: "sess-1"}, raw)
		if !errors.Is(err, ErrNothingToSnapshot) {
			t.Errorf("raw %q: got %v, want ErrNothingToSnapshot", raw, err)
		}
		if len(repo.saved) != 0 {
			t.Errorf("raw %q: nothing should be persisted", raw)
		}
	}
}
