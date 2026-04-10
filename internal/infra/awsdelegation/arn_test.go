package awsdelegation

import "testing"

func TestRoleARNFromParts(t *testing.T) {
	got, err := RoleARNFromParts("123456789012", "MyRole")
	if err != nil {
		t.Fatal(err)
	}
	want := "arn:aws:iam::123456789012:role/MyRole"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRoleARNFromParts_DefaultRoleName(t *testing.T) {
	got, err := RoleARNFromParts("123456789012", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "arn:aws:iam::123456789012:role/" + DefaultDelegatedRoleName; got != want {
		t.Fatalf("got %q", got)
	}
}

func TestRoleARNFromParts_BadAccount(t *testing.T) {
	if _, err := RoleARNFromParts("123", "x"); err == nil {
		t.Fatal("expected error")
	}
}
