package ssh

import "testing"

func TestTarget(t *testing.T) {
	cases := []struct{ host, user, want string }{
		{"regent0.lan", "code", "code@regent0.lan"},
		{"me@regent0.lan", "code", "me@regent0.lan"},
		{"regent0.lan", "", "regent0.lan"},
	}
	for _, c := range cases {
		if got := Target(c.host, c.user); got != c.want {
			t.Errorf("Target(%q, %q) = %q, want %q", c.host, c.user, got, c.want)
		}
	}
}
