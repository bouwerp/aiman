package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Clients route pane capture, prompt delivery and teardown on Backend, and an
// absent value reads as tmux. Leaving it off the wire pointed every client at
// tmux for sessions living in the PTY runtime, so their previews never loaded.
func TestSessionListCarriesTheBackend(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "child-1", Name: "review-treasury-prs", Group: "pr-review",
		ParentID: "parent-1", TmuxSession: "fix-yield", Backend: domain.BackendPTY,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)

	resp, err := Call(sock, "session.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("list failed: %v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(out.Sessions))
	}
	if out.Sessions[0].Backend != domain.BackendPTY {
		t.Errorf("backend lost on the wire: got %q, want %q", out.Sessions[0].Backend, domain.BackendPTY)
	}
	if out.Sessions[0].ParentID != "parent-1" {
		t.Errorf("parent lost on the wire: %+v", out.Sessions[0])
	}
}

func TestSessionInfoCarriesTheBackend(t *testing.T) {
	info := sessionInfo(domain.Session{ID: "a", Backend: domain.BackendPTY}, "")
	if info.Backend != domain.BackendPTY {
		t.Errorf("got %q, want %q", info.Backend, domain.BackendPTY)
	}
}
