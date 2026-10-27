package usecase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
)

// countingExecutor runs commands under real bash and counts them.
type countingExecutor struct {
	*local.Executor
	calls int
}

func (c *countingExecutor) Execute(ctx context.Context, cmd string) (string, error) {
	c.calls++
	return c.Executor.Execute(ctx, cmd)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newBootstrapRepo makes a main checkout with one commit and a linked
// worktree beside it. gitignore is committed when non-empty.
func newBootstrapRepo(t *testing.T, gitignore string) (mainRepo, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	mainRepo = filepath.Join(root, "repo")
	worktree = filepath.Join(root, "repo@feature")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRepo, "init", "-q")
	writeFile(t, filepath.Join(mainRepo, "README.md"), "hello\n")
	if gitignore != "" {
		writeFile(t, filepath.Join(mainRepo, ".gitignore"), gitignore)
	}
	runGit(t, mainRepo, "add", "-A")
	runGit(t, mainRepo, "commit", "-q", "-m", "init")
	runGit(t, mainRepo, "worktree", "add", "-q", "-b", "feature", worktree)
	return mainRepo, worktree
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapCopiesAndLinks(t *testing.T) {
	mainRepo, wt := newBootstrapRepo(t, ".env.local\nnode_modules/\n")
	writeFile(t, filepath.Join(mainRepo, ".env.local"), "SECRET=1\n")
	writeFile(t, filepath.Join(mainRepo, "node_modules", "pkg", "index.js"), "x\n")

	res, err := ApplyWorktreeBootstrap(context.Background(), local.NewExecutor(""), mainRepo, wt,
		config.RepoBootstrap{Copy: []string{".env.local"}, Symlink: []string{"node_modules"}})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !slices.Equal(res.Copied, []string{".env.local"}) || !slices.Equal(res.Linked, []string{"node_modules"}) {
		t.Fatalf("result = %+v", res)
	}
	if res.SetupLog != "" {
		t.Fatalf("SetupLog = %q without setup", res.SetupLog)
	}

	fi, err := os.Lstat(filepath.Join(wt, ".env.local"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal(".env.local should be a copy, not a symlink")
	}
	body, _ := os.ReadFile(filepath.Join(wt, ".env.local"))
	if string(body) != "SECRET=1\n" {
		t.Fatalf(".env.local = %q", body)
	}

	target, err := os.Readlink(filepath.Join(wt, "node_modules"))
	if err != nil {
		t.Fatalf("node_modules is not a symlink: %v", err)
	}
	if target != filepath.Join(mainRepo, "node_modules") {
		t.Fatalf("node_modules -> %q", target)
	}
}

func TestBootstrapSkipsMissingAndExisting(t *testing.T) {
	mainRepo, wt := newBootstrapRepo(t, ".env.local\n")
	writeFile(t, filepath.Join(mainRepo, ".env.local"), "MAIN=1\n")
	writeFile(t, filepath.Join(wt, ".env.local"), "MINE=1\n")

	res, err := ApplyWorktreeBootstrap(context.Background(), local.NewExecutor(""), mainRepo, wt,
		config.RepoBootstrap{Copy: []string{".env.local", "missing.txt"}, Symlink: []string{"absent"}})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if len(res.Copied) != 0 || len(res.Linked) != 0 {
		t.Fatalf("nothing should be applied: %+v", res)
	}
	if !slices.Equal(res.Skipped, []string{".env.local", "missing.txt", "absent"}) {
		t.Fatalf("Skipped = %v", res.Skipped)
	}
	body, _ := os.ReadFile(filepath.Join(wt, ".env.local"))
	if string(body) != "MINE=1\n" {
		t.Fatalf("existing destination overwritten: %q", body)
	}
	if _, err := os.Lstat(filepath.Join(wt, "absent")); !os.IsNotExist(err) {
		t.Fatalf("missing source must not create a link: %v", err)
	}
}

func TestBootstrapSymlinkIsExcludedFromGit(t *testing.T) {
	mainRepo, wt := newBootstrapRepo(t, "node_modules/\n")
	writeFile(t, filepath.Join(mainRepo, "node_modules", "pkg", "index.js"), "x\n")

	b := config.RepoBootstrap{Symlink: []string{"node_modules"}}
	for range 2 {
		if _, err := ApplyWorktreeBootstrap(context.Background(), local.NewExecutor(""), mainRepo, wt, b); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
	}
	if out := runGit(t, wt, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("worktree status not clean:\n%s", out)
	}
	exclude, err := os.ReadFile(filepath.Join(mainRepo, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(exclude), "/node_modules\n"); n != 1 {
		t.Fatalf("exclude holds /node_modules %d times:\n%s", n, exclude)
	}
}

func TestBootstrapSetupReturnsImmediately(t *testing.T) {
	mainRepo, wt := newBootstrapRepo(t, "")

	start := time.Now()
	res, err := ApplyWorktreeBootstrap(context.Background(), local.NewExecutor(""), mainRepo, wt,
		config.RepoBootstrap{Setup: "sleep 5; echo done"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("Execute took %s; setup must run detached", elapsed)
	}

	gitDir := strings.TrimSpace(runGit(t, wt, "rev-parse", "--absolute-git-dir"))
	if res.SetupLog != filepath.Join(gitDir, "aiman-setup.log") {
		t.Fatalf("SetupLog = %q, want under %q", res.SetupLog, gitDir)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		body, _ := os.ReadFile(res.SetupLog)
		if strings.Contains(string(body), "done") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup log never said done: %q", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if out := runGit(t, wt, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("setup log must not show in git status:\n%s", out)
	}
}

func TestBootstrapRejectsEscapingPaths(t *testing.T) {
	rec := &countingExecutor{Executor: local.NewExecutor("")}
	for _, b := range []config.RepoBootstrap{
		{Copy: []string{"../x"}},
		{Symlink: []string{"/etc"}},
	} {
		if _, err := ApplyWorktreeBootstrap(context.Background(), rec, "/main", "/wt", b); err == nil {
			t.Errorf("%+v: expected error", b)
		}
	}
	if rec.calls != 0 {
		t.Fatalf("Execute called %d times for invalid config", rec.calls)
	}
}

func TestBootstrapScriptQuotesPaths(t *testing.T) {
	script := bootstrapScript("/m'ain", "/w t", config.RepoBootstrap{Copy: []string{"a b"}})
	for _, want := range []string{`'/m'\''ain'`, `'/w t'`, `'a b'`} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %s:\n%s", want, script)
		}
	}
}
