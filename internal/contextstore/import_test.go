package contextstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestParseImportAgents(t *testing.T) {
	got := ParseImportAgents("claude,agy")
	if len(got) != 2 || got[0] != "claude" || got[1] != "agy" {
		t.Fatalf("%v", got)
	}
	if len(ParseImportAgents("all")) != 4 {
		t.Fatal("all")
	}
	if ParseImportAgents("antigravity")[0] != "agy" {
		t.Fatal("alias")
	}
}

func TestSlugFromRemoteURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:realfi-co/realfi.git": "realfi-co/realfi",
		"https://github.com/owner/repo.git":   "owner/repo",
		"ssh://git@github.com/owner/repo":     "owner/repo",
		"https://gitlab.com/acme/app.git":     "acme/app",
	}
	for in, want := range cases {
		if got := slugFromRemoteURL(in); got != want {
			t.Errorf("%s -> %q want %q", in, got, want)
		}
	}
}

func TestCollectAndImport(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "work", "app")
	writeGitOrigin(t, repoDir, "git@github.com:acme/app.git")

	claudeMem := filepath.Join(home, ".claude", "projects", "-"+strings.ReplaceAll(repoDir, "/", "-")[1:], "memory")
	if err := os.MkdirAll(claudeMem, 0o700); err != nil {
		t.Fatal(err)
	}
	topic := "---\nname: cookie-fix\ndescription: Set the cookie on the API host.\n---\n\n# Cookie\n\nSet it on the API host.\n"
	if err := os.WriteFile(filepath.Join(claudeMem, "cookie-fix.md"), []byte(topic), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeMem, "MEMORY.md"), []byte("# Memory index\n- cookie\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(home, ".grok", "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grok", "memory", "MEMORY.md"), []byte("# Prefs\nAlways use pnpm.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(home, ".codex", "memories"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "memories", "memory_summary.md"), []byte("# Summary\nCodex prefers rebase.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	agyDir := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "aaaa-bbbb")
	if err := os.MkdirAll(agyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	walk := "# Walkthrough - Yield oracle\n\nSee [proxy](file://" + repoDir + "/api/proxy.ts).\n"
	if err := os.WriteFile(filepath.Join(agyDir, "walkthrough.md"), []byte(walk), 0o600); err != nil {
		t.Fatal(err)
	}

	files := CollectMemories(home, []string{"claude", "grok", "codex", "agy"})
	if len(files) != 4 {
		t.Fatalf("files=%d %+v", len(files), names(files))
	}

	store := NewFiles(t.TempDir())
	res, err := ImportMemories(context.Background(), store, files, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 4 {
		t.Fatalf("imported %d skipped %d notes %+v", res.Imported, res.Skipped, res.Notes)
	}

	again, err := ImportMemories(context.Background(), store, files, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Notes[0].ID != res.Notes[0].ID {
		t.Fatalf("id should be stable %s vs %s", again.Notes[0].ID, res.Notes[0].ID)
	}
	list, err := store.List(context.Background(), domain.ContextQuery{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 {
		t.Fatalf("store size %d", len(list))
	}

	var claude domain.ContextEntry
	for _, e := range list {
		if strings.Contains(e.Title, "cookie-fix") || strings.Contains(e.Title, "Cookie") {
			claude = e
		}
	}
	if claude.ID == "" || claude.Namespace != domain.ContextNSRepo || claude.Key != "acme__app" {
		t.Fatalf("claude dest %+v", claude)
	}
}

func TestImportDryRun(t *testing.T) {
	files := []MemoryFile{{Agent: "claude", RelPath: "x.md", Title: "t", Abstract: "a", Body: "b"}}
	store := NewFiles(t.TempDir())
	res, err := ImportMemories(context.Background(), store, files, "G1", "", true)
	if err != nil || res.Imported != 1 || !res.DryRun {
		t.Fatalf("%+v %v", res, err)
	}
	list, _ := store.List(context.Background(), domain.ContextQuery{})
	if len(list) != 0 {
		t.Fatal("dry-run must not write")
	}
}

func TestImportDestGroupOverride(t *testing.T) {
	e := memoryEntry(MemoryFile{Agent: "grok", RelPath: "m.md", Title: "t", Abstract: "a", Body: "b", Repo: "o/r"}, "WTB-1", "")
	if e.Namespace != domain.ContextNSGroup || e.Key != "WTB-1" {
		t.Fatalf("%+v", e)
	}
}

func writeGitOrigin(t *testing.T, repoDir, url string) {
	t.Helper()
	git := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(git, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + url + "\n"
	if err := os.WriteFile(filepath.Join(git, "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(files []MemoryFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Agent+":"+f.RelPath)
	}
	return out
}
