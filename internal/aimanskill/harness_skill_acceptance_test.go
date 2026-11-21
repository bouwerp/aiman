package aimanskill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessSkillListOrderAndFiles(t *testing.T) {
	skills := Skills()
	if len(skills) != 2 {
		t.Fatalf("want 2 skills, got %d", len(skills))
	}
	if skills[0].Name != "aiman" || skills[1].Name != "aiman-harness" {
		t.Fatalf("skill order: %q, %q", skills[0].Name, skills[1].Name)
	}
	bundle := Bundle()
	if len(skills[0].Files) != len(bundle) {
		t.Fatalf("aiman files %d, bundle %d", len(skills[0].Files), len(bundle))
	}
	for i, f := range bundle {
		if skills[0].Files[i] != f {
			t.Fatalf("aiman file %d is %q, want %q", i, skills[0].Files[i].Name, f.Name)
		}
	}
	want := []BundleFile{{Name: "SKILL.md", Content: HarnessSkillText}, {Name: "harness.md", Content: Harness}}
	got := skills[1].Files
	if len(got) != len(want) {
		t.Fatalf("aiman-harness files: %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("aiman-harness file %d is %q, want %q", i, got[i].Name, want[i].Name)
		}
	}
}

func TestHarnessSkillBundleUnchanged(t *testing.T) {
	b := Bundle()
	if len(b) != 3 || b[0].Name != "SKILL.md" || b[0].Content != Text || b[1].Name != TemplateName || b[2].Name != HarnessName {
		t.Fatalf("aiman bundle changed: %+v", b)
	}
}

func TestHarnessSkillTextFrontmatter(t *testing.T) {
	if !strings.HasPrefix(HarnessSkillText, "---\n") {
		t.Fatal("harness skill has no frontmatter")
	}
	end := strings.Index(HarnessSkillText[4:], "\n---")
	if end < 0 {
		t.Fatal("harness skill frontmatter is not closed")
	}
	front := HarnessSkillText[4 : 4+end]
	var name, desc string
	for _, line := range strings.Split(front, "\n") {
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			name = strings.Trim(strings.TrimSpace(v), `"'`)
		}
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			desc = v
		}
	}
	if name != "aiman-harness" {
		t.Fatalf("frontmatter name %q", name)
	}
	if !strings.Contains(strings.ToLower(desc), "harness") {
		t.Fatalf("description does not mention the harness: %q", desc)
	}
	if !strings.Contains(desc, "AIMAN_ENV=1") {
		t.Fatalf("description does not mention AIMAN_ENV=1: %q", desc)
	}
}

func TestHarnessSkillTextBody(t *testing.T) {
	for _, want := range []string{
		`test "${AIMAN_ENV:-}" = 1`,
		"aiman harness list --dirty",
		"aiman harness up",
		"aiman harness check",
		"aiman harness share",
		"harness.md",
	} {
		if !strings.Contains(HarnessSkillText, want) {
			t.Fatalf("harness skill missing %q", want)
		}
	}
}

func TestHarnessSkillEnsureOnHostInstallsUserAndProject(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if _, err := EnsureOnHost(home, []string{root}); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{home, root} {
		dir := filepath.Join(base, ".claude", "skills", "aiman-harness")
		for name, want := range map[string]string{"SKILL.md": HarnessSkillText, "harness.md": Harness} {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("%s: %v", filepath.Join(dir, name), err)
			}
			if string(got) != want {
				t.Fatalf("%s content differs from the embedded file", filepath.Join(dir, name))
			}
		}
		// The aiman skill stays installed beside it.
		got, err := os.ReadFile(filepath.Join(base, ".claude", "skills", "aiman", "SKILL.md"))
		if err != nil || string(got) != Text {
			t.Fatalf("aiman SKILL.md under %s: %v", base, err)
		}
	}
}

func TestHarnessSkillEnsureOnHostIsIdempotent(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	if _, err := EnsureOnHost(home, []string{root}); err != nil {
		t.Fatal(err)
	}
	results, err := EnsureOnHost(home, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	var sawHarness bool
	for _, r := range results {
		if r.Action != ActionCurrent {
			t.Fatalf("%s action %s on second run", r.Path, r.Action)
		}
		if strings.Contains(filepath.ToSlash(r.Path), "/skills/aiman-harness/") {
			sawHarness = true
		}
	}
	if !sawHarness {
		t.Fatal("second run did not report the aiman-harness files")
	}
}

func TestHarnessSkillIsProjectSkillPath(t *testing.T) {
	for rel, want := range map[string]bool{
		".claude/skills/aiman-harness/SKILL.md":  true,
		".codex/skills/aiman-harness/harness.md": true,
		".claude/skills/aiman/SKILL.md":          true,
		".claude/skills/aiman/harness.md":        true,
		".claude/skills/other/SKILL.md":          false,
		".claude/skills/aiman-harness/notes.md":  false,
		"src/main.go":                            false,
	} {
		if got := IsProjectSkillPath(rel); got != want {
			t.Errorf("IsProjectSkillPath(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestHarnessSkillDirs(t *testing.T) {
	user := UserSkillDirs("/home/dev", "aiman-harness")
	if !containsStr(user, filepath.Join("/home/dev", ".claude", "skills", "aiman-harness")) {
		t.Fatalf("user dirs: %v", user)
	}
	if len(user) != len(UserSkillFiles("/home/dev")) {
		t.Fatalf("user dirs %d, user skill files %d", len(user), len(UserSkillFiles("/home/dev")))
	}
	proj := ProjectSkillDirs("/wt", "aiman-harness")
	if !containsStr(proj, "/wt/.claude/skills/aiman-harness") {
		t.Fatalf("project dirs: %v", proj)
	}
	if len(proj) != len(ProjectSkillFiles("/wt")) {
		t.Fatalf("project dirs %d, project skill files %d", len(proj), len(ProjectSkillFiles("/wt")))
	}
	// The aiman skill's file lists keep their meaning.
	for _, p := range ProjectSkillFiles("/wt") {
		if !strings.HasSuffix(p, "/skills/aiman/SKILL.md") {
			t.Fatalf("ProjectSkillFiles changed: %s", p)
		}
	}
}

func TestHarnessSkillAimanSectionNamesIt(t *testing.T) {
	_, after, ok := strings.Cut(Text, "\n## Harness\n")
	if !ok {
		t.Fatal("aiman skill has no ## Harness section")
	}
	if i := strings.Index(after, "\n## "); i >= 0 {
		after = after[:i]
	}
	if !strings.Contains(after, "aiman-harness") {
		t.Fatal("aiman skill's Harness section does not name the aiman-harness skill")
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
