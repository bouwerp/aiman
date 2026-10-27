package quality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
)

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not installed", name)
	}
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

func run(t *testing.T, script string) string {
	t.Helper()
	out, err := local.NewExecutor("").Execute(context.Background(), script)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return out
}

func TestMatchScriptRealGit(t *testing.T) {
	requireTool(t, "git")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/usr/bin:/bin")
	wt := filepath.Join(t.TempDir(), "it's a repo")
	writeFile(t, filepath.Join(wt, "internal", "a.go"), "package a\n")
	writeFile(t, filepath.Join(wt, "web", "x.ts"), "export {}\n")
	for _, args := range [][]string{{"init", "-q"}, {"add", "internal/a.go"}} {
		if out, err := exec.Command("git", append([]string{"-C", wt}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	lists := []domain.Checklist{
		{Name: "go", AppliesTo: []string{"*.go"}},
		{Name: "ts", AppliesTo: []string{"*.ts"}},
		{Name: "tf", AppliesTo: []string{"*.tf"}},
		{Name: "all"},
	}
	got := ParseMatchOutput(run(t, MatchScript(wt, lists)))
	want := map[string]bool{"go": true, "ts": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matched = %v, want %v", got, want)
	}
}

func TestMatchScriptWithoutGlobsIsEmpty(t *testing.T) {
	if s := MatchScript("/wt", []domain.Checklist{{Name: "all"}}); s != "" {
		t.Errorf("MatchScript = %q, want empty", s)
	}
	shadowed := []domain.Checklist{
		{Name: "go", Source: SourceUser, AppliesTo: []string{"*.go"}},
		{Name: "go", Source: SourceRepo},
	}
	if s := MatchScript("/wt", shadowed); s != "" {
		t.Errorf("MatchScript with shadowed user checklist = %q, want empty", s)
	}
}

func TestListScriptRealBash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	wt := filepath.Join(t.TempDir(), "wt with space")
	userBody := "---\napplies_to: \"*.tf\"\n---\n- [ ] plan reviewed\n"
	writeFile(t, filepath.Join(home, UserChecklistDir, "tf.md"), userBody)
	writeFile(t, filepath.Join(home, UserChecklistDir, "notes.txt"), "ignored")
	big := strings.Repeat("x", maxChecklistBytes+500)
	writeFile(t, filepath.Join(wt, RepoChecklistDir, "go.md"), big)

	gotHome, files := ParseListOutput(run(t, ListScript(wt)))
	if gotHome != home {
		t.Errorf("home = %q, want %q", gotHome, home)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want 2", files)
	}
	repo, user := files[0], files[1]
	if repo.Source != "repo" || repo.Path != filepath.Join(wt, RepoChecklistDir, "go.md") || repo.Name() != "go" {
		t.Errorf("repo file = %+v", repo)
	}
	if len(repo.Content) != maxChecklistBytes {
		t.Errorf("repo content len = %d, want %d", len(repo.Content), maxChecklistBytes)
	}
	if user.Source != "user" || user.Path != filepath.Join(home, UserChecklistDir, "tf.md") {
		t.Errorf("user file = %+v", user)
	}
	if string(user.Content) != userBody {
		t.Errorf("user content = %q, want %q", user.Content, userBody)
	}
}

func TestListScriptNoDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gotHome, files := ParseListOutput(run(t, ListScript(filepath.Join(t.TempDir(), "missing"))))
	if gotHome != home || len(files) != 0 {
		t.Errorf("got home %q files %+v", gotHome, files)
	}
}

func TestPushPlan(t *testing.T) {
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	local := map[string][]byte{
		"same.md":    []byte("a"),
		"changed.md": []byte("b"),
		"new.md":     []byte("c"),
	}
	remote := map[string]string{
		"same.md":    sum([]byte("a")),
		"changed.md": sum([]byte("old")),
		"other.md":   sum([]byte("z")),
	}
	got := PushPlan(local, remote)
	want := []string{"changed.md", "new.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PushPlan = %v, want %v", got, want)
	}
}

func TestRemoteSumsScriptRealBash(t *testing.T) {
	requireTool(t, "sha256sum")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	gotHome, sums := ParseRemoteSums(run(t, RemoteSumsScript()))
	if gotHome != home || len(sums) != 0 {
		t.Fatalf("empty dir: home %q sums %v", gotHome, sums)
	}
	if _, err := os.Stat(filepath.Join(home, UserChecklistDir)); err != nil {
		t.Fatalf("script did not create the checklist dir: %v", err)
	}

	writeFile(t, filepath.Join(home, UserChecklistDir, "go.md"), "a")
	_, sums = ParseRemoteSums(run(t, RemoteSumsScript()))
	h := sha256.Sum256([]byte("a"))
	if sums["go.md"] != hex.EncodeToString(h[:]) || len(sums) != 1 {
		t.Errorf("sums = %v", sums)
	}
}
