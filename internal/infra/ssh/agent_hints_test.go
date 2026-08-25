package ssh

import (
	"strings"
	"testing"
)

func TestWorktreeCoAuthorHintsCmd(t *testing.T) {
	if got := WorktreeCoAuthorHintsCmd(nil); got != ":" {
		t.Errorf("empty paths: got %q, want a no-op command", got)
	}

	cmd := WorktreeCoAuthorHintsCmd([]string{"/repos/app@feature", "/repos/app's worktree"})
	if !strings.Contains(cmd, `'/repos/app@feature'`) {
		t.Errorf("expected the plain path quoted, got: %s", cmd)
	}
	if !strings.Contains(cmd, `'/repos/app'\''s worktree'`) {
		t.Errorf("expected the single-quote-containing path escaped, got: %s", cmd)
	}
	if !strings.Contains(cmd, "Co-authored-by") {
		t.Errorf("expected the git trailer key in the command, got: %s", cmd)
	}
	if !strings.Contains(cmd, hintRecordTag) {
		t.Errorf("expected the %s tag in the command, got: %s", hintRecordTag, cmd)
	}
}

func TestParseWorktreeCoAuthorHints(t *testing.T) {
	out := strings.Join([]string{
		"HINT\t/repos/app@feature\tClaude Sonnet 5 <noreply@anthropic.com>",
		"HINT\t/repos/app@feature\tCodex <noreply@openai.com>",
		"HINT\t/repos/other\tClaude Sonnet 5 <noreply@anthropic.com>",
		"some unrelated stderr noise",
		"HINT\tincomplete",
		"",
	}, "\n")

	got := ParseWorktreeCoAuthorHints(out)

	if len(got["/repos/app@feature"]) != 2 {
		t.Fatalf("expected 2 hints for app@feature, got %+v", got["/repos/app@feature"])
	}
	if len(got["/repos/other"]) != 1 || got["/repos/other"][0] != "Claude Sonnet 5 <noreply@anthropic.com>" {
		t.Fatalf("expected 1 hint for other, got %+v", got["/repos/other"])
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 worktrees with hints, got %+v", got)
	}
}

func TestParseWorktreeCoAuthorHints_EmptyInput(t *testing.T) {
	got := ParseWorktreeCoAuthorHints("")
	if len(got) != 0 {
		t.Fatalf("expected no hints, got %+v", got)
	}
}
