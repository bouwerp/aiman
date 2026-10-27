package contextstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
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
	files  map[string][]byte
	writes int
}

// Execute answers the sha256sum probe remotefile.WriteIfChanged sends.
func (m *memWriter) Execute(_ context.Context, cmd string) (string, error) {
	rest, ok := strings.CutPrefix(cmd, "sha256sum -- '")
	if !ok {
		return "", nil
	}
	p, _, _ := strings.Cut(rest, "'")
	content, held := m.files[p]
	if !held {
		return "", nil
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) + "  " + p + "\n", nil
}

func (m *memWriter) WriteFile(_ context.Context, path string, content []byte) error {
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[path] = content
	m.writes++
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

func TestWriteSessionPackUnchangedSkipsWrite(t *testing.T) {
	w := &memWriter{}
	ctx := context.Background()
	pack := "# Shared context\n\n- **n** (`id`): a\n"
	for range 2 {
		if err := WriteSessionPack(ctx, w, "/wt", pack); err != nil {
			t.Fatal(err)
		}
	}
	if w.writes != 1 {
		t.Fatalf("writes = %d, want 1", w.writes)
	}
	if err := WriteSessionPack(ctx, w, "/wt", pack+"- **m** (`id2`): b\n"); err != nil {
		t.Fatal(err)
	}
	if w.writes != 2 {
		t.Fatalf("changed pack writes = %d, want 2", w.writes)
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

func TestPutRoundTripsTypedFields(t *testing.T) {
	s := NewFiles(t.TempDir())
	valid := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	stored, err := s.Put(context.Background(), domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "G1", Title: "Typed",
		Type: domain.ContextTypePitfall, Supersedes: "old-1", ValidUntil: valid,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != domain.ContextTypePitfall || got.Supersedes != "old-1" || !got.ValidUntil.Equal(valid) {
		t.Fatalf("got %+v", got)
	}
	raw, err := os.ReadFile(absPath(s.root, stored))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "type: pitfall") {
		t.Fatalf("file missing type:\n%s", raw)
	}
}

func TestUntypedNoteReadsAsFact(t *testing.T) {
	s := NewFiles(t.TempDir())
	writeRawNote(t, s, "legacy_1", `id: legacy_1
ns: host
key: host
title: Legacy
abstract: old
created: 2026-01-01T00:00:00Z
`)
	got, err := s.Get(context.Background(), "legacy_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != domain.ContextTypeFact {
		t.Fatalf("type = %q, want fact", got.Type)
	}
}

func TestPutRejectsUnknownType(t *testing.T) {
	s := NewFiles(t.TempDir())
	_, err := s.Put(context.Background(), domain.ContextEntry{Title: "x", Type: "banana"})
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandEditedBadFieldsStillLoad(t *testing.T) {
	s := NewFiles(t.TempDir())
	writeRawNote(t, s, "edited_1", `id: edited_1
ns: host
key: host
title: Edited
abstract: old
type: Banana
valid_until: soon
created: 2026-01-01T00:00:00Z
`)
	got, err := s.Get(context.Background(), "edited_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != domain.ContextTypeFact || !got.ValidUntil.IsZero() {
		t.Fatalf("got %+v", got)
	}
}

func TestSnapshotEntryIsSummary(t *testing.T) {
	e := EntryFromSnapshot(&domain.SessionSnapshot{Summary: "Fixed auth"}, "", "sid")
	if e.Type != domain.ContextTypeSummary {
		t.Fatalf("type = %q, want summary", e.Type)
	}
}

func TestProvenanceRoundTrip(t *testing.T) {
	s := NewFiles(t.TempDir())
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	stored, err := s.Put(context.Background(), domain.ContextEntry{
		Title: "Verified note", Author: domain.ContextAuthorHuman, Verified: true,
		VerifiedAt: at, Source: "file:docs/auth.md", SourceHash: "abc123",
		Confidence: domain.ContextConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Author != domain.ContextAuthorHuman || !got.Verified || !got.VerifiedAt.Equal(at) ||
		got.Source != "file:docs/auth.md" || got.SourceHash != "abc123" || got.Confidence != domain.ContextConfidenceHigh {
		t.Fatalf("got %+v", got)
	}
	raw, err := os.ReadFile(absPath(s.root, stored))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"author: human", "verified: true", "source: file:docs/auth.md", "source_hash: abc123", "confidence: high"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("file missing %q:\n%s", want, raw)
		}
	}
}

func TestLegacyNoteIsUnverifiedUnknown(t *testing.T) {
	s := NewFiles(t.TempDir())
	writeRawNote(t, s, "legacy_prov", `id: legacy_prov
ns: host
key: host
title: Legacy provenance
abstract: old
created: 2026-01-01T00:00:00Z
`)
	got, err := s.Get(context.Background(), "legacy_prov")
	if err != nil {
		t.Fatal(err)
	}
	if got.Author != domain.ContextAuthorUnknown || got.Verified || got.Confidence != "" {
		t.Fatalf("legacy = %+v", got)
	}
}

func writeRawNote(t *testing.T, s *Files, id, frontmatter string) {
	t.Helper()
	p := path.Join(s.root, "host", "host", id+".md")
	if err := os.MkdirAll(path.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "---\n" + frontmatter + "---\n\nbody\n"
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHabitSupersedeRequiresEvidence(t *testing.T) {
	s := NewFiles(t.TempDir())
	first, err := s.Put(context.Background(), domain.ContextEntry{Title: "Old habit", Type: domain.ContextTypeHabit})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Put(context.Background(), domain.ContextEntry{
		Title: "New habit", Type: domain.ContextTypeHabit, Supersedes: first.ID,
	})
	if err == nil {
		t.Fatal("habit supersede without evidence succeeded")
	}
	stored, err := s.Put(context.Background(), domain.ContextEntry{
		Title: "New habit", Type: domain.ContextTypeHabit, Supersedes: first.ID, Evidence: "session:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Evidence != "session:abc" {
		t.Fatalf("evidence = %q", stored.Evidence)
	}
}

func TestFactSupersedeNeedsNoEvidence(t *testing.T) {
	s := NewFiles(t.TempDir())
	first, err := s.Put(context.Background(), domain.ContextEntry{Title: "Old fact"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(context.Background(), domain.ContextEntry{Title: "New fact", Supersedes: first.ID}); err != nil {
		t.Fatalf("fact supersede: %v", err)
	}
}

func TestUnverifiedHabitNotPacked(t *testing.T) {
	now := time.Now().UTC()
	entries := []domain.ContextEntry{
		{ID: "habit", Type: domain.ContextTypeHabit, Title: "Unverified habit", Abstract: "suggestion", CreatedAt: now},
		{ID: "fact", Type: domain.ContextTypeFact, Title: "Fact", Abstract: "truth", CreatedAt: now},
	}
	pack := FormatPack(entries)
	if strings.Contains(pack, "Unverified habit") || !strings.Contains(pack, "Fact") {
		t.Fatalf("pack = %q", pack)
	}
	entries[0].Verified = true
	if pack := FormatPack(entries); !strings.Contains(pack, "Unverified habit") {
		t.Fatalf("verified habit missing: %q", pack)
	}
}
