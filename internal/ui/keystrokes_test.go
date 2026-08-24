package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pressKey builds the KeyPressMsg for a keystroke name so tests never
// hand-construct version-specific key literals. During the bubbletea v1→v2
// migration only this file changes; test bodies stay identical across both
// implementations.
//
// Supported names: "enter", "esc", "tab", "up", "down", "left", "right",
// "ctrl+<letter>", any printable text such as "a", "?", "hi there".
func pressKey(name string) tea.KeyPressMsg {
	name = strings.TrimSpace(name)
	if k, ok := specialKeyV2[name]; ok {
		return tea.KeyPressMsg{Mod: k.mod, Code: k.code}
	}
	runes := []rune(name)
	if len(runes) == 1 {
		return tea.KeyPressMsg{Code: runes[0], Text: name}
	}
	if name == "" {
		return tea.KeyPressMsg{}
	}
	return tea.KeyPressMsg{Text: name}
}

// pressRune is pressKey for callers that already hold a rune.
func pressRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

var specialKeyV2 = map[string]struct {
	mod  tea.KeyMod
	code rune
}{
	"enter":     {0, tea.KeyEnter},
	"esc":       {0, tea.KeyEsc},
	"tab":       {0, tea.KeyTab},
	"up":        {0, tea.KeyUp},
	"down":      {0, tea.KeyDown},
	"left":      {0, tea.KeyLeft},
	"right":     {0, tea.KeyRight},
	"space":     {0, tea.KeySpace},
	"pgup":      {0, tea.KeyPgUp},
	"pgdown":    {0, tea.KeyPgDown},
	"home":      {0, tea.KeyHome},
	"end":       {0, tea.KeyEnd},
	"delete":    {0, tea.KeyDelete},
	"insert":    {0, tea.KeyInsert},
	"backspace": {0, tea.KeyBackspace},
}

func init() {
	for i := 0; i < 26; i++ {
		name := "ctrl+" + string(rune('a'+i))
		specialKeyV2[name] = struct {
			mod  tea.KeyMod
			code rune
		}{tea.ModCtrl, rune('a' + i)}
	}
}
