package domain

import "testing"

func TestValidateParent_EmptyIsRoot(t *testing.T) {
	if err := ValidateParent("child", "", nil); err != nil {
		t.Fatalf("empty parent must be a root, got %v", err)
	}
}

func TestValidateParent_RejectsSelf(t *testing.T) {
	if err := ValidateParent("s1", "s1", nil); err == nil {
		t.Fatal("expected self-parent to be rejected")
	}
}

func TestValidateParent_RejectsUnknown(t *testing.T) {
	if err := ValidateParent("child", "missing", []Session{{ID: "other"}}); err == nil {
		t.Fatal("expected unknown parent to be rejected")
	}
}

func TestValidateParent_AcceptsKnown(t *testing.T) {
	if err := ValidateParent("child", "parent", []Session{{ID: "parent"}}); err != nil {
		t.Fatalf("known parent: %v", err)
	}
}

func TestValidateParent_RejectsCycle(t *testing.T) {
	sessions := []Session{
		{ID: "a", ParentID: "c"},
		{ID: "b", ParentID: "a"},
		{ID: "c", ParentID: "b"},
	}
	if err := ValidateParent("a", "c", sessions); err == nil {
		t.Fatal("expected cycle to be rejected")
	}
}

func TestResolveCreateParent_UnknownCallerIsRoot(t *testing.T) {
	got, err := ResolveCreateParent(false, "", "stale-caller", "child", []Session{{ID: "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("unknown caller must not fail create, got %q", got)
	}
}

func TestResolveCreateParent_DefaultsToCaller(t *testing.T) {
	got, err := ResolveCreateParent(false, "", "parent", "child", []Session{{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "parent" {
		t.Fatalf("got %q want parent", got)
	}
}

func TestResolveCreateParent_OrphanIsRoot(t *testing.T) {
	got, err := ResolveCreateParent(true, "parent", "parent", "child", []Session{{ID: "parent"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("orphan must be a root, got %q", got)
	}
}

func TestResolveCreateParent_ExplicitUnknownFails(t *testing.T) {
	if _, err := ResolveCreateParent(false, "missing", "caller", "child", []Session{{ID: "caller"}}); err == nil {
		t.Fatal("explicit unknown parent must fail")
	}
}

func TestResolveCreateParent_ExplicitOverridesCaller(t *testing.T) {
	got, err := ResolveCreateParent(false, "other", "caller", "child", []Session{
		{ID: "caller"}, {ID: "other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "other" {
		t.Fatalf("got %q want other", got)
	}
}
