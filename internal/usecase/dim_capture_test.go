package usecase

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// Dim only changes the bytes of a capture. Every reader of the text has to
// reach the same answer with and without it, so each pane here renders twice
// through the real emulator: once as the agent drew it and once with SGR 2 and
// 22 removed.
func TestCaptureReadersIgnoreDim(t *testing.T) {
	const prompt = "Review PR 439 and report what you find"
	frame := "\x1b[2m────────────────────\x1b[22m\r\n"
	panes := map[string]string{
		"idle with a suggestion": "• Done\r\n" + frame +
			"❯\u00a0\x1b[2mTry \"fix lint errors\"\x1b[22m\r\n" + frame +
			"\x1b[2m  ? for shortcuts\x1b[22m",
		"typed prompt in the composer": "• Done\r\n" + frame +
			"❯\u00a0" + prompt + "\r\n" + frame +
			"\x1b[2m  ? for shortcuts\x1b[22m",
		"submitted prompt above a suggestion": "❯ " + prompt + "\r\n" +
			"• Working\r\n" + frame +
			"❯\u00a0\x1b[2m" + prompt + " later\x1b[22m\r\n" + frame,
		"working": "\x1b[2m✻ Brewing… (12s · \x1b[22mesc\x1b[2m to interrupt)\x1b[22m\r\n" + frame +
			"❯\u00a0",
		"menu": "Allow this?\r\n\x1b[2m❯ 1. Yes\x1b[22m\r\n  2. No\r\n",
	}
	strip := strings.NewReplacer("\x1b[2m", "", "\x1b[22m", "")
	for name, raw := range panes {
		t.Run(name, func(t *testing.T) {
			dimmed := ptyruntime.RenderScreen([]byte(raw), 80, 12)
			plain := ptyruntime.RenderScreen([]byte(strip.Replace(raw)), 80, 12)
			if !strings.Contains(dimmed, "\x1b[2m") {
				t.Fatalf("the capture should carry dim: %q", dimmed)
			}
			if strings.Contains(plain, "\x1b[2m") {
				t.Fatalf("the plain capture carries dim: %q", plain)
			}
			if a, b := pane.StripANSI(dimmed), pane.StripANSI(plain); a != b {
				t.Errorf("StripANSI: %q vs %q", a, b)
			}
			if a, b := pane.Clean(dimmed), pane.Clean(plain); a != b {
				t.Errorf("Clean: %q vs %q", a, b)
			}
			a := pane.Classify(pane.Observation{Pane: dimmed, SinceOutput: -1})
			b := pane.Classify(pane.Observation{Pane: plain, SinceOutput: -1})
			if a.State != b.State || a.Reason != b.Reason {
				t.Errorf("Classify: %+v vs %+v", a, b)
			}
			marker := submitMarker(prompt)
			if composerStillHolds(dimmed, marker) != composerStillHolds(plain, marker) {
				t.Errorf("composerStillHolds differs: %q vs %q", dimmed, plain)
			}
			if PromptStillInComposer(dimmed, prompt) != PromptStillInComposer(plain, prompt) {
				t.Errorf("PromptStillInComposer differs")
			}
			if a, b := ptyruntime.TightenBottomGap(dimmed), ptyruntime.TightenBottomGap(plain); pane.StripANSI(a) != pane.StripANSI(b) {
				t.Errorf("TightenBottomGap: %q vs %q", a, b)
			}
		})
	}
}

// A dim suggestion that happens to equal the prompt still reads as held, the
// same as plain text: the check reads visible text only.
func TestComposerCheckSeesThroughDim(t *testing.T) {
	const prompt = "fix lint errors"
	dimmed := ptyruntime.RenderScreen([]byte("❯\u00a0\x1b[2m"+prompt+"\x1b[22m"), 40, 3)
	if !composerStillHolds(dimmed, submitMarker(prompt)) {
		t.Fatalf("visible text not found in %q", dimmed)
	}
}
