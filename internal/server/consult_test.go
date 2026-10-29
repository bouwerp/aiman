package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/todo"
)

const consultBrief = "## Question\n\nWhat is wrong with this design?\n\n## Scope\n\nRead the parent worktree; edit nothing.\n\n" +
	"## Report must contain\n\nObjections, each with severity and a suggested change, and a verdict.\n"

// consultServer is forServer with the smart tier set to smart and the worker
// tier to claude, and HOME pointed at a temp dir.
func consultServer(t *testing.T, smart []config.PreferredAgent) (*Server, *fakeCreator, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv, creator := forServer(t, "")
	srv.SetAgentChoice(config.AgentsConfig{Smart: smart, Preferred: config.PreferredFromCommands("claude")},
		func(context.Context, string) bool { return true })
	return srv, creator, home
}

func consultParams() map[string]any {
	return map[string]any{"for": "t3", "parent": "p", "consult": true, "brief": consultBrief}
}

func TestCreateConsult(t *testing.T) {
	srv, creator, home := consultServer(t, []config.PreferredAgent{{Agent: "codex", Model: "gpt-5.4"}})
	resp := rpc(t, srv, "", "session.create", consultParams())
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	cfg := creator.cfg
	if cfg.Role != domain.SessionRoleConsult || cfg.ParentID != "p" || cfg.Tier != config.TierSmart {
		t.Fatalf("cfg role %q parent %q tier %q", cfg.Role, cfg.ParentID, cfg.Tier)
	}
	if cfg.Branch != "" || cfg.BaseBranch != "" || cfg.Repo.Name != "" || !cfg.AdHoc {
		t.Fatalf("cfg branch %q base %q repo %q adhoc %v, want an ad-hoc session with no branch",
			cfg.Branch, cfg.BaseBranch, cfg.Repo.Name, cfg.AdHoc)
	}
	if cfg.Agent == nil || cfg.Agent.Command != "codex" || cfg.Agent.LaunchModel != "gpt-5.4" {
		t.Fatalf("agent = %+v, want codex/gpt-5.4", cfg.Agent)
	}
	base := filepath.Join(home, ".aiman", "consults") + string(filepath.Separator)
	if !strings.HasPrefix(cfg.PreparedWorktree, base) {
		t.Fatalf("prepared worktree %q, want under %s", cfg.PreparedWorktree, base)
	}
	if !strings.Contains(cfg.InitialPrompt, ".aiman_task.md") {
		t.Fatalf("prompt %q does not point at the task file", cfg.InitialPrompt)
	}
	task, err := os.ReadFile(filepath.Join(cfg.PreparedWorktree, domain.AimanTaskFileName))
	if err != nil || !strings.Contains(string(task), "What is wrong with this design?") {
		t.Fatalf("task file %q, %v", task, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.PreparedWorktree, ".git")); err != nil {
		t.Fatalf("scratch dir is not a git repository: %v", err)
	}
	row, err := srv.repo.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if row.Role != domain.SessionRoleConsult || !row.Role.Hidden() || row.ParentID != "p" || row.Tier != config.TierSmart {
		t.Fatalf("row role %q parent %q tier %q", row.Role, row.ParentID, row.Tier)
	}
	if !strings.HasPrefix(row.WorkingDirectory, base) {
		t.Fatalf("row working directory %q, want under %s", row.WorkingDirectory, base)
	}
	if it := parentT3(t, srv); it.Ref != coordinator.ChildRef("new-id") {
		t.Fatalf("t3 ref = %q", it.Ref)
	}
}

func TestCreateConsultNeedsSmart(t *testing.T) {
	srv, creator, _ := consultServer(t, nil)
	wantCode(t, rpc(t, srv, "", "session.create", consultParams()), CodeConsultNeedsSmart)
	if creator.calls != 0 {
		t.Fatalf("created %d sessions", creator.calls)
	}
}

func TestCreateConsultAllowedBeforeApproval(t *testing.T) {
	srv, _, _ := consultServer(t, []config.PreferredAgent{{Agent: "codex"}})
	err := srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l)
		for i := range l.Items {
			if l.Items[i].Ref == todo.DesignGateRef {
				l.Items[i].ID = "g1"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp := rpc(t, srv, "", "session.create", consultParams()); resp.Error != nil {
		t.Fatalf("consult with an open gate: %+v", resp.Error)
	}
}

func TestCreateConsultUsesConsultBrief(t *testing.T) {
	srv, creator, _ := consultServer(t, []config.PreferredAgent{{Agent: "codex"}})
	if resp := rpc(t, srv, "", "session.create", consultParams()); resp.Error != nil {
		t.Fatalf("three-heading brief refused: %+v", resp.Error)
	}
	if creator.calls != 1 {
		t.Fatalf("calls = %d", creator.calls)
	}
	bad := consultParams()
	bad["for"] = "t2"
	bad["brief"] = "## Question\n\nwhy?\n"
	wantCode(t, rpc(t, srv, "", "session.create", bad), CodeInvalidParams)
	noBrief := consultParams()
	noBrief["for"] = "t2"
	delete(noBrief, "brief")
	wantCode(t, rpc(t, srv, "", "session.create", noBrief), CodeInvalidParams)
}

func TestCreateConsultRefusesWorkerTierAndAgent(t *testing.T) {
	srv, creator, _ := consultServer(t, []config.PreferredAgent{{Agent: "codex"}})
	for _, extra := range []map[string]any{{"tier": "worker"}, {"agent": "claude"}} {
		p := consultParams()
		for k, v := range extra {
			p[k] = v
		}
		wantCode(t, rpc(t, srv, "", "session.create", p), CodeInvalidParams)
	}
	if creator.calls != 0 {
		t.Fatalf("created %d sessions", creator.calls)
	}
}

func TestRetireConsult(t *testing.T) {
	srv, creator, _ := consultServer(t, []config.PreferredAgent{{Agent: "codex"}})
	if resp := rpc(t, srv, "", "session.create", consultParams()); resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	dir := creator.cfg.PreparedWorktree
	row, err := srv.repo.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatal(err)
	}
	srv.RetireConsult(context.Background(), *row)
	if _, err := srv.repo.Get(context.Background(), "new-id"); err == nil {
		t.Fatal("consult row survived retire")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir survived retire: %v", err)
	}
}

// A consult row whose directory is outside the consult base keeps it.
func TestRetireConsultKeepsForeignDir(t *testing.T) {
	srv, _, _ := consultServer(t, nil)
	dir := t.TempDir()
	srv.RetireConsult(context.Background(), domain.Session{ID: "x", Role: domain.SessionRoleConsult, WorkingDirectory: dir})
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("foreign dir removed: %v", err)
	}
}
