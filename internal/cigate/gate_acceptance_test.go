package cigate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cigate"
)

// ci-minutes rev 3, piece A: the local gate runs the repo's list against
// HEAD in a snapshot and keeps one record per session and commit.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

// repo makes a worktree whose gate list is gate (YAML), committed.
func repo(t *testing.T, gate string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.name", "Aiman Test")
	git(t, dir, "config", "user.email", "aiman@example.com")
	write(t, dir, ".gitignore", ".env.local\n")
	write(t, dir, "a.txt", "a\n")
	if gate != "" {
		write(t, dir, ".aiman/ci-local.yaml", gate)
	}
	commit(t, dir, "init")
	return dir
}

func runner(t *testing.T) *cigate.Runner {
	t.Helper()
	return &cigate.Runner{Dir: filepath.Join(t.TempDir(), "ci-local"), MaxParallel: 2}
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestParseList(t *testing.T) {
	l, err := cigate.ParseList([]byte("setup: npm ci\ncommands:\n  - name: lint\n    run: npm run lint\n  - name: test\n    run: go test ./...\n    dir: api\n    timeout: 10m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Setup != "npm ci" || len(l.Commands) != 2 {
		t.Fatalf("list = %+v", l)
	}
	if l.Commands[0].Timeout != 45*time.Minute || l.Commands[1].Timeout != 10*time.Minute || l.Commands[1].Dir != "api" {
		t.Fatalf("commands = %+v, want default 45m and the given 10m", l.Commands)
	}
	for _, bad := range []string{
		"commands:\n  - name: x\n    run: \"\"\n",
		"commands:\n  - name: x\n    run: ls\n    dir: /etc\n",
		"commands:\n  - name: x\n    run: ls\n    dir: ../up\n",
		"commands:\n  - run: ls\n",
	} {
		if _, err := cigate.ParseList([]byte(bad)); err == nil {
			t.Errorf("ParseList accepted %q", bad)
		}
	}
}

func TestLoadListReadsTheCommitNotTheWorkingTree(t *testing.T) {
	dir := repo(t, "commands:\n  - name: one\n    run: \"true\"\n")
	head := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, ".aiman/ci-local.yaml", "commands:\n  - name: edited\n    run: \"true\"\n")
	l, ok, err := cigate.LoadList(ctx(t), dir, head, nil)
	if err != nil || !ok || len(l.Commands) != 1 || l.Commands[0].Name != "one" {
		t.Fatalf("LoadList = %+v, %v, %v; want the committed list", l, ok, err)
	}
	override := &cigate.List{Commands: []cigate.Command{{Name: "mine", Run: "true", Timeout: time.Minute}}}
	if l, ok, _ := cigate.LoadList(ctx(t), dir, head, override); !ok || l.Commands[0].Name != "mine" {
		t.Fatalf("override ignored: %+v", l)
	}
	bare := repo(t, "")
	if _, ok, err := cigate.LoadList(ctx(t), bare, git(t, bare, "rev-parse", "HEAD"), nil); ok || err != nil {
		t.Fatalf("a repo without a list: ok=%v err=%v, want no list and no error", ok, err)
	}
}

func TestRunPassesAndRecordsByCommit(t *testing.T) {
	dir := repo(t, "commands:\n  - name: check\n    run: test -f a.txt && echo checked\n")
	head := git(t, dir, "rev-parse", "HEAD")
	r := runner(t)
	rec, err := r.Run(ctx(t), "s1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != cigate.StatusPass || rec.SHA != head || rec.Session != "s1" {
		t.Fatalf("record = %+v, want pass for %s", rec, head)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "s1", head+".json")); err != nil {
		t.Fatalf("record file: %v", err)
	}
	got, ok, err := r.Record("s1", head)
	if err != nil || !ok || got.Status != cigate.StatusPass {
		t.Fatalf("Record = %+v, %v, %v", got, ok, err)
	}
	if len(got.Commands) != 1 || got.Commands[0].Log == "" {
		t.Fatalf("commands = %+v, want one with a log path", got.Commands)
	}
	if b, _ := os.ReadFile(got.Commands[0].Log); !strings.Contains(string(b), "checked") {
		t.Fatalf("log %s lacks the command's output: %q", got.Commands[0].Log, b)
	}
}

