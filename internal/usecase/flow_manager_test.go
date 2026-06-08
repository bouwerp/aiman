package usecase

import "testing"

func TestJoinPrompt(t *testing.T) {
	cases := []struct {
		name       string
		base, user string
		want       string
	}{
		{"both present", "base prompt", "extra", "base prompt extra"},
		{"empty base", "", "extra", "extra"},
		{"empty user", "base prompt", "", "base prompt"},
		{"trims whitespace", "  base  ", "  extra  ", "base extra"},
		{"both empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := joinPrompt(c.base, c.user); got != c.want {
				t.Fatalf("joinPrompt(%q, %q) = %q, want %q", c.base, c.user, got, c.want)
			}
		})
	}
}

func TestAWSRoleSessionName(t *testing.T) {
	if got := awsRoleSessionName("12345678-90ab-cdef"); got != "session-12345678" {
		t.Fatalf("unexpected aws role session name: %q", got)
	}
	if got := awsRoleSessionName(""); got != "session" {
		t.Fatalf("unexpected empty-session fallback: %q", got)
	}
}

func TestSharedSessionAWSEnv(t *testing.T) {
	env := sharedSessionAWSEnv("eu-west-1")
	if got := env["AWS_REGION"]; got != "eu-west-1" {
		t.Fatalf("unexpected AWS_REGION: %q", got)
	}
	if got := env["AWS_DEFAULT_REGION"]; got != "eu-west-1" {
		t.Fatalf("unexpected AWS_DEFAULT_REGION: %q", got)
	}
	if _, ok := env["AWS_SHARED_CREDENTIALS_FILE"]; ok {
		t.Fatal("did not expect AWS_SHARED_CREDENTIALS_FILE in shared session env")
	}
	if _, ok := env["AWS_CONFIG_FILE"]; ok {
		t.Fatal("did not expect AWS_CONFIG_FILE in shared session env")
	}
}
