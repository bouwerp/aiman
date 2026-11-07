//go:build acceptance

package server

import "testing"

// session.create must refuse a branch git would refuse, before the creator
// (and so any shell on the host) sees it. A phone can reach this method.
func TestSessionCreateRefusesInvalidBranch(t *testing.T) {
	bad := []string{
		"-rf",
		"--upload-pack=touch /tmp/x",
		"a..b",
		"a b",
		"a\nb",
		"topic.lock",
		"a//b",
		"a/",
		"a@{1}",
		"a~1",
		"x$(id) y",
	}
	for _, existing := range []bool{false, true} {
		for _, branch := range bad {
			creator := &fakeCreator{}
			resp := createOnFakeCreator(t, creator, map[string]any{
				"repo": "o/r", "branch": branch, "agent": "codex", "existing_branch": existing,
			})
			if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
				t.Errorf("branch %q (existing=%v): want invalid_params, got %+v", branch, existing, resp.Error)
			}
			if creator.calls != 0 {
				t.Errorf("branch %q (existing=%v): creator ran %d times, want 0", branch, existing, creator.calls)
			}
		}
	}
}

func TestSessionCreateAcceptsOrdinaryBranch(t *testing.T) {
	for _, branch := range []string{"feat/x-1", "WTB-123-fix"} {
		creator := &fakeCreator{}
		resp := createOnFakeCreator(t, creator, map[string]any{
			"repo": "o/r", "branch": branch, "agent": "codex",
		})
		if resp.Error != nil {
			t.Fatalf("branch %q: %+v", branch, resp.Error)
		}
		if creator.cfg.Branch != branch {
			t.Fatalf("branch %q reached the creator as %q", branch, creator.cfg.Branch)
		}
	}
}
