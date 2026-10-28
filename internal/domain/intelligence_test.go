package domain

import "testing"

func TestFilterMemoryCandidates(t *testing.T) {
	in := []MemoryCandidate{
		{Type: "Decision", Title: "  Use sqlite  ", Abstract: "a", Body: "b"},
		{Type: "habit", Title: "not durable kind"},
		{Type: "bogus", Title: "unknown kind"},
		{Type: "pitfall", Title: "   "},
		{Type: "", Title: "empty type is a fact"},
		{Type: "convention", Title: "c1"},
		{Type: "fact", Title: "f1"},
		{Type: "pitfall", Title: "p1"},
		{Type: "fact", Title: "over the cap"},
	}
	got := FilterMemoryCandidates(in)
	if len(got) != MaxMemoryCandidates {
		t.Fatalf("len = %d, want %d: %+v", len(got), MaxMemoryCandidates, got)
	}
	want := []MemoryCandidate{
		{Type: ContextTypeDecision, Title: "Use sqlite", Abstract: "a", Body: "b"},
		{Type: ContextTypeFact, Title: "empty type is a fact"},
		{Type: ContextTypeConvention, Title: "c1"},
		{Type: ContextTypeFact, Title: "f1"},
		{Type: ContextTypePitfall, Title: "p1"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFilterMemoryCandidatesEmpty(t *testing.T) {
	if got := FilterMemoryCandidates(nil); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}
