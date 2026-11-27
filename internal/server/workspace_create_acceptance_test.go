package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// Acceptance tests for multi-repo sessions, piece P2. Children must not edit
// this file.

func wsServer(t *testing.T, creator *fakeCreator) (*Server, *sqlite.Repository) {
	t.Helper()
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
	return New(ln, repo, &fakeRemote{}, creator, nil, nil, "test"), repo
}

func wsMembers() []domain.SessionRepo {
	return []domain.SessionRepo{
		{Repo: "o/a", Branch: "host-tab", BaseBranch: "main", WorktreePath: "/w/host-tab@ws/a", Ord: 0},
		{Repo: "o/b", Branch: "host-tab", BaseBranch: "dev", WorktreePath: "/w/host-tab@ws/b", Ord: 1},
	}
}

func TestSessionCreateRepos(t *testing.T) {
	creator := &fakeCreator{sess: &domain.Session{
		ID: "ws-1", Branch: "host-tab", WorktreePath: "/w/host-tab@ws", WorkingDirectory: "/w/host-tab@ws",
		AgentName: "codex", Backend: domain.BackendPTY, Status: domain.SessionStatusActive, Members: wsMembers(),
	}}
	srv, repo := wsServer(t, creator)
	resp := rpc(t, srv, "", "session.create", map[string]any{
		"repos":  []map[string]any{{"repo": "o/a"}, {"repo": "o/b", "base": "dev"}},
		"branch": "host-tab", "agent": "codex",
	})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	cfg := creator.cfg
	if cfg.Repo.Name != "" || len(cfg.Workspace) != 2 ||
		cfg.Workspace[0] != (domain.RepoSpec{Repo: "o/a"}) || cfg.Workspace[1] != (domain.RepoSpec{Repo: "o/b", Base: "dev"}) {
		t.Fatalf("creator config repo %q workspace %+v", cfg.Repo.Name, cfg.Workspace)
	}
	stored, err := repo.Get(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Members) != 2 {
		t.Fatalf("stored members = %+v", stored.Members)
	}
	b, _ := json.Marshal(resp.Result)
	var out struct {
		Session struct {
			Repos []struct {
				Repo         string `json:"repo"`
				Branch       string `json:"branch"`
				BaseBranch   string `json:"base_branch"`
				WorktreePath string `json:"worktree_path"`
			} `json:"repos"`
		} `json:"session"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Session.Repos) != 2 || out.Session.Repos[1].Repo != "o/b" || out.Session.Repos[1].BaseBranch != "dev" || out.Session.Repos[1].WorktreePath != "/w/host-tab@ws/b" {
		t.Fatalf("result repos = %+v", out.Session.Repos)
	}

	// session.get answers the members too.
	got := rpc(t, srv, "", "session.get", map[string]any{"session": "ws-1"})
	gb, _ := json.Marshal(got.Result)
	if err := json.Unmarshal(gb, &out); err != nil || len(out.Session.Repos) != 2 {
		t.Fatalf("session.get repos = %s", gb)
	}
}

func TestSessionCreateReposRefusals(t *testing.T) {
	nine := []map[string]any{}
	for _, r := range []string{"o/1", "o/2", "o/3", "o/4", "o/5", "o/6", "o/7", "o/8", "o/9"} {
		nine = append(nine, map[string]any{"repo": r})
	}
	cases := map[string]map[string]any{
		"one repo":   {"repos": []map[string]any{{"repo": "o/a"}}, "branch": "x", "agent": "codex"},
		"nine repos": {"repos": nine, "branch": "x", "agent": "codex"},
		"duplicate":  {"repos": []map[string]any{{"repo": "o/a"}, {"repo": "O/A"}}, "branch": "x", "agent": "codex"},
		"with repo":  {"repo": "o/a", "repos": []map[string]any{{"repo": "o/b"}, {"repo": "o/c"}}, "branch": "x", "agent": "codex"},
		"with quick": {"quick": true, "repos": []map[string]any{{"repo": "o/b"}, {"repo": "o/c"}}, "agent": "codex"},
		"with dir":   {"dir": "sub", "repos": []map[string]any{{"repo": "o/b"}, {"repo": "o/c"}}, "branch": "x", "agent": "codex"},
		"bad base":   {"repos": []map[string]any{{"repo": "o/b", "base": "-rf"}, {"repo": "o/c"}}, "branch": "x", "agent": "codex"},
	}
	for name, params := range cases {
		creator := &fakeCreator{}
		resp := createOnFakeCreator(t, creator, params)
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Errorf("%s: want invalid_params, got %+v", name, resp.Error)
		}
		if creator.calls != 0 {
			t.Errorf("%s: creator ran", name)
		}
	}
}

// A laptop register carries no members and may guess a repo name; neither
// may change a workspace session's row.
func TestRegisterKeepsMembers(t *testing.T) {
	srv, repo := wsServer(t, &fakeCreator{})
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{ID: "ws-1", Name: "w", Branch: "host-tab", WorktreePath: "/w/host-tab@ws", Status: domain.SessionStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSessionRepos(ctx, "ws-1", wsMembers()); err != nil {
		t.Fatal(err)
	}
	resp := rpc(t, srv, "", "session.register", map[string]any{
		"sessions": []domain.Session{{ID: "ws-1", Name: "w", RepoName: "o/a", Branch: "host-tab"}},
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	got, err := repo.Get(ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || got.RepoName != "" {
		t.Fatalf("after register: repo %q members %+v", got.RepoName, got.Members)
	}
}
