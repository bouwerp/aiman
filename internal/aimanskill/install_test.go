package aimanskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillDocumentsContextCLI(t *testing.T) {
	if !strings.Contains(Text, "aiman context ls") || !strings.Contains(Text, "aiman context put") || !strings.Contains(Text, "aiman context import") {
		t.Fatal("skill must document aiman context")
	}
}

func TestEnsureFileInstallsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills", "aiman", "SKILL.md")
	res, err := EnsureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionInstalled {
		t.Fatalf("action %s", res.Action)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != Text {
		t.Fatal("written content does not match embedded skill")
	}
}

func TestEnsureFileSkipsWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(Text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	res, err := EnsureFile(path)
	if err != nil {
		t.Fatalf("current file must not be rewritten: %v", err)
	}
	if res.Action != ActionCurrent {
		t.Fatalf("action %s", res.Action)
	}
}

func TestEnsureFileUpdatesStaleCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("old skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := EnsureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUpdated {
		t.Fatalf("action %s", res.Action)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != Text {
		t.Fatal("stale skill was not updated")
	}
}

func TestUserSkillFilesAreUnderHome(t *testing.T) {
	files := UserSkillFiles("/home/dev")
	if len(files) < 4 {
		t.Fatalf("want several agent skill paths, got %v", files)
	}
	for _, p := range files {
		if filepath.Dir(filepath.Dir(p)) == "/home/dev" {
			t.Fatalf("path too shallow: %s", p)
		}
		if filepath.Base(p) != "SKILL.md" {
			t.Fatalf("want SKILL.md, got %s", p)
		}
	}
}

func TestProjectSkillFilesAreUnderRoot(t *testing.T) {
	files := ProjectSkillFiles("/wt")
	want := map[string]bool{
		"/wt/.agents/skills/aiman/SKILL.md": true,
		"/wt/.claude/skills/aiman/SKILL.md": true,
	}
	found := 0
	for _, p := range files {
		if want[p] {
			found++
		}
	}
	if found != len(want) {
		t.Fatalf("missing project skill paths: %v", files)
	}
}

func TestSummarizeCountsActions(t *testing.T) {
	got := Summarize([]FileResult{
		{Action: ActionInstalled},
		{Action: ActionInstalled},
		{Action: ActionUpdated},
		{Action: ActionCurrent},
	})
	if got != "installed 2, updated 1, current 1" {
		t.Fatalf("%q", got)
	}
}

func TestEnsureOnHostInstallsUserAndExistingProjects(t *testing.T) {
	home := t.TempDir()
	live := t.TempDir()
	missing := filepath.Join(home, "no-such-worktree")
	results, err := EnsureOnHost(home, []string{live, missing, live})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no files ensured")
	}
	var sawUser, sawProject, sawMissing bool
	for _, r := range results {
		if r.Action != ActionInstalled {
			t.Fatalf("%s action %s", r.Path, r.Action)
		}
		switch {
		case hasPrefixDir(r.Path, live):
			sawProject = true
		case hasPrefixDir(r.Path, missing):
			sawMissing = true
		case hasPrefixDir(r.Path, home):
			sawUser = true
		}
	}
	if !sawUser {
		t.Fatal("user-level skill paths were not installed")
	}
	if !sawProject {
		t.Fatal("existing project was not installed")
	}
	if sawMissing {
		t.Fatal("missing worktree must be skipped")
	}
}

func hasPrefixDir(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && rel != "." && !startsWithDotDot(rel)
}

func startsWithDotDot(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}
