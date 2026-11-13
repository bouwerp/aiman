package cigate_test

import (
	"testing"

	"github.com/bouwerp/aiman/internal/cigate"
)

// Acceptance tests for the git -C push gate. Do not edit.

func TestSplitPushDir(t *testing.T) {
	cases := []struct {
		cmd, dir, rest string
		ok             bool
	}{
		{"git -C /a/b push -u origin feat", "/a/b", "git push -u origin feat", true},
		{"git -C /a/b push", "/a/b", "git push", true},
		{"git -C rel/dir push", "rel/dir", "git push", true},
		{"git push origin feat", "", "", false},
		{"git -C /a/b status", "", "", false},
		{"git -C push", "", "", false},
		{"git -c core.x=y push", "", "", false},
		{"git -C /a -C /b push", "", "", false},
		{"git -C '/a b' push", "", "", false},
		{"git -C /a/b push; rm -rf /", "", "", false},
	}
	for _, c := range cases {
		dir, rest, ok := cigate.SplitPushDir(c.cmd)
		if ok != c.ok || dir != c.dir || rest != c.rest {
			t.Errorf("SplitPushDir(%q) = %q, %q, %v; want %q, %q, %v", c.cmd, dir, rest, ok, c.dir, c.rest, c.ok)
		}
	}
}
