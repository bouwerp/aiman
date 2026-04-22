package pane

import (
	"strings"
	"testing"
)

// ── cleanLines tests ────────────────────────────────────────────────────────

func TestCleanLines_AnsiStrip(t *testing.T) {
	input := "\x1b[32mhello\x1b[0m world"
	got := cleanLines(input)
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestCleanLines_DupCollapse(t *testing.T) {
	input := "$ make build\n$ make build\n$ make build"
	got := cleanLines(input)
	if got != "$ make build [×3]" {
		t.Errorf("got %q, want %q", got, "$ make build [×3]")
	}
}

func TestCleanLines_BlankCollapse(t *testing.T) {
	input := "a\n\n\n\nb"
	got := cleanLines(input)
	if got != "a\n\nb" {
		t.Errorf("got %q", got)
	}
}

// ── structuralCompress / isNoise tests ─────────────────────────────────────

func TestStructuralCompress_KeepsCmdLine(t *testing.T) {
	out := structuralCompress("$ go build ./...\nsome noise line")
	if !strings.Contains(out, "$ go build") {
		t.Error("expected cmd line to be kept")
	}
}

func TestStructuralCompress_KeepsGoError(t *testing.T) {
	out := structuralCompress("internal/ui/dashboard.go:4880:49: undefined field")
	if !strings.Contains(out, "dashboard.go") {
		t.Error("expected build diagnostic to be kept")
	}
}

func TestStructuralCompress_KeepsPanic(t *testing.T) {
	out := structuralCompress("goroutine 1 [running]:\nmain.main()\npanic: runtime error: index out of range")
	if !strings.Contains(out, "panic") {
		t.Error("expected panic line to be kept")
	}
}

func TestStructuralCompress_KeepsTestFail(t *testing.T) {
	out := structuralCompress("--- FAIL: TestFoo (0.01s)\nFAIL\tok  github.com/x/y/pkg  0.023s")
	if !strings.Contains(out, "FAIL: TestFoo") {
		t.Error("expected test failure to be kept")
	}
}

func TestStructuralCompress_KeepsGitLine(t *testing.T) {
	out := structuralCompress(" internal/ui/snapshot_browser.go |  47 +++++")
	if !strings.Contains(out, "snapshot_browser.go") {
		t.Error("expected git diff stat line to be kept")
	}
}

// ── Noise suppression tests ─────────────────────────────────────────────────

func TestIsNoise_NpmWarnDeprecated(t *testing.T) {
	cases := []string{
		"npm warn deprecated bluebird@3.7.2: See https://github.com/...",
		"npm warn deprecated graceful-fs@1.2.3: please upgrade",
		"npm warn EBADENGINE Unsupported engine",
	}
	for _, c := range cases {
		if !isNoise(c) {
			t.Errorf("expected noise: %q", c)
		}
	}
}

func TestIsNoise_GoModDownload(t *testing.T) {
	cases := []string{
		"go: downloading github.com/user/repo v1.2.3",
		"go: extracting github.com/user/repo v1.2.3",
		"go: finding module providing github.com/user/repo",
	}
	for _, c := range cases {
		if !isNoise(c) {
			t.Errorf("expected noise: %q", c)
		}
	}
}

func TestIsNoise_CargoBuildProgress(t *testing.T) {
	cases := []string{
		"   Compiling serde v1.0.200",
		"   Downloaded tokio v1.36.0",
		"   Fetching serde v1.0.200",
	}
	for _, c := range cases {
		if !isNoise(c) {
			t.Errorf("expected noise: %q", c)
		}
	}
}

func TestIsNoise_PipProgress(t *testing.T) {
	cases := []string{
		"Collecting requests>=2.28.0",
		"  Downloading requests-2.31.0-py3-none-any.whl",
		"Installing collected packages: requests, certifi",
		"Successfully installed requests-2.31.0",
	}
	for _, c := range cases {
		if !isNoise(c) {
			t.Errorf("expected noise: %q", c)
		}
	}
}

func TestIsNoise_Timestamps(t *testing.T) {
	cases := []string{
		"2024-01-15T12:34:56 INFO starting server",
		"2024-01-15 12:34:56 DEBUG handler called",
		"[2024-01-15 12:34:56] request processed",
	}
	for _, c := range cases {
		if !isNoise(c) {
			t.Errorf("expected noise: %q", c)
		}
	}
}

func TestIsNoise_ProgressBars(t *testing.T) {
	cases := []string{
		"[=============================>    ]  90%",
		"downloading........................................",
		"  45% =========>",
	}
	for _, c := range cases {
		if !isNoise(c) {
			t.Errorf("expected noise: %q", c)
		}
	}
}

// ── Noise should NOT suppress real errors ───────────────────────────────────

func TestNoise_DoesNotSuppressRealErrors(t *testing.T) {
	// A real Go build error should never be noise.
	if isNoise("internal/ui/dashboard.go:4880:49: m.foo undefined") {
		t.Error("build diagnostic should not be noise")
	}
	if isNoise("panic: runtime error: index out of range [0] with length 0") {
		t.Error("panic should not be noise")
	}
}

// ── warn: tightened regex tests ─────────────────────────────────────────────

func TestScError_WarnRequiresLineStart(t *testing.T) {
	// "warn" buried in the middle of a log line should NOT be a signal
	// (since it would have been noise-suppressed anyway), but we verify
	// the tightened regex doesn't accidentally keep junk lines.
	keep := []string{
		"warning: unused variable `x` [-Wunused-variable]",
		"warn: config key deprecated",
		"WARNING: foo bar baz",
	}
	for _, l := range keep {
		if !scError.MatchString(l) {
			t.Errorf("expected scError to match %q", l)
		}
	}

	noKeep := []string{
		// "warn" embedded inside a word or URL – should NOT be a signal line on its own
		"download.example.com/reward/badge",
	}
	for _, l := range noKeep {
		if scError.MatchString(l) {
			t.Errorf("expected scError NOT to match %q", l)
		}
	}
}

// ── Full pipeline integration test ─────────────────────────────────────────

func TestClean_Integration(t *testing.T) {
	input := strings.Join([]string{
		"$ npm install",
		"npm warn deprecated bluebird@3.7.2: use native promises",
		"npm warn deprecated graceful-fs@1.2.3: update your package",
		"npm warn deprecated core-js@2.6.12: ",
		"added 1234 packages in 45s",
		"",
		"$ go build ./...",
		"go: downloading github.com/charmbracelet/bubbletea v0.25.0",
		"go: downloading github.com/charmbracelet/lipgloss v0.9.1",
		"",
		"$ go test ./...",
		"ok  \tgithub.com/bouwerp/aiman/internal/pane\t0.023s",
		"--- FAIL: TestFoo (0.01s)",
		"    clean_test.go:42: got foo, want bar",
		"FAIL\tgithub.com/bouwerp/aiman/internal/usecase\t0.045s",
	}, "\n")

	got := Clean(input)

	// Should keep
	must := []string{
		"$ npm install",
		"$ go build",
		"$ go test",
		"ok  \tgithub.com/bouwerp/aiman/internal/pane",
		"--- FAIL: TestFoo",
		"FAIL\tgithub.com/bouwerp/aiman/internal/usecase",
	}
	for _, s := range must {
		if !strings.Contains(got, s) {
			t.Errorf("expected %q in output\ngot:\n%s", s, got)
		}
	}

	// Should NOT keep
	mustNot := []string{
		"bluebird",
		"graceful-fs",
		"core-js",
		"go: downloading",
	}
	for _, s := range mustNot {
		if strings.Contains(got, s) {
			t.Errorf("expected %q NOT in output\ngot:\n%s", s, got)
		}
	}
}
