package usecase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
)

// writeCountingExecutor runs commands under real bash and counts WriteFile.
type writeCountingExecutor struct {
	*local.Executor
	writes int
}

func (w *writeCountingExecutor) WriteFile(ctx context.Context, path string, content []byte) error {
	w.writes++
	return w.Executor.WriteFile(ctx, path, content)
}

// kitRepo makes a git repository with the given files and a temp $HOME.
func kitRepo(t *testing.T, files map[string]string) (wt, home string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	wt = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt, "init", "-q")
	for p, c := range files {
		writeFile(t, filepath.Join(wt, p), c)
	}
	return wt, home
}

func checklistNames(lists []domain.Checklist) []string {
	var out []string
	for _, c := range lists {
		out = append(out, c.Name+"("+c.Source+")")
	}
	return out
}

func TestBuildQualityKitMatchesAndOrders(t *testing.T) {
	wt, home := kitRepo(t, map[string]string{
		"a.go":                    "package a\n",
		".aiman/checklists/go.md": "---\napplies_to: \"*.go\"\n---\n- vet\n",
	})
	writeFile(t, filepath.Join(home, ".aiman/checklists/tf.md"), "---\napplies_to: [\"*.tf\"]\n---\n- plan\n")
	writeFile(t, filepath.Join(home, ".aiman/checklists/general.md"), "- think\n")

	kit, notes, err := BuildQualityKit(context.Background(), local.NewExecutor(""), KitInput{Repo: "o/r", Worktree: wt})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes %v", notes)
	}
	got := checklistNames(kit.Checklists)
	want := []string{"go(repo)", "general(user)"}
	if !slices.Equal(got, want) {
		t.Fatalf("checklists %v, want %v", got, want)
	}
	if kit.Checklists[0].Path != filepath.Join(wt, ".aiman/checklists/go.md") {
		t.Fatalf("path %q", kit.Checklists[0].Path)
	}
	if kit.Repo != "o/r" {
		t.Fatalf("repo %q", kit.Repo)
	}
}

func TestBuildQualityKitDoneWhen(t *testing.T) {
	wt, _ := kitRepo(t, nil)
	in := KitInput{
		Repo:     "o/r",
		Worktree: wt,
		Issue:    &domain.Issue{Key: "P-1", AcceptanceCriteria: []string{"A"}},
		RepoCfg:  config.RepoBootstrap{DoneWhen: []string{"B"}},
	}
	kit, _, err := BuildQualityKit(context.Background(), local.NewExecutor(""), in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kit.DoneWhen, []string{"A", "B"}) {
		t.Fatalf("done-when %v", kit.DoneWhen)
	}
	in.Restart = true
	kit, _, err = BuildQualityKit(context.Background(), local.NewExecutor(""), in)
	if err != nil {
		t.Fatal(err)
	}
	if kit.DoneWhen != nil {
		t.Fatalf("restart done-when %v, want nil", kit.DoneWhen)
	}
}

func TestBuildQualityKitSkipsBadChecklist(t *testing.T) {
	wt, _ := kitRepo(t, map[string]string{
		".aiman/checklists/bad.md":  "---\napplies_to: [\n",
		".aiman/checklists/good.md": "- ok\n",
	})
	kit, notes, err := BuildQualityKit(context.Background(), local.NewExecutor(""), KitInput{Repo: "o/r", Worktree: wt})
	if err != nil {
		t.Fatal(err)
	}
	if got := checklistNames(kit.Checklists); !slices.Equal(got, []string{"good(repo)"}) {
		t.Fatalf("checklists %v", got)
	}
	bad := filepath.Join(wt, ".aiman/checklists/bad.md")
	if len(notes) != 1 || !strings.Contains(notes[0], bad) {
		t.Fatalf("notes %v, want one naming %s", notes, bad)
	}
}

