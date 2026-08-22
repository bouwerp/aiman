package domain

import "testing"

func TestValidSessionName(t *testing.T) {
	t.Parallel()
	ok := []string{"q1", "impl", "reviewer", "WTB-1925", "A", "a_b-c1"}
	for _, name := range ok {
		if err := ValidateSessionName(name); err != nil {
			t.Errorf("ValidateSessionName(%q) = %v, want nil", name, err)
		}
	}
	bad := []string{"", "1q", "has.dot", "has:colon", "has/slash", "has space", "-lead", stringsRepeat("a", 49)}
	for _, name := range bad {
		if err := ValidateSessionName(name); err == nil {
			t.Errorf("ValidateSessionName(%q) = nil, want error", name)
		}
	}
}

func stringsRepeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func TestGroupLabelAndNormalize(t *testing.T) {
	t.Parallel()
	if GroupLabel("") != GroupUngrouped || GroupLabel("  ") != GroupUngrouped {
		t.Fatal("empty is ungrouped")
	}
	if GroupLabel("WTB-1") != "WTB-1" {
		t.Fatal("keeps a set group")
	}
	got, err := NormalizeGroupName("  my group  ")
	if err != nil || got != "my-group" {
		t.Fatalf("got %q %v", got, err)
	}
	got, err = NormalizeGroupName("")
	if err != nil || got != GroupUngrouped {
		t.Fatalf("empty normalize %q %v", got, err)
	}
	got, err = NormalizeGroupName("ungrouped")
	if err != nil || got != GroupUngrouped {
		t.Fatalf("ungrouped %q %v", got, err)
	}
	if _, err := NormalizeGroupName("1bad"); err == nil {
		t.Fatal("want error")
	}
}

func TestNameTakenIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	existing := []Session{{Name: "Reviewer"}, {Name: "impl"}}
	if !NameTaken(existing, "reviewer") {
		t.Fatal("expected reviewer to collide with Reviewer")
	}
	if NameTaken(existing, "q1") {
		t.Fatal("q1 should be free")
	}
}

func TestAssignSessionNameQuickSequence(t *testing.T) {
	t.Parallel()
	existing := []Session{{Name: "q1"}, {Name: "Q2"}}
	got, err := AssignSessionName(existing, "", true)
	if err != nil {
		t.Fatalf("AssignSessionName: %v", err)
	}
	if got != "q3" {
		t.Fatalf("got %q, want q3", got)
	}
}

func TestAssignSessionNamePrefersImpl(t *testing.T) {
	t.Parallel()
	got, err := AssignSessionName(nil, "wtb-1925-auth", false)
	if err != nil {
		t.Fatalf("AssignSessionName: %v", err)
	}
	if got != "impl" {
		t.Fatalf("got %q, want impl", got)
	}
	existing := []Session{{Name: "impl"}}
	got, err = AssignSessionName(existing, "wtb-1925-auth", false)
	if err != nil {
		t.Fatalf("AssignSessionName: %v", err)
	}
	if got != "wtb-1925-auth" {
		t.Fatalf("got %q, want wtb-1925-auth", got)
	}
}

func TestAssignSessionGroup(t *testing.T) {
	t.Parallel()
	if got := AssignSessionGroup("explicit", "WTB-1", "owner/realfi", false); got != "explicit" {
		t.Fatalf("got %q", got)
	}
	if got := AssignSessionGroup("", "WTB-1", "owner/realfi", false); got != "WTB-1" {
		t.Fatalf("got %q", got)
	}
	if got := AssignSessionGroup("", "", "owner/realfi", false); got != "realfi" {
		t.Fatalf("got %q", got)
	}
	if got := AssignSessionGroup("", "", "", false); got != GroupUngrouped {
		t.Fatalf("got %q", got)
	}
	if got := AssignSessionGroup("", "WTB-1", "owner/realfi", true); got != GroupQuick {
		t.Fatalf("got %q", got)
	}
}
