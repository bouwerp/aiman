package ptyhold

import (
	"slices"
	"strings"
	"testing"
)

func TestWithTerminalEnvAddsUTF8WhenMissing(t *testing.T) {
	got := withTerminalEnv([]string{"HOME=/tmp", "PATH=/bin"})
	if !slices.Contains(got, "TERM="+defaultTERM) {
		t.Fatalf("missing TERM: %v", got)
	}
	if !slices.Contains(got, "COLORTERM=truecolor") {
		t.Fatalf("missing COLORTERM: %v", got)
	}
	if !slices.Contains(got, "LANG="+defaultUTF8Locale) {
		t.Fatalf("missing UTF-8 locale: %v", got)
	}
}

func TestWithTerminalEnvKeepsExistingUTF8Locale(t *testing.T) {
	got := withTerminalEnv([]string{"LANG=en_US.UTF-8", "TERM=xterm-256color"})
	if !slices.Contains(got, "LANG=en_US.UTF-8") {
		t.Fatalf("should keep LANG: %v", got)
	}
	if slices.Contains(got, "LANG="+defaultUTF8Locale) {
		t.Fatalf("should not add a second LANG: %v", got)
	}
}

func TestWithTerminalEnvReplacesNonUTF8Locale(t *testing.T) {
	got := withTerminalEnv([]string{"LANG=C", "LC_ALL=POSIX"})
	if slices.Contains(got, "LANG=C") || slices.Contains(got, "LC_ALL=POSIX") {
		t.Fatalf("non-UTF-8 locale should be dropped: %v", got)
	}
	if !slices.Contains(got, "LANG="+defaultUTF8Locale) {
		t.Fatalf("missing replacement locale: %v", got)
	}
}

func TestResizeNudgeWhenSizeUnchanged(t *testing.T) {
	cases := []struct {
		cols, rows, curCols, curRows int
		wantCols, wantRows           int
		want                         bool
	}{
		{80, 24, 100, 30, 0, 0, false},
		{80, 24, 80, 24, 79, 24, true},
		{1, 24, 1, 24, 1, 23, true},
		{1, 1, 1, 1, 2, 1, true},
	}
	for _, tc := range cases {
		gotCols, gotRows, ok := resizeNudge(tc.cols, tc.rows, tc.curCols, tc.curRows)
		if ok != tc.want || (ok && (gotCols != tc.wantCols || gotRows != tc.wantRows)) {
			t.Errorf("resizeNudge(%d,%d, cur %d,%d) = %d,%d,%v want %d,%d,%v",
				tc.cols, tc.rows, tc.curCols, tc.curRows, gotCols, gotRows, ok, tc.wantCols, tc.wantRows, tc.want)
		}
	}
}

func TestChildShellForcesTERMAfterLoginProfile(t *testing.T) {
	got := childShell("codex --dangerously-bypass-approvals-and-sandbox")
	// bash -l sources the profile before this script. Codex 0.150 refuses
	// to start its TUI when TERM is still dumb, then the holder drops to
	// `exec bash -i` — which is the bare terminal operators attach to.
	termAt := strings.Index(got, "TERM="+defaultTERM)
	cmdAt := strings.Index(got, "codex ")
	if termAt < 0 || cmdAt < 0 || termAt > cmdAt {
		t.Fatalf("TERM must be forced after login profile and before the agent, got %q", got)
	}
	if !strings.Contains(got, "exec bash -i") {
		t.Fatalf("missing fallback shell: %q", got)
	}
}

func TestWithTerminalEnvDropsDumbTERM(t *testing.T) {
	got := withTerminalEnv([]string{"TERM=dumb"})
	if slices.Contains(got, "TERM=dumb") {
		t.Fatal("TERM=dumb should be dropped")
	}
	if !slices.Contains(got, "TERM="+defaultTERM) {
		t.Fatalf("missing replacement TERM: %v", got)
	}
}
