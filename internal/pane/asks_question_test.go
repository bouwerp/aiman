package pane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dialogDir = "../usecase/testdata/bootdialogs"

// frame loads a recorded screen from testdata/frames, ANSI and NBSP included.
func frame(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "frames", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// replaceLast swaps the last occurrence of old, so a recorded frame's final
// prose row can be turned into a question without touching its chrome.
func replaceLast(t *testing.T, s, old, repl string) string {
	t.Helper()
	i := strings.LastIndex(s, old)
	if i < 0 {
		t.Fatalf("%q not in frame", old)
	}
	return s[:i] + repl + s[i+len(old):]
}

func TestAsksQuestionOnRecordedFrames(t *testing.T) {
	const q = "Should I migrate now or keep the shim?"
	for _, tc := range []struct {
		agent, file, lastProse string
	}{
		{"claude, under a right-aligned update notice and Brewed marker", "claude_finished", "queued"},
		{"codex, under a Worked marker", "codex_finished", "queued"},
		{"grok, in a box beside a scrollbar", "grok_finished", "queued"},
		{"muse, under a rule", "muse_finished", "done\x1b[38;2;125;126;127;49m\""},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			screen := frame(t, tc.file)
			if AsksQuestion(screen) {
				t.Fatal("the recorded frame ends in a statement")
			}
			if !AsksQuestion(replaceLast(t, screen, tc.lastProse, q)) {
				t.Fatal("a final question was not seen")
			}
		})
	}
}

func TestAsksQuestionNeedsAComposer(t *testing.T) {
	if AsksQuestion(replaceLast(t, frame(t, "pi_finished"), "queued", "Should I go on?")) {
		t.Fatal("Pi draws no composer glyph, so nothing can be claimed")
	}
	if AsksQuestion("Should I go on?") {
		t.Fatal("a screen with no composer is not asking")
	}
	if AsksQuestion("") {
		t.Fatal("an empty screen is not asking")
	}
}

func TestAsksQuestionIgnoresTheComposerItself(t *testing.T) {
	for _, tc := range []struct{ name, screen string }{
		{"typed text", "● done\n\n────\n❯ should we do this?\n────\n  -- INSERT --"},
		{"dim suggestion", "● done\n\n────\n❯ \x1b[2mWant me to continue?\x1b[22m\n────\n  -- INSERT --"},
		{"recorded typed text, wrapped", replaceLast(t, frame(t, "claude_typed_long"), "done.", "done?")},
		{"recorded text typed while busy", replaceLast(t, frame(t, "claude_typed_while_busy"), "queued.", "queued?")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if AsksQuestion(tc.screen) {
				t.Fatal("composer text counted as the agent's question")
			}
		})
	}
}

func TestAsksQuestionWalksUpToTheFirstProseRow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		want   bool
	}{
		{"plain", "● Want me to continue?\n\n────\n❯ \n────", true},
		{"no space after the glyph", "● Which do you prefer?\n❯", true},
		{"codex composer", "  Want me to continue?\n\n› Ask Codex to do anything\n\n  ? for shortcuts", true},
		{"styled question", "\x1b[1m● Want me to continue?\x1b[0m\n❯ ", true},
		{"finished marker", "● Want me to continue?\n\n✻ Baked for 2s · done 13:45\n\n────\n❯ ", true},
		{"finished marker with minutes", "● Want me to continue?\n\n✻ Churned for 1m 12s\n\n────\n❯ ", true},
		{"box rules", "● Want me to continue?\n╭───╮\n│ ❯ │\n╰───╯", true},
		{"statement above a question", "● Want me to continue?\n● Carrying on.\n❯ ", false},
		{"question further up", "● Want me to continue?\n\n● No, I decided.\n\n✻ Baked for 2s\n❯ ", false},
		{"question mark inside a row", "● Is it? It is.\n❯ ", false},
		{"an echoed prompt is not the agent asking", "● ok\n\n❯ should we do this?\n\n────\n❯ ", false},
		{"a prose row naming a duration is prose", "● I worked for 5s, want more?\n❯ ", true},
		{"only chrome above", "────\n❯ \n────", false},
		{"footer below the composer does not count", "● Done.\n❯ \n  anything else?", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AsksQuestion(tc.screen); got != tc.want {
				t.Fatalf("AsksQuestion = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMenuOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen string
		want   bool
	}{
		{"claude permission menu", "Do you want to proceed?\n❯ 1. Yes\n  2. No\n\n Esc to cancel · Tab to amend", true},
		{"second row selected", "Pick one\n  1. Yes\n❯ 2. No", true},
		{"last row selected, neighbour above", "  1. a\n  2. b\n› 3. c\n", true},
		{"codex marker", "› 1. Trust and continue\n  2. Quit", true},
		{"parenthesis numbering", "❯ 1) Yes\n  2) No", true},
		{"nbsp after the marker", "❯ 1. Yes\n  2. No", true},
		{"styled", "\x1b[36m❯ 1. Yes\x1b[0m\n\x1b[2m  2. No\x1b[22m", true},
		{"boxed", "│ ❯ 1. Yes  │\n│   2. No   │", true},
		{"composer text alone", "● done\n────\n❯ 1. foo\n────\n  -- INSERT --", false},
		{"composer text beside prose", "❯ 1. foo\n  bar baz", false},
		{"blockquote", "> 1. item", false},
		{"blockquote with a following row", "> 1. item\n  2. item", false},
		{"blockquote with a following quoted row", "> 1. item\n> 2. item", false},
		{"numbered list with no selected row", "  1. first\n  2. second", false},
		{"marker with no number", "❯ yes\n  2. No", false},
		{"not directly beside", "❯ 1. Yes\n\n  2. No", false},
		{"description rows between options", "Which library?\n\n❯ 1. Option A\n     What A costs\n  2. Option B\n     What B costs\n  3. Type something.\n Enter to select · Esc to cancel", true},
		{"description rows above the selected option", "  1. Option A\n     What A costs\n     and more\n❯ 2. Option B\n     What B costs", true},
		{"long menu past the old tail", "❯ 1. a\n     a\n  2. b\n     b\n  3. c\n     c\n  4. d\n     d\n  5. e\n     e\n  6. f\n     f\n  7. g\n Enter to select", true},
		{"unindented prose stops the walk", "❯ 1. foo\nsome prose\n  2. bar", false},
		{"numbered list typed in the composer", "● ok\n────\n❯ 1. foo\n  2. bar\n────\n  -- INSERT --", false},
		{"composer below a quoted menu", "❯ 1. Yes\n  2. No\n● ok\n────\n❯ \n────", false},
		{"empty", "", false},
		{"menu scrolled far above", "❯ 1. Yes\n  2. No\n" + strings.Repeat("output\n", 30), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := MenuOpen(tc.screen)
			if ok != tc.want {
				t.Fatalf("MenuOpen = %v (%q), want %v", ok, reason, tc.want)
			}
			if ok && reason == "" {
				t.Fatal("a match must name a reason")
			}
		})
	}
}

func TestMenuOpenOnRecordedDialogs(t *testing.T) {
	for _, name := range []string{"codex_update.txt", "codex_hooks_review.txt"} {
		b, err := os.ReadFile(filepath.Join(dialogDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := MenuOpen(string(b)); !ok {
			t.Errorf("%s: menu not seen", name)
		}
	}
	for _, name := range []string{"claude_finished", "claude_queued", "codex_finished", "grok_finished", "muse_finished", "pi_finished"} {
		if reason, ok := MenuOpen(frame(t, name)); ok {
			t.Errorf("%s: no menu on screen, got %q", name, reason)
		}
	}
}
