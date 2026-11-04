package vt10x

import "testing"

func dimCells(t *testing.T, input string) []bool {
	t.Helper()
	term := New()
	if _, err := term.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	out := make([]bool, 0, 2)
	for x := 0; x < 2; x++ {
		out = append(out, term.Cell(x, 0).Dim())
	}
	return out
}

func TestDimAttribute(t *testing.T) {
	cases := []struct {
		name, input string
		want        [2]bool
	}{
		{"unset by default", "ab", [2]bool{false, false}},
		{"SGR 2 sets", "\x1b[2mab", [2]bool{true, true}},
		{"SGR 22 clears", "\x1b[2ma\x1b[22mb", [2]bool{true, false}},
		{"SGR 0 clears", "\x1b[2ma\x1b[0mb", [2]bool{true, false}},
		{"bare SGR clears", "\x1b[2ma\x1b[mb", [2]bool{true, false}},
		{"inside a parameter list", "\x1b[38;5;3;2ma\x1b[22mb", [2]bool{true, false}},
		{"a colour's 2 selector is not dim", "\x1b[38;2;1;2;3mab", [2]bool{false, false}},
		{"colour change keeps dim", "\x1b[2ma\x1b[31mb", [2]bool{true, true}},
		{"SGR 21 keeps dim", "\x1b[2ma\x1b[21mb", [2]bool{true, true}},
		{"full reset clears", "\x1b[2ma\x1bcab", [2]bool{false, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dimCells(t, c.input)
			if got[0] != c.want[0] || got[1] != c.want[1] {
				t.Fatalf("dim = %v, want %v", got, c.want)
			}
		})
	}
}

// Normal intensity ends bold too: a red bold cell is stored bright (9), a red
// cell after SGR 22 is stored as plain red (1).
func TestSGR22ClearsBoldAndDim(t *testing.T) {
	term := New()
	if _, err := term.Write([]byte("\x1b[1;2;31ma\x1b[22mb")); err != nil {
		t.Fatal(err)
	}
	a, b := term.Cell(0, 0), term.Cell(1, 0)
	if !a.Dim() || a.FG != 9 {
		t.Fatalf("first cell: dim=%v fg=%d", a.Dim(), a.FG)
	}
	if b.Dim() || b.FG != 1 {
		t.Fatalf("second cell: dim=%v fg=%d", b.Dim(), b.FG)
	}
}
