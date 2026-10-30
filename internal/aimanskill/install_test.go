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
	if !strings.Contains(Text, "aiman slack send") || !strings.Contains(Text, "aiman slack wait") {
		t.Fatal("skill must document aiman slack")
	}
}

func TestSkillDocumentsTodoCLI(t *testing.T) {
	for _, span := range []string{"aiman todo list", "aiman todo done", "[aiman]", ".aiman_todo.md"} {
		if !strings.Contains(Text, span) {
			t.Fatalf("skill missing %q", span)
		}
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
	found := map[string]bool{}
	for _, p := range files {
		found[p] = true
	}
	for _, p := range []string{
		"/home/dev/.config/muse/skills/aiman/SKILL.md",
		"/home/dev/.deepcode/skills/aiman/SKILL.md",
	} {
		if !found[p] {
			t.Fatalf("missing %s in %v", p, files)
		}
	}
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
		"/wt/.agents/skills/aiman/SKILL.md":   true,
		"/wt/.claude/skills/aiman/SKILL.md":   true,
		"/wt/.deepcode/skills/aiman/SKILL.md": true,
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

func TestSkillDescribesVerify(t *testing.T) {
	for _, want := range []string{"aiman verify --wait --if-enabled", "gh pr create"} {
		if !strings.Contains(Text, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestSkillDescribesReviewMode(t *testing.T) {
	for _, want := range []string{
		"## Review mode",
		"aiman review reveal",
		"aiman review post",
		"approve-review",
		"blocking",
		"suggestion",
		"question",
		"nit",
	} {
		if !strings.Contains(Text, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestSkillStatesChildContract(t *testing.T) {
	for _, want := range []string{
		"--brief-file", "## Objective", "## Contract", "## Owns", "## Out of scope",
		"## Acceptance tests", "## Report must contain", "## Escalate when",
		".aiman_report.md", "## Evidence", "## Surprises",
	} {
		if !strings.Contains(Text, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestSkillStatesCoordinator(t *testing.T) {
	for _, want := range []string{"coordinator", "simple, short-lived", "child Aiman session", "--tier", "worker` (the default)", "docs/specs/", "do not spawn a child", "vendor subagent", "Leave a delegated item pending"} {
		if !strings.Contains(Text, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
	for _, forbidden := range []string{"fan-out key", "Spec wizard", "picks from `agents.preferred`"} {
		if strings.Contains(Text, forbidden) {
			t.Fatalf("skill must not contain %q", forbidden)
		}
	}
}

func TestSkillDescriptionCoversCoordinator(t *testing.T) {
	head := Text
	if i := strings.Index(head, "---\n\n# Aiman"); i > 0 {
		head = head[:i]
	}
	if !strings.Contains(head, "coordinate work") || !strings.Contains(head, "child sessions") {
		t.Fatalf("skill description does not load for coordinator work: %q", head)
	}
}

func TestSkillStatesDoneWhen(t *testing.T) {
	for _, want := range []string{
		"The last item, `Done-when met`, belongs to aiman.",
		"Tick it only when every condition in the \"Done when\" section of `.aiman_task.md` holds.",
		"work through each checklist",
	} {
		if !strings.Contains(Text, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestEnsureOnHostInstallsTemplate(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	if Template == "" {
		t.Fatal("design template is not embedded")
	}
	if _, err := EnsureOnHost(home, []string{project}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(home, ".claude", "skills", "aiman"),
		filepath.Join(project, ".agents", "skills", "aiman"),
	} {
		for name, want := range map[string]string{"SKILL.md": Text, TemplateName: Template} {
			got, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("%s/%s does not match the embed", dir, name)
			}
		}
	}
	again, err := EnsureOnHost(home, []string{project})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Action != ActionCurrent {
			t.Fatalf("second run: %s %s", r.Path, r.Action)
		}
	}
	if len(again) != 2*(len(UserSkillFiles(home))+len(ProjectSkillFiles(project))) {
		t.Fatalf("second run ensured %d files", len(again))
	}
}

func TestSkillStatesDesignFirst(t *testing.T) {
	design := strings.Index(Text, "## Design first")
	coord := strings.Index(Text, "## Coordinator")
	if design < 0 || coord < 0 || design > coord {
		t.Fatal("## Design first must come before ## Coordinator")
	}
	for _, want := range []string{
		"**Trivial**", "**Small**", "**Project**", TemplateName,
		"aiman design publish", "aiman todo gate", "aiman todo approve-design --quote",
		"session create --consult", "## Question", "## Scope", "## Objections", "## Verdict",
		"`[high]`", "`approve`", "consult_needs_smart", "gate_needs_operator", "design_not_approved",
		"| Objection | Severity | Triage | Reason |",
		"| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |",
		"at most 4 children", "Never approve on a consult verdict", "as built", "## What changed", "## Files",
	} {
		if !strings.Contains(Text, want) {
			t.Fatalf("skill missing %q", want)
		}
	}
}

func TestTemplateSectionsInReadingOrder(t *testing.T) {
	last := -1
	for _, h := range []string{
		"## The problem", "## How it works today", "## Glossary", "## Proposed design",
		"## A scenario, end to end", "## Alternatives considered", "## Open questions",
		"## Decisions", "## Breakdown", "## What you should be able to explain after reading",
	} {
		i := strings.Index(Template, h)
		if i <= last {
			t.Fatalf("template section %q missing or out of order", h)
		}
		last = i
	}
	for _, want := range []string{
		"classDef new fill:#d4f7d4", "classDef changed fill:#fde9b8", "classDef removed stroke-dasharray:4,fill:#f9d0d0",
		"| Objection | Severity | Triage | Reason |",
		"| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |",
	} {
		if !strings.Contains(Template, want) {
			t.Fatalf("template missing %q", want)
		}
	}
	if strings.Contains(Text+Template, "—") {
		t.Fatal("skill or template contains an em-dash")
	}
}

// section returns the text of the "## " section starting at heading, up to
// the next "## " heading.
func section(t *testing.T, heading string) string {
	t.Helper()
	start := strings.Index(Text, heading+"\n")
	if start < 0 {
		t.Fatalf("skill has no %q", heading)
	}
	rest := Text[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestSkillStatesAutonomousDesign(t *testing.T) {
	design := section(t, "## Design first")
	for _, want := range []string{
		"### Autonomous sessions", "no gate", "another model family", "stands in for approval",
		"high-severity objection", "second", "stop and wait for input", "notification", "issue comment",
	} {
		if !strings.Contains(design, want) {
			t.Fatalf("Design first missing %q", want)
		}
	}
}

// Children skip Design first, so escalation lives in its own section.
func TestSkillEscalationReachesChildren(t *testing.T) {
	if strings.Contains(section(t, "## Design first"), "## Escalating") {
		t.Fatal("escalation is inside Design first, which children skip")
	}
	esc := section(t, "## Escalating (children too)")
	for _, want := range []string{
		`aiman todo add "<question>"`, "aiman session create --consult --for <todo-id> --brief-file",
		"## Question", "## Scope", "## Report must contain", "[aiman]", "harder than",
	} {
		if !strings.Contains(esc, want) {
			t.Fatalf("Escalating missing %q", want)
		}
	}
}
