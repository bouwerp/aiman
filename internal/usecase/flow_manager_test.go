package usecase

import "testing"

func TestAWSRoleSessionName(t *testing.T) {
	if got := awsRoleSessionName("12345678-90ab-cdef"); got != "session-12345678" {
		t.Fatalf("unexpected aws role session name: %q", got)
	}
	if got := awsRoleSessionName(""); got != "session" {
		t.Fatalf("unexpected empty-session fallback: %q", got)
	}
}
