package coordinator

import "testing"

func TestChildBranch(t *testing.T) {
	if got := ChildBranch("feat/proj-1", "t3"); got != "feat/proj-1--t3" {
		t.Fatalf("ChildBranch = %q, want feat/proj-1--t3", got)
	}
}

func TestChildRef(t *testing.T) {
	if got := ChildRef("c1"); got != "child:c1" {
		t.Fatalf("ChildRef = %q, want child:c1", got)
	}
}

func TestChildID(t *testing.T) {
	cases := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{"child:c1", "c1", true},
		{ChildRef("abc-123"), "abc-123", true},
		{"child:", "", false},
		{"check:lint", "", false},
		{"review:42", "", false},
		{"", "", false},
		{"xchild:c1", "", false},
	}
	for _, c := range cases {
		got, ok := ChildID(c.ref)
		if got != c.want || ok != c.wantOK {
			t.Errorf("ChildID(%q) = %q, %v; want %q, %v", c.ref, got, ok, c.want, c.wantOK)
		}
	}
}
