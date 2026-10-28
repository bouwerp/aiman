package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/quality"
)

func TestStartReadsKitFromSessionTask(t *testing.T) {
	r := newMRig(t, Config{})
	list := filepath.Join(t.TempDir(), "go.md")
	if err := os.WriteFile(list, []byte("- [ ] wrap errors with %w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "gone.md")
	dw := quality.RenderDoneWhenBlock([]string{"tests added"})
	cl := quality.RenderChecklistBlock([]domain.Checklist{{Path: list}, {Path: missing}})
	task := quality.ReplaceKitBlocks("# Task\n\nAdd x.\n", &dw, cl)
	if err := os.WriteFile(filepath.Join(r.worker.WorkingDirectory, domain.AimanTaskFileName), []byte(task), 0o600); err != nil {
		t.Fatal(err)
	}
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	running := r.waitStatus(t, rv.ID, StatusRunning)
	brief, err := os.ReadFile(filepath.Join(running.Worktree, running.Dir, domain.AimanTaskFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Review rubric", "### " + list + "\n\n- [ ] wrap errors with %w", "### " + missing + "\n\nnot readable",
		"## Done-when conditions", "- tests added",
	} {
		if !strings.Contains(string(brief), want) {
			t.Fatalf("brief missing %q:\n%s", want, brief)
		}
	}
}

func TestReadRubricCapsBody(t *testing.T) {
	list := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(list, []byte(strings.Repeat("x", 2*maxRubricBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readRubric([]string{list})
	if len(got) != 1 || len(got[0].Body) != maxRubricBytes {
		t.Fatalf("rubric %d items", len(got))
	}
}

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

func TestStartRejectsHiddenSession(t *testing.T) {
	r := newMRig(t, Config{})
	for _, role := range []domain.SessionRole{domain.SessionRoleReviewer, domain.SessionRoleHandover} {
		hidden := r.worker
		hidden.ID, hidden.Role = "h-"+string(role), role
		r.sessions.sessions[hidden.ID] = hidden
		if _, err := r.m.Start(context.Background(), StartRequest{WorkerID: hidden.ID}); !errors.Is(err, ErrReviewerSession) {
			t.Fatalf("%s: err %v", role, err)
		}
	}
}

func TestStartNoReviewer(t *testing.T) {
	r := newMRig(t, Config{Agents: config.PreferredFromCommands("claude")})
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

func TestStartReadsTaskFromWorktreeWithoutWorkingDir(t *testing.T) {
	r := newMRig(t, Config{})
	w := r.worker
	w.WorkingDirectory = ""
	if err := os.WriteFile(filepath.Join(w.WorktreePath, domain.AimanTaskFileName), []byte("Root task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.sessions.sessions["w1"] = w
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	running := r.waitStatus(t, rv.ID, StatusRunning)
	brief, err := os.ReadFile(filepath.Join(running.Worktree, domain.AimanTaskFileName))
	if err != nil || !strings.Contains(string(brief), "Root task.") {
		t.Fatalf("brief %q %v", brief, err)
	}
}

// A reviewer that crashed leaves an incomplete review; the same tree starts
// a fresh one rather than re-reporting the crash.
func TestStartRetriesIncompleteReview(t *testing.T) {
	r := newMRig(t, Config{})
	first, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	r.waitStatus(t, first.ID, StatusRunning)
	r.term.kill("rev-1")
	r.waitStatus(t, first.ID, StatusIncomplete)
	second, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("an incomplete review was reused")
	}
}
