package ptyruntime

import "testing"

func TestFitsClient(t *testing.T) {
	if !FitsClient(`"$HOME/.aiman/hooks/deepcode-launch.sh"`) {
		t.Fatal("deep code launcher")
	}
	if !FitsClient("Deep Code") {
		t.Fatal("display name")
	}
	if FitsClient("muse --trust-workspace --yolo") {
		t.Fatal("muse")
	}
}

func TestSizesWithClient(t *testing.T) {
	for _, name := range []string{
		"muse --trust-workspace --yolo",
		"Muse Code",
		"codex --dangerously-bypass-approvals-and-sandbox",
		"Codex CLI",
		"Deep Code",
		`"$HOME/.aiman/hooks/deepcode-launch.sh"`,
	} {
		if !SizesWithClient(name) {
			t.Errorf("%q should be sized to the client", name)
		}
	}
	for _, name := range []string{"claude", "Claude Code", "grok", "Grok Build CLI", ""} {
		if SizesWithClient(name) {
			t.Errorf("%q should keep the size it already has", name)
		}
	}
}

func TestSizeDiffers(t *testing.T) {
	if SizeDiffers("155x40", 155, 40) {
		t.Fatal("a same-size change nudges Muse and Codex into a second layout")
	}
	if !SizeDiffers("80x24", 155, 40) {
		t.Fatal("leaving 80x24 is a real resize")
	}
	if !SizeDiffers("", 80, 24) {
		t.Fatal("an unknown size has to be applied")
	}
	if SizeDiffers("80x24", 0, 24) {
		t.Fatal("an unusable size must not be applied")
	}
}
