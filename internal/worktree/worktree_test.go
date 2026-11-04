package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture makes a main checkout with an origin, and a linked worktree on
// branch feature at the pushed commit.
func fixture(t *testing.T) (mainDir, linked string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(base, "origin.git")
	mainDir = filepath.Join(base, "repo")
	linked = filepath.Join(base, "repo@feature")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, base, "init", "--bare", "-q", origin)
	run(t, mainDir, "init", "-q", "-b", "main")
	write(t, filepath.Join(mainDir, "a.txt"), "a\n")
	run(t, mainDir, "add", ".")
	run(t, mainDir, "commit", "-qm", "init")
	run(t, mainDir, "remote", "add", "origin", origin)
	run(t, mainDir, "push", "-q", "origin", "main")
	run(t, mainDir, "worktree", "add", "-q", "-b", "feature", linked)
	return mainDir, linked
}

func TestInspectKinds(t *testing.T) {
	ctx := context.Background()
	mainDir, linked := fixture(t)
	sub := filepath.Join(linked, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(linked, symlink); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir()
	cases := []struct {
		name string
		path string
		want Kind
	}{
		{"main checkout", mainDir, KindMain},
		{"linked worktree", linked, KindLinked},
		{"linked through a symlink", symlink, KindLinked},
		{"subdirectory of a linked worktree", sub, KindOther},
		{"missing", filepath.Join(plain, "gone"), KindNone},
		{"not a repository", plain, KindNone},
		{"empty", "", KindNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Inspect(ctx, c.path).Kind; got != c.want {
				t.Fatalf("Inspect(%q).Kind = %d, want %d", c.path, got, c.want)
			}
		})
	}
	if got := Inspect(ctx, linked).Branch; got != "feature" {
		t.Fatalf("branch = %q", got)
	}
}

func TestInspectLinkedWorktreeOfBareRepository(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	base := filepath.Dir(linked)
	wt := filepath.Join(base, "bare-wt")
	run(t, filepath.Join(base, "origin.git"), "worktree", "add", "-q", "-b", "bare-branch", wt, "main")
	info := Inspect(ctx, wt)
	if info.Kind != KindLinked {
		t.Fatalf("kind = %d", info.Kind)
	}
	if err := Remove(ctx, info, false); err != nil {
		t.Fatal(err)
	}
}

func TestPendingCleanWorktree(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	loss, err := Pending(ctx, Inspect(ctx, linked))
	if err != nil {
		t.Fatal(err)
	}
	if !loss.Empty() {
		t.Fatalf("clean worktree reports loss: %+v", loss)
	}
}

func TestPendingCountsChangesAndCapsPaths(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	write(t, filepath.Join(linked, "a.txt"), "changed\n")
	for i := range MaxLossPaths + 5 {
		write(t, filepath.Join(linked, fmt.Sprintf("new%02d.txt", i)), "x")
	}
	loss, err := Pending(ctx, Inspect(ctx, linked))
	if err != nil {
		t.Fatal(err)
	}
	if loss.ChangedFiles != MaxLossPaths+6 {
		t.Fatalf("changed = %d", loss.ChangedFiles)
	}
	if len(loss.Paths) != MaxLossPaths {
		t.Fatalf("paths = %d", len(loss.Paths))
	}
}

func TestPendingCountsUnpushedCommits(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	write(t, filepath.Join(linked, "b.txt"), "b\n")
	run(t, linked, "add", ".")
	run(t, linked, "commit", "-qm", "one")
	write(t, filepath.Join(linked, "c.txt"), "c\n")
	run(t, linked, "add", ".")
	run(t, linked, "commit", "-qm", "two")
	loss, err := Pending(ctx, Inspect(ctx, linked))
	if err != nil {
		t.Fatal(err)
	}
	if loss.UnpushedCommits != 2 || loss.ChangedFiles != 0 {
		t.Fatalf("loss = %+v", loss)
	}
	run(t, linked, "push", "-q", "origin", "feature")
	loss, err = Pending(ctx, Inspect(ctx, linked))
	if err != nil {
		t.Fatal(err)
	}
	if !loss.Empty() {
		t.Fatalf("pushed commits still count: %+v", loss)
	}
}

