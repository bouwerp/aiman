package contextstore

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestFilesPutGetListFindPack(t *testing.T) {
	s := NewFiles(t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a := domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "WTB-1925",
		Title: "Auth cookie on API", Abstract: "SPA cannot set the session cookie.",
		Body: "Set it on the API host.", SessionID: "sess-1", CreatedAt: now,
	}
	b := domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "WTB-1925",
		Title: "Worktree layout", Abstract: "Repos use owner/repo@branch.",
		Body: "See git manager.", CreatedAt: now.Add(-time.Hour),
	}
	c := domain.ContextEntry{
		Namespace: domain.ContextNSRepo, Key: "org/repo",
		Title: "Other group", Abstract: "unrelated", Body: "nope", CreatedAt: now,
	}
	if _, err := s.Put(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, c); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx, domain.ContextQuery{Namespace: domain.ContextNSGroup, Key: "WTB-1925"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "Auth cookie on API" {
		t.Fatalf("list=%+v", list)
	}

	found, err := s.Find(ctx, domain.ContextQuery{Text: "session cookie"})
	if err != nil || len(found) != 1 {
		t.Fatalf("find: %v %+v", err, found)
	}
	got, err := s.Get(ctx, found[0].ID)
	if err != nil || got.Body != "Set it on the API host." {
		t.Fatalf("get: %v %+v", err, got)
	}

	pack, err := s.Pack(ctx, domain.ContextQuery{Namespace: domain.ContextNSGroup, Key: "WTB-1925", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pack, "Auth cookie on API") || !strings.Contains(pack, found[0].ID) {
		t.Fatalf("pack %q", pack)
	}
	if !strings.Contains(pack, "aiman context get") {
		t.Fatalf("pack missing get hint: %q", pack)
	}
}

func TestFilesRejectsUnsafe(t *testing.T) {
	s := NewFiles(t.TempDir())
	_, err := s.Put(context.Background(), domain.ContextEntry{Title: "x", Key: "../etc"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPackForSession(t *testing.T) {
	s := NewFiles(t.TempDir())
	ctx := context.Background()
	if _, err := s.Put(ctx, domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "G1", Title: "Group note", Abstract: "g",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, domain.ContextEntry{
		Namespace: domain.ContextNSRepo, Key: "org/repo", Title: "Repo note", Abstract: "r",
	}); err != nil {
		t.Fatal(err)
	}
	pack := PackForSession(ctx, s, "G1", "org/repo")
	if !strings.Contains(pack, "Group note") || !strings.Contains(pack, "Repo note") {
		t.Fatalf("pack %q", pack)
	}
}

type memWriter struct {
	files map[string][]byte
}

func (m *memWriter) WriteFile(_ context.Context, path string, content []byte) error {
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[path] = content
	return nil
}

func TestWriteSessionPack(t *testing.T) {
	w := &memWriter{}
	if err := WriteSessionPack(context.Background(), w, "/wt", "# Shared context\n\n- **n** (`id`): a\n"); err != nil {
		t.Fatal(err)
	}
	got := string(w.files["/wt/"+domain.AimanContextFileName])
	if !strings.Contains(got, "DO NOT COMMIT") || !strings.Contains(got, "Shared context") {
		t.Fatalf("%s", got)
	}
}

func TestSafeKeyRepo(t *testing.T) {
	if SafeKey("org/repo") != "org__repo" {
		t.Fatalf("%q", SafeKey("org/repo"))
	}
	if SafeKey("..") != "" || SafeKey("a/b/../../x") != "" {
		t.Fatal("dotdot")
	}
}

func TestEncodeFilePath(t *testing.T) {
	p, data, err := EncodeFile("/tmp/ctx", domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "WTB-1", Title: "n", ID: "abc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p != path.Join("/tmp/ctx", "groups", "WTB-1", "abc-1.md") {
		t.Fatalf("path %q", p)
	}
	if !strings.Contains(string(data), "title:") || !strings.Contains(string(data), "n") {
		t.Fatalf("%s", data)
	}
}

func TestEntryFromSnapshot(t *testing.T) {
	e := EntryFromSnapshot(&domain.SessionSnapshot{
		Summary: "Fixed auth", IssueKey: "WTB-9", RepoName: "o/r",
		Overview: []string{"did x"}, NextSteps: []string{"do y"},
	}, "", "sid")
	if e.Namespace != domain.ContextNSGroup || e.Key != "WTB-9" {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(e.Body, "do y") {
		t.Fatalf("body %q", e.Body)
	}
}

func TestListEmptyRoot(t *testing.T) {
	s := NewFiles(path.Join(t.TempDir(), "missing"))
	list, err := s.List(context.Background(), domain.ContextQuery{})
	if err != nil || len(list) != 0 {
		t.Fatalf("%v %+v", err, list)
	}
}
