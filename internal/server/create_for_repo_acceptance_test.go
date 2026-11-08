package server

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// forRepoServer is a todo-enabled serve with the fake creator and a parent "p"
// on branch feat in repo o/a holding the to-do t1.
func forRepoServer(t *testing.T) (*Server, *fakeCreator) {
	t.Helper()
	srv, _, _ := todoServer(t, domain.Session{
		ID: "p", Name: "parent", Branch: "feat", BaseBranch: "main",
		RepoName: "o/a", Backend: domain.BackendPTY, TerminalName: "p",
	})
	creator := &fakeCreator{}
	srv.create = creator
	srv.SetAgentChoice(config.AgentsConfig{Preferred: config.PreferredFromCommands("claude")}, func(context.Context, string) bool { return true })
	err := srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{ID: "t1", Text: "Add the parser", State: domain.TodoPending})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, creator
}

func TestF10CreateForRepoBase(t *testing.T) {
	cases := []struct {
		name     string
		repo     string
		wantBase string
		wantRepo string
	}{
		{name: "parent's repository", repo: "", wantBase: "feat", wantRepo: "o/a"},
		{name: "another repository", repo: "o/b", wantBase: "", wantRepo: "o/b"},
		{name: "parent's repository in another case", repo: "O/A", wantBase: "feat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, creator := forRepoServer(t)
			params := map[string]any{"for": "t1", "parent": "p"}
			if c.repo != "" {
				params["repo"] = c.repo
			}
			resp := rpc(t, srv, "", "session.create", params)
			if resp.Error != nil {
				t.Fatalf("create: %+v", resp.Error)
			}
			if creator.calls != 1 {
				t.Fatalf("creator ran %d times, want 1", creator.calls)
			}
			cfg := creator.cfg
			if cfg.BaseBranch != c.wantBase {
				t.Fatalf("base = %q, want %q", cfg.BaseBranch, c.wantBase)
			}
			if c.wantRepo != "" && cfg.Repo.Name != c.wantRepo {
				t.Fatalf("repo = %q, want %q", cfg.Repo.Name, c.wantRepo)
			}
			if cfg.Branch != "feat--t1" {
				t.Fatalf("branch = %q, want feat--t1", cfg.Branch)
			}
		})
	}
}
