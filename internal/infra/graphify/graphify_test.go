package graphify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/local"
)

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

// newRepo makes a committed repository with no ignore rules and points HOME
// at an empty directory, so the only graphify on PATH is one the test
// installs into $HOME/.local/bin.
func newRepo(t *testing.T) (repo, home string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	repo = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")
	return repo, home
}

// installFakeGraphify writes a graphify that sleeps, then writes
// graphify-out/graph.json in its working directory.
func installFakeGraphify(t *testing.T, home string, sleep int) {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nsleep " + strconv.Itoa(sleep) + "\nmkdir -p graphify-out\necho '{}' > graphify-out/graph.json\n"
	if err := os.WriteFile(filepath.Join(bin, "graphify"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s was not written within 10s", path)
}

func TestGraphifyScriptRunsDetached(t *testing.T) {
	repo, home := newRepo(t)
	installFakeGraphify(t, home, 5)
	start := time.Now()
	if out, err := local.NewExecutor("").Execute(context.Background(), BackgroundUpdateScript(repo)); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("script waited %s for graphify; it must run detached", d)
	}
}

func TestGraphifyScriptExcludesOutput(t *testing.T) {
	repo, home := newRepo(t)
	installFakeGraphify(t, home, 0)
	if out, err := local.NewExecutor("").Execute(context.Background(), BackgroundUpdateScript(repo)); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	waitForFile(t, filepath.Join(repo, "graphify-out", "graph.json"))
	if st := runGit(t, repo, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Fatalf("graphify left untracked output:\n%s", st)
	}
}

func TestGraphifyScriptNoopWithoutBinary(t *testing.T) {
	repo, _ := newRepo(t)
	if out, err := local.NewExecutor("").Execute(context.Background(), BackgroundUpdateScript(repo)); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(repo, "graphify-out")); !os.IsNotExist(err) {
		t.Fatalf("graphify-out exists without a graphify binary (stat err %v)", err)
	}
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if strings.Contains(string(exclude), "graphify-out") {
		t.Fatalf("exclude gained a graphify-out line without a graphify binary:\n%s", exclude)
	}
}

func TestAvailable(t *testing.T) {
	_, home := newRepo(t)
	ex := local.NewExecutor("")
	if Available(context.Background(), ex) {
		t.Fatal("Available = true with no graphify on PATH")
	}
	installFakeGraphify(t, home, 0)
	if !Available(context.Background(), ex) {
		t.Fatal("Available = false with graphify in $HOME/.local/bin")
	}
}
