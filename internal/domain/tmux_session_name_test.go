package domain

import "testing"

func TestSanitizeTmuxSessionName(t *testing.T) {
	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{
			// The regression: a dot made the session unaddressable, so terminate
			// left it running and the next create hit "duplicate session".
			name:   "dot becomes underscore, matching what tmux stores",
			branch: "WTB-1817-orderdao.getByStatus-returns-newest-first-missing-Scan",
			want:   "WTB-1817-orderdao_getByStatus-returns-newest-first-missing-Scan",
		},
		{
			name:   "slash becomes hyphen",
			branch: "feature/WTB-1234-thing",
			want:   "feature-WTB-1234-thing",
		},
		{
			name:   "colon becomes underscore",
			branch: "fix:WTB-1",
			want:   "fix_WTB-1",
		},
		{
			name:   "slash and dot together",
			branch: "feat/api.v2.handler",
			want:   "feat-api_v2_handler",
		},
		{
			name:   "plain name is untouched",
			branch: "WTB-1750-Remove-app-multisig-permissions",
			want:   "WTB-1750-Remove-app-multisig-permissions",
		},
		{
			name:   "empty stays empty",
			branch: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeTmuxSessionName(tt.branch); got != tt.want {
				t.Errorf("SanitizeTmuxSessionName(%q) = %q, want %q", tt.branch, got, tt.want)
			}
		})
	}
}

// A sanitized name must survive a second pass unchanged, otherwise discovery
// and termination would keep rewriting the name they are trying to match.
func TestSanitizeTmuxSessionNameIsIdempotent(t *testing.T) {
	for _, branch := range []string{
		"feat/api.v2",
		"WTB-1817-orderdao.getByStatus",
		"plain-name",
		"a:b.c/d",
	} {
		once := SanitizeTmuxSessionName(branch)
		if twice := SanitizeTmuxSessionName(once); twice != once {
			t.Errorf("not idempotent for %q: %q then %q", branch, once, twice)
		}
	}
}

// tmux target syntax is session:window.pane, so neither separator may survive.
func TestSanitizeTmuxSessionNameLeavesNoTargetSeparators(t *testing.T) {
	got := SanitizeTmuxSessionName("a.b:c/d.e")
	for _, bad := range []rune{'.', ':'} {
		for _, r := range got {
			if r == bad {
				t.Fatalf("sanitized name %q still contains %q", got, string(bad))
			}
		}
	}
}
