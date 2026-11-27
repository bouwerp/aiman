package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// A derived branch busy in any member takes the next suffix, as for one repo.
func TestSessionCreateReposDerivedBranchSuffix(t *testing.T) {
	creator := &fakeCreator{failWith: func(cfg domain.SessionConfig) error {
		if len(cfg.Workspace) != 2 {
			return errors.New("not a workspace create")
		}
		if cfg.Branch == "host-tab" {
			return fmt.Errorf("member o/b: %w", errors.New("WORKTREE_EXISTS"))
		}
		return nil
	}}
	resp := createOnFakeCreator(t, creator, map[string]any{
		"repos": []map[string]any{{"repo": "o/a"}, {"repo": "o/b"}},
		"name":  "host tab", "agent": "codex",
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if got := strings.Join(creator.branches, " "); got != "host-tab host-tab-2" {
		t.Fatalf("branches tried: %s", got)
	}
}

// Discarding a workspace session removes every member worktree and branch.
func TestDiscardChildRemovesMembers(t *testing.T) {
	repo, err := sqlite.NewRepository(filepath.Join(shortTempDir(t), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	remote := &fakeRemote{}
	srv := New(ln, repo, remote, &fakeCreator{}, nil, nil, "test")
	sess := domain.Session{ID: "ws-1", Branch: "host-tab", WorktreePath: "/w/host-tab@ws", Members: wsMembers()}
	if err := srv.discardChild(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(remote.execs, "\n")
	for _, path := range []string{"/w/host-tab@ws/a", "/w/host-tab@ws/b"} {
		if !strings.Contains(all, quoteArg(path)) {
			t.Errorf("no removal of %s in %s", path, all)
		}
	}
	if strings.Contains(all, "wt="+quoteArg("/w/host-tab@ws")+"\n") {
		t.Errorf("removal ran on the workspace directory: %s", all)
	}
}
