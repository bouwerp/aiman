package server

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// serve keeps its own database and the dashboard rebuilds its list from serve's
// live stream, so a teardown that deleted only the dashboard's row let the
// session reappear on the next poll — ctrl+k looked like it had done nothing.
func TestSessionForgetRemovesTheRow(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "dead-1", Name: "sol", Group: "pr-439-review", TmuxSession: "fix-yield",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)

	resp, err := Call(sock, "session.forget", map[string]any{"id": "sol"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("forget failed: %v", resp.Error)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == "dead-1" {
			t.Fatal("the row survived session.forget")
		}
	}
}

func TestSessionForgetReportsAnUnknownTarget(t *testing.T) {
	sock := startTestServer(t, testRepo(t))
	resp, err := Call(sock, "session.forget", map[string]any{"id": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("got %+v, want %s", resp.Error, CodeNotFound)
	}
}
