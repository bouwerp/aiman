package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

func replayRepo(t *testing.T, home string) *sqlite.Repository {
	t.Helper()
	t.Setenv("HOME", home)
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestReplayBundleWritesFiles(t *testing.T) {
	home := t.TempDir()
	repo := replayRepo(t, home)
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, domain.AimanHandoverFileName), []byte("# Handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	todoDir := filepath.Join(home, ".aiman", "todos")
	if err := os.MkdirAll(todoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(todoDir, "s1.json"), []byte(`{"rev":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{
		ID: "s1", Name: "fix-login", Group: "g", WorkingDirectory: dir,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []domain.SessionEvent{
		{SessionID: "s1", Actor: domain.EventActorUser, Kind: domain.EventSessionCreated, Payload: json.RawMessage(`{"issue":"A"}`)},
		{SessionID: "s1", Actor: domain.EventActorAgent, Kind: domain.EventHookReported, Payload: json.RawMessage(`{"state":"idle"}`)},
	} {
		if _, err := repo.AppendSessionEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	bundle, err := ReplayBundle(ctx, repo, "s1", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bundle.Manifest, bundle.Events, bundle.Todo, bundle.Handover} {
		if path == "" {
			t.Fatal("bundle field is empty")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if bundle.Review != "" {
		t.Fatalf("review unexpectedly present: %s", bundle.Review)
	}
	events, err := os.ReadFile(bundle.Events)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(events), "\n"); lines != 2 {
		t.Fatalf("event lines = %d", lines)
	}
	manifest, err := os.ReadFile(bundle.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"session_id": "s1"`, `"name": "fix-login"`, `"events": 2`, `"todo": "todo.json"`, `"handover": ".aiman_handover.md"`} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestReplayBundleMissingArtifactsAreOmitted(t *testing.T) {
	home := t.TempDir()
	repo := replayRepo(t, home)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "s1", Name: "s1", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	bundle, err := ReplayBundle(ctx, repo, "s1", out)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Todo != "" || bundle.Handover != "" || bundle.Review != "" {
		t.Fatalf("unexpected artifacts: %+v", bundle)
	}
	for _, name := range []string{"todo.json", domain.AimanHandoverFileName, "review.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("%s exists", name)
		}
	}
}

type replayFakeRepo struct {
	domain.SessionRepository
	sess   *domain.Session
	events []domain.SessionEvent
}

func (r replayFakeRepo) Get(context.Context, string) (*domain.Session, error) {
	if r.sess == nil {
		return nil, errors.New("missing")
	}
	return r.sess, nil
}

func (r replayFakeRepo) ListSessionEvents(context.Context, string, int64, int) ([]domain.SessionEvent, error) {
	return r.events, nil
}

func TestReplayBundleCapsEvents(t *testing.T) {
	events := make([]domain.SessionEvent, maxReplayEvents+1)
	for i := range events {
		events[i] = domain.SessionEvent{SessionID: "s1", Seq: int64(i + 1), Kind: domain.EventHookReported}
	}
	repo := replayFakeRepo{
		sess:   &domain.Session{ID: "s1"},
		events: events,
	}
	_, err := ReplayBundle(context.Background(), repo, "s1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "events") {
		t.Fatalf("err = %v", err)
	}
}

func TestReplayBundleCapsBytes(t *testing.T) {
	repo := replayFakeRepo{
		sess: &domain.Session{ID: "s1"},
		events: []domain.SessionEvent{{
			SessionID: "s1", Seq: 1, Kind: domain.EventHookReported,
			Payload: make([]byte, maxReplayBytes+1),
		}},
	}
	_, err := ReplayBundle(context.Background(), repo, "s1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v", err)
	}
}
