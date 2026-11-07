//go:build acceptance

package shellx

import (
	"os/exec"
	"strings"
	"testing"
)

var nastyInputs = []string{
	"",
	"plain",
	"two words",
	"  leading and trailing  ",
	"it's",
	"'",
	"''",
	`"double"`,
	`back\slash`,
	"$(echo expanded)",
	"${HOME}",
	"$HOME",
	"`id`",
	"a;b",
	"a && b || c",
	"a | b > c < d",
	"line1\nline2",
	"tab\there",
	"glob * ? [a]",
	"~user",
	"-n",
	"--flag=value",
	"#comment",
	"!bang",
	"unicode: héllo wörld 日本 🚀",
	"x';echo injected;'",
}

// Every input must come back byte for byte after bash parses the quoted word.
func TestQuoteRoundTripsThroughBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	dir := t.TempDir() // a broken quoter's redirections land here
	for _, in := range nastyInputs {
		script := "printf %s " + Quote(in)
		cmd := exec.Command(bash, "-c", script) // #nosec G204 -- the point of the test
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("Quote(%q) = %q: bash failed: %v", in, Quote(in), err)
			continue
		}
		if string(out) != in {
			t.Errorf("Quote(%q) = %q: bash printed %q", in, Quote(in), out)
		}
	}
}

// The word is single-quoted, so nothing inside it is expanded.
func TestQuoteIsSingleQuoted(t *testing.T) {
	for _, in := range []string{"plain", "two words", "$(id)", "it's"} {
		q := Quote(in)
		if !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
			t.Errorf("Quote(%q) = %q, want a single-quoted word", in, q)
		}
	}
	if got := Quote(""); got != "''" {
		t.Errorf("Quote(\"\") = %q, want ''", got)
	}
}
