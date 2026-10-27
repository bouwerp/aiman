package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestStartIfEnabledDisabled(t *testing.T) {
	r := newMRig(t, Config{BeforePR: false})
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1", IfEnabled: true})
	if err != nil || rv.Status != StatusDisabled {
		t.Fatalf("review %+v err %v", rv, err)
	}
	if len(r.sessions.createdConfigs()) != 0 {
		t.Fatal("a disabled review created a session")
	}
}

func TestStartRejectsReviewerSession(t *testing.T) {
	r := newMRig(t, Config{})
	rev := r.worker
	rev.ID, rev.Role = "r0", domain.SessionRoleReviewer
	r.sessions.sessions[rev.ID] = rev
	if _, err := r.m.Start(context.Background(), StartRequest{WorkerID: "r0"}); !errors.Is(err, ErrReviewerSession) {
		t.Fatalf("err %v", err)
	}
}

func TestStartNoReviewer(t *testing.T) {
	r := newMRig(t, Config{Agents: []string{"claude"}})
	if _, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"}); !errors.Is(err, ErrNoReviewer) {
		t.Fatalf("err %v", err)
	}
}

func TestStartCreatesHiddenChild(t *testing.T) {
	r := newMRig(t, Config{})
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if rv.ID == "" || rv.Agent != "codex" {
		t.Fatalf("review %+v", rv)
	}
	running := r.waitStatus(t, rv.ID, StatusRunning)
	cfgs := r.sessions.createdConfigs()
	if len(cfgs) != 1 {
		t.Fatalf("created %d", len(cfgs))
	}
	cfg := cfgs[0]
	wantPath := filepath.Join(filepath.Dir(r.worker.WorktreePath), "repo@review-"+rv.ID[:8])
	if cfg.Role != domain.SessionRoleReviewer || cfg.ParentID != "w1" || cfg.Group != "PROJ-1" ||
		cfg.PreparedWorktree != wantPath || !cfg.PromptFree || cfg.IssueKey != "" ||
		cfg.InitialPrompt != InitialPrompt || cfg.Agent == nil || cfg.Agent.Command != "codex" || cfg.Directory != "sub" {
		t.Fatalf("cfg %+v (agent %+v)", cfg, cfg.Agent)
	}
	if !strings.HasPrefix(cfg.Name, "impl-review-") || len(cfg.Name) != len("impl-review-")+4 {
		t.Fatalf("name %q", cfg.Name)
	}
	brief, err := os.ReadFile(filepath.Join(wantPath, "sub", domain.AimanTaskFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "Add x.") || !strings.Contains(string(brief), "var x = 1") {
		t.Fatalf("brief missing task or diff:\n%s", brief)
	}
	if running.ReviewerID != "rev-1" || running.Worktree != wantPath {
		t.Fatalf("running %+v", running)
	}
}

func TestStartReturnsExistingReviewForSameTree(t *testing.T) {
	r := newMRig(t, Config{})
	first, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	r.waitStatus(t, first.ID, StatusRunning)
	second, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || len(r.sessions.createdConfigs()) != 1 {
		t.Fatalf("second %s first %s, created %d", second.ID, first.ID, len(r.sessions.createdConfigs()))
	}
	latest, ok := r.m.LatestFor("w1")
	if !ok || latest.ID != first.ID {
		t.Fatalf("latest %+v %v", latest, ok)
	}
}

func TestStartNewTreeStartsNewReview(t *testing.T) {
	r := newMRig(t, Config{})
	first, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	r.waitStatus(t, first.ID, StatusRunning)
	if err := os.WriteFile(filepath.Join(r.worker.WorkingDirectory, "b.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("an edited tree re-attached to the old review")
	}
}

func TestStartFailsCleanly(t *testing.T) {
	r := newMRig(t, Config{})
	r.sessions.createErr = errors.New("agent api down")
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	failed := r.waitStatus(t, rv.ID, StatusFailed)
	if !strings.Contains(failed.Error, "agent api down") {
		t.Fatalf("error %q", failed.Error)
	}
	if _, err := os.Stat(failed.Worktree); !os.IsNotExist(err) {
		t.Fatalf("review worktree left behind: %v", err)
	}
}
