package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// A restart writes the launch model, vendor id and ended flag as ChangeOther,
// which leaves the session list cache in place. A second call queued behind
// the lock must still act on what the first one wrote.
func TestLockedTargetReadsTheRowAfterTheLock(t *testing.T) {
	old := sessionListTTL
	sessionListTTL = time.Minute
	t.Cleanup(func() { sessionListTTL = old })

	repo, err := sqlite.NewRepository(filepath.Join(shortTempDir(t), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	row := &domain.Session{
		ID: "lt-1", Name: "lt", Group: "g", Backend: domain.BackendPTY, AgentName: "claude",
		LaunchModel: "old-model", AgentSessionID: "old-vendor-id", AgentSessionPath: "/t/old.jsonl",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}
	if err := repo.Save(ctx, row); err != nil {
		t.Fatal(err)
	}
	srv := New(nil, repo, &fakeRemote{}, nil, nil, nil, "test")
	if _, err := srv.loadSessions(ctx); err != nil { // the cache now holds the old row
		t.Fatal(err)
	}

	row.LaunchModel = "new-model"
	row.LaunchEffort = "high"
	row.AgentSessionID = "new-vendor-id"
	row.AgentSessionPath = "/t/new.jsonl"
	row.AgentEnded = true
	if err := repo.Save(ctx, row); err != nil {
		t.Fatal(err)
	}

	sess, list, unlock, resp := srv.lockedTarget(ctx, Request{ID: "1"}, "lt")
	if resp != nil {
		t.Fatalf("lockedTarget: %+v", resp.Error)
	}
	unlock()
	if sess.LaunchModel != "new-model" || sess.LaunchEffort != "high" ||
		sess.AgentSessionID != "new-vendor-id" || sess.AgentSessionPath != "/t/new.jsonl" || !sess.AgentEnded {
		t.Fatalf("lockedTarget returned a cached row: %+v", sess)
	}
	for _, l := range list {
		if l.ID == sess.ID && l.LaunchModel != "new-model" {
			t.Fatalf("the list's copy of the target is stale: %+v", l)
		}
	}
}
