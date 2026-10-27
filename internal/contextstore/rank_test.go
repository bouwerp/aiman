package contextstore

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func putRankNote(t *testing.T, s *Files, e domain.ContextEntry) domain.ContextEntry {
	t.Helper()
	if e.Namespace == "" {
		e.Namespace = domain.ContextNSGroup
	}
	if e.Key == "" {
		e.Key = "G1"
	}
	if e.Title == "" {
		e.Title = "note"
	}
	stored, err := s.Put(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestRankTierThenRecency(t *testing.T) {
	now := time.Now().UTC()
	list := []domain.ContextEntry{
		{ID: "fact", Type: domain.ContextTypeFact, CreatedAt: now},
		{ID: "pitfall", Type: domain.ContextTypePitfall, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "decision", Type: domain.ContextTypeDecision, CreatedAt: now.Add(-time.Hour)},
		{ID: "decision2", Type: domain.ContextTypeDecision, CreatedAt: now.Add(-time.Hour)},
	}
	Rank(list)
	got := []string{list[0].ID, list[1].ID, list[2].ID, list[3].ID}
	want := []string{"decision2", "decision", "pitfall", "fact"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestFindSkipsSupersededAndExpired(t *testing.T) {
	s := NewFiles(t.TempDir())
	now := time.Now().UTC()
	a := putRankNote(t, s, domain.ContextEntry{Title: "A", Body: "needle", CreatedAt: now})
	putRankNote(t, s, domain.ContextEntry{Title: "B", Body: "needle", Supersedes: a.ID, CreatedAt: now})
	putRankNote(t, s, domain.ContextEntry{Title: "C", Body: "needle", ValidUntil: now.Add(-24 * time.Hour), CreatedAt: now})
	putRankNote(t, s, domain.ContextEntry{Title: "D", Body: "needle", ValidUntil: now.Add(24 * time.Hour), CreatedAt: now})
	list, err := s.Find(context.Background(), domain.ContextQuery{Text: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	got := titles(list)
	sort.Strings(got)
	if strings.Join(got, ",") != "B,D" {
		t.Fatalf("find = %v, want B,D", got)
	}
}

func TestPackSessionSkipsSupersededAndRanks(t *testing.T) {
	s := NewFiles(t.TempDir())
	now := time.Now().UTC()
	putRankNote(t, s, domain.ContextEntry{Title: "Convention", Type: domain.ContextTypeConvention, CreatedAt: now.Add(-2 * time.Hour)})
	putRankNote(t, s, domain.ContextEntry{Title: "New fact", Type: domain.ContextTypeFact, CreatedAt: now})
	old := putRankNote(t, s, domain.ContextEntry{Title: "Old fact", Type: domain.ContextTypeFact, CreatedAt: now.Add(-3 * time.Hour)})
	putRankNote(t, s, domain.ContextEntry{Title: "Replacement", Type: domain.ContextTypeFact, Supersedes: old.ID, CreatedAt: now.Add(-time.Hour)})
	pack := PackForSession(context.Background(), s, "G1", "")
	if strings.Contains(pack, "Old fact") || !strings.Contains(pack, "Replacement") {
		t.Fatalf("pack supersede:\n%s", pack)
	}
	if strings.Index(pack, "[convention]") > strings.Index(pack, "[fact]") {
		t.Fatalf("pack did not rank convention first:\n%s", pack)
	}
}

func TestListStillShowsSuperseded(t *testing.T) {
	s := NewFiles(t.TempDir())
	now := time.Now().UTC()
	old := putRankNote(t, s, domain.ContextEntry{Title: "Old", CreatedAt: now})
	putRankNote(t, s, domain.ContextEntry{Title: "New", Supersedes: old.ID, CreatedAt: now})
	list, err := s.List(context.Background(), domain.ContextQuery{Namespace: domain.ContextNSGroup, Key: "G1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(titles(list), ","), "Old") {
		t.Fatalf("list hid superseded note: %v", titles(list))
	}
}

func TestSupersedeAcrossNamespaces(t *testing.T) {
	s := NewFiles(t.TempDir())
	now := time.Now().UTC()
	group := putRankNote(t, s, domain.ContextEntry{Title: "Group", CreatedAt: now})
	putRankNote(t, s, domain.ContextEntry{
		Namespace: domain.ContextNSHost, Key: "host", Title: "Host", Supersedes: group.ID, CreatedAt: now,
	})
	pack := PackForSession(context.Background(), s, "G1", "")
	if strings.Contains(pack, "Group") || !strings.Contains(pack, "Host") {
		t.Fatalf("cross-namespace supersede failed:\n%s", pack)
	}
}

func TestFormatPackShowsType(t *testing.T) {
	pack := FormatPack([]domain.ContextEntry{{
		ID: "id1", Title: "Auth", Abstract: "Set it on the API host.", Type: domain.ContextTypePitfall,
	}})
	want := "- **Auth** [pitfall] (`id1`): Set it on the API host."
	if !strings.Contains(pack, want) {
		t.Fatalf("pack = %q, want %q", pack, want)
	}
}

func TestAge(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		created time.Time
		want    string
	}{
		{now.Add(-59 * time.Second), "0m"},
		{now.Add(-3 * time.Hour), "3h"},
		{now.Add(-48 * time.Hour), "2d"},
		{now.Add(-13 * 24 * time.Hour), "1w"},
		{now.Add(-400 * 24 * time.Hour), "1y"},
	}
	for _, tc := range cases {
		if got := Age(tc.created, now); got != tc.want {
			t.Fatalf("Age(%v) = %q, want %q", now.Sub(tc.created), got, tc.want)
		}
	}
}

func titles(list []domain.ContextEntry) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.Title)
	}
	return out
}