func TestCheckRequires(t *testing.T) {
	wt, home := kitRepo(t, nil)
	writeFile(t, filepath.Join(home, ".local/bin/gh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(home, ".local/bin/gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"atlassian":{}}}`)
	missing, err := CheckRequires(context.Background(), local.NewExecutor(""), wt, "claude --dangerously-skip-permissions",
		[]string{"gh", "nope-aiman-test", "mcp:atlassian", "mcp:slack"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nope-aiman-test", "mcp:slack"}; !slices.Equal(missing, want) {
		t.Fatalf("missing %v, want %v", missing, want)
	}

	writeFile(t, filepath.Join(home, ".codex/config.toml"), "[mcp_servers.linear]\ncommand = \"x\"\n")
	missing, err = CheckRequires(context.Background(), local.NewExecutor(""), wt, "codex", []string{"mcp:linear", "mcp:atlassian"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mcp:atlassian"}; !slices.Equal(missing, want) {
		t.Fatalf("codex missing %v, want %v", missing, want)
	}
}

func TestCheckRequiresUnknownAgentMCP(t *testing.T) {
	wt, _ := kitRepo(t, nil)
	missing, err := CheckRequires(context.Background(), local.NewExecutor(""), wt, "kilo --auto", []string{"mcp:x"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mcp:x (cannot check for this agent)"}; !slices.Equal(missing, want) {
		t.Fatalf("missing %v, want %v", missing, want)
	}
}

func TestPushUserChecklistsWritesOnlyChanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	localDir := t.TempDir()
	writeFile(t, filepath.Join(localDir, "go.md"), "- vet\n")
	writeFile(t, filepath.Join(localDir, "notes.txt"), "ignored\n")
	ex := &writeCountingExecutor{Executor: local.NewExecutor("")}

	n, err := PushUserChecklists(context.Background(), ex, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || ex.writes != 1 {
		t.Fatalf("first push n=%d writes=%d, want 1", n, ex.writes)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".aiman/checklists/go.md")); err != nil || string(b) != "- vet\n" {
		t.Fatalf("pushed %q, %v", b, err)
	}

	ex.writes = 0
	if n, err = PushUserChecklists(context.Background(), ex, localDir); err != nil {
		t.Fatal(err)
	}
	if n != 0 || ex.writes != 0 {
		t.Fatalf("second push n=%d writes=%d, want 0", n, ex.writes)
	}
}

type recordingDoneItems struct{ ids []string }

func (r *recordingDoneItems) Ensure(_ context.Context, _ domain.RemoteExecutor, s domain.Session) error {
	r.ids = append(r.ids, s.ID)
	return nil
}

func (r *recordingDoneItems) MarkMet(context.Context, domain.Session) error { return nil }

func TestFinaliseCreateEnsuresDoneItemOnlyWithConditions(t *testing.T) {
	for _, tc := range []struct {
		name string
		kit  domain.QualityKit
		want int
	}{
		{"conditions", domain.QualityKit{DoneWhen: []string{"A"}}, 1},
		{"checklists only", domain.QualityKit{Checklists: []domain.Checklist{{Name: "go"}}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingDoneItems{}
			m := NewFlowManager(nil, nil, nil, nil, nil, nil)
			m.SetDoneItems(rec)
			s := &domain.Session{ID: "s1", Status: domain.SessionStatusProvisioning}
			if _, err := m.finaliseCreate(context.Background(), local.NewExecutor(""), s, domain.SessionConfig{}, tc.kit, ""); err != nil {
				t.Fatal(err)
			}
			if len(rec.ids) != tc.want {
				t.Fatalf("Ensure called %d times, want %d", len(rec.ids), tc.want)
			}
		})
	}
}

func TestKitSkippedForReviewerAndAdHoc(t *testing.T) {
	cases := []struct {
		name string
		cfg  domain.SessionConfig
		want bool
	}{
		{"repo", domain.SessionConfig{Repo: domain.Repo{Name: "o/r"}}, true},
		{"no repo", domain.SessionConfig{}, false},
		{"no repository", domain.SessionConfig{Repo: domain.Repo{Name: "No Repository"}}, false},
		{"ad hoc", domain.SessionConfig{Repo: domain.Repo{Name: "o/r"}, AdHoc: true}, false},
		{"reviewer", domain.SessionConfig{Repo: domain.Repo{Name: "o/r"}, Role: domain.SessionRoleReviewer}, false},
	}
	for _, tc := range cases {
		if got := shouldBuildKit(tc.cfg); got != tc.want {
			t.Errorf("%s: shouldBuildKit = %v, want %v", tc.name, got, tc.want)
		}
	}
}