// A real session's worktree holds these with no work in it, so they must
// neither read as dirty nor stop a removal that has no --force.
func TestScaffoldIsNotDirtyAndDoesNotBlockRemoval(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	for _, f := range []string{
		".aiman_task.md", ".aiman_handover.md", "sub/.aiman_todo.md",
		".claude/skills/aiman/SKILL.md", ".agents/skills/aiman/SKILL.md",
		".codex/skills/aiman/design-template.md", "graphify-out/cache/x.json",
	} {
		write(t, filepath.Join(linked, f), "generated")
	}
	info := Inspect(ctx, linked)
	loss, err := Pending(ctx, info)
	if err != nil {
		t.Fatal(err)
	}
	if !loss.Empty() {
		t.Fatalf("scaffold reads as dirty: %+v", loss)
	}
	if err := Remove(ctx, info, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
}

func TestUserFilesInAgentDirectoriesAreDirty(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	write(t, filepath.Join(linked, ".claude/settings.local.json"), "{}")
	write(t, filepath.Join(linked, ".claude/skills/mine/SKILL.md"), "mine")
	loss, err := Pending(ctx, Inspect(ctx, linked))
	if err != nil {
		t.Fatal(err)
	}
	if loss.ChangedFiles != 2 {
		t.Fatalf("loss = %+v", loss)
	}
}

const aimanIgnore = "\n" + domain.AimanGeneratedIgnoreComment + "\n" + domain.AimanGeneratedFileGlob + "\n"

func TestGitignoreChangedOnlyByAimanIsNotDirty(t *testing.T) {
	ctx := context.Background()
	mainDir, linked := fixture(t)
	write(t, filepath.Join(mainDir, ".gitignore"), "*.log\n")
	run(t, mainDir, "add", ".")
	run(t, mainDir, "commit", "-qm", "ignore")
	run(t, mainDir, "push", "-q", "origin", "main")
	run(t, linked, "merge", "-q", "main")
	write(t, filepath.Join(linked, ".gitignore"), "*.log\n"+aimanIgnore)
	write(t, filepath.Join(linked, ".aiman_task.md"), "task")
	info := Inspect(ctx, linked)
	loss, err := Pending(ctx, info)
	if err != nil {
		t.Fatal(err)
	}
	if !loss.Empty() {
		t.Fatalf("aiman's ignore rule reads as dirty: %+v", loss)
	}
	if err := Remove(ctx, info, false); err != nil {
		t.Fatal(err)
	}
}

func TestGitignoreCreatedByAimanDoesNotBlockRemoval(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	write(t, filepath.Join(linked, ".gitignore"), domain.AimanGeneratedIgnoreComment+"\n"+domain.AimanGeneratedFileGlob+"\n")
	write(t, filepath.Join(linked, ".aiman_task.md"), "task")
	info := Inspect(ctx, linked)
	loss, err := Pending(ctx, info)
	if err != nil {
		t.Fatal(err)
	}
	if !loss.Empty() {
		t.Fatalf("loss = %+v", loss)
	}
	if err := Remove(ctx, info, false); err != nil {
		t.Fatal(err)
	}
}

func TestGitignoreWithOtherChangesIsDirty(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	write(t, filepath.Join(linked, ".gitignore"), "secret.txt\n"+aimanIgnore)
	loss, err := Pending(ctx, Inspect(ctx, linked))
	if err != nil {
		t.Fatal(err)
	}
	if loss.ChangedFiles != 1 {
		t.Fatalf("loss = %+v", loss)
	}
}

func TestRemoveKeepsTheBranch(t *testing.T) {
	ctx := context.Background()
	mainDir, linked := fixture(t)
	write(t, filepath.Join(linked, "b.txt"), "b\n")
	run(t, linked, "add", ".")
	run(t, linked, "commit", "-qm", "work")
	if err := Remove(ctx, Inspect(ctx, linked), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	if got := run(t, mainDir, "rev-parse", "--verify", "refs/heads/feature"); got == "" {
		t.Fatal("branch deleted")
	}
	if got := run(t, mainDir, "show", "feature:b.txt"); got != "b" {
		t.Fatalf("commit lost: %q", got)
	}
}

func TestRemoveWithoutForceRefusesAChange(t *testing.T) {
	ctx := context.Background()
	_, linked := fixture(t)
	write(t, filepath.Join(linked, "a.txt"), "changed\n")
	if err := Remove(ctx, Inspect(ctx, linked), false); err == nil {
		t.Fatal("removed a dirty worktree without force")
	}
	if got, _ := os.ReadFile(filepath.Join(linked, "a.txt")); string(got) != "changed\n" {
		t.Fatalf("change lost: %q", got)
	}
}

func TestRemoveWithForceDiscards(t *testing.T) {
	ctx := context.Background()
	mainDir, linked := fixture(t)
	write(t, filepath.Join(linked, "a.txt"), "changed\n")
	if err := Remove(ctx, Inspect(ctx, linked), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(linked); !os.IsNotExist(err) {
		t.Fatalf("worktree still there: %v", err)
	}
	run(t, mainDir, "rev-parse", "--verify", "refs/heads/feature")
}

func TestRemoveLeavesLockedWorktree(t *testing.T) {
	ctx := context.Background()
	mainDir, linked := fixture(t)
	run(t, mainDir, "worktree", "lock", linked)
	if err := Remove(ctx, Inspect(ctx, linked), true); err == nil {
		t.Fatal("removed a locked worktree")
	}
	if _, err := os.Stat(linked); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRefusesEverythingButLinked(t *testing.T) {
	ctx := context.Background()
	mainDir, linked := fixture(t)
	sub := filepath.Join(linked, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mainDir, sub, t.TempDir()} {
		for _, force := range []bool{false, true} {
			if err := Remove(ctx, Inspect(ctx, p), force); err == nil {
				t.Fatalf("Remove(%q, force=%v) succeeded", p, force)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(mainDir, "a.txt")); err != nil {
		t.Fatalf("main checkout damaged: %v", err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatalf("subdirectory removed: %v", err)
	}
}

func TestParseStatusRename(t *testing.T) {
	got := parseStatus("R  new name\x00old name\x00?? u\x00 M m\x00")
	want := []entry{{"R ", "new name"}, {"??", "u"}, {" M", "m"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%+v", got)
		}
	}
}