func TestRunStopsAtTheFirstFailure(t *testing.T) {
	dir := repo(t, "commands:\n  - name: first\n    run: exit 3\n  - name: second\n    run: \"true\"\n")
	rec, err := runner(t).Run(ctx(t), "s1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != cigate.StatusFail || rec.Failed != "first" || len(rec.Commands) != 1 || rec.Commands[0].Exit != 3 {
		t.Fatalf("record = %+v, want fail at first with exit 3 and second never run", rec)
	}
}

func TestRunRefusesADirtyTree(t *testing.T) {
	dir := repo(t, "commands:\n  - name: c\n    run: \"true\"\n")
	write(t, dir, "a.txt", "changed\n")
	if _, err := runner(t).Run(ctx(t), "s1", dir); !errors.Is(err, cigate.ErrDirty) {
		t.Fatalf("err = %v, want ErrDirty", err)
	}
	dir2 := repo(t, "commands:\n  - name: c\n    run: \"true\"\n")
	write(t, dir2, "new.txt", "untracked\n")
	if _, err := runner(t).Run(ctx(t), "s1", dir2); !errors.Is(err, cigate.ErrDirty) {
		t.Fatalf("untracked file: err = %v, want ErrDirty", err)
	}
}

func TestRunUsesARemovedSnapshotWithCopiesOnly(t *testing.T) {
	out := filepath.Join(t.TempDir(), "where")
	dir := repo(t, "commands:\n  - name: where\n    run: pwd > "+out+" && cat .env.local >> "+out+"\n")
	write(t, dir, ".env.local", "SECRET_SHAPE=1\n") // ignored, so the tree is clean
	r := runner(t)
	r.Copy = []string{".env.local"}
	rec, err := r.Run(ctx(t), "s1", dir)
	if err != nil || rec.Status != cigate.StatusPass {
		t.Fatalf("Run = %+v, %v", rec, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	snap := lines[0]
	if snap == dir || strings.HasPrefix(snap, dir+string(os.PathSeparator)) {
		t.Fatalf("command ran in the worktree %s, want a snapshot", snap)
	}
	if len(lines) < 2 || lines[1] != "SECRET_SHAPE=1" {
		t.Fatalf("the copy file did not reach the snapshot: %q", lines)
	}
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot %s still exists after the run", snap)
	}
}

func TestOneRunPerSession(t *testing.T) {
	dir := repo(t, "commands:\n  - name: slow\n    run: sleep 2\n")
	r := runner(t)
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx(t), "s1", dir); done <- err }()
	time.Sleep(500 * time.Millisecond)
	if _, err := r.Run(ctx(t), "s1", dir); !errors.Is(err, cigate.ErrRunning) {
		t.Fatalf("second run: err = %v, want ErrRunning", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRecordsPruneToFivePlusHead(t *testing.T) {
	dir := repo(t, "commands:\n  - name: c\n    run: \"true\"\n")
	r := runner(t)
	var shas []string
	for i := range 7 {
		write(t, dir, "n.txt", strings.Repeat("x", i+1))
		shas = append(shas, commit(t, dir, "c"))
		if _, err := r.Run(ctx(t), "s1", dir); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := filepath.Glob(filepath.Join(r.Dir, "s1", "*.json"))
	if len(entries) > 6 {
		t.Fatalf("%d records kept, want at most 6 (newest 5 plus HEAD)", len(entries))
	}
	if _, ok, _ := r.Record("s1", shas[len(shas)-1]); !ok {
		t.Fatal("the record for HEAD was pruned")
	}
	if _, ok, _ := r.Record("s1", shas[0]); ok {
		t.Fatal("the oldest record was kept")
	}
}
