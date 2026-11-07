package shellx

import (
	"os/exec"
	"testing"
)

func TestQuoteHomePath(t *testing.T) {
	t.Setenv("HOME", "/tmp/h o'me")
	cases := map[string]string{
		"$HOME/.aiman/tmp/x.json": "/tmp/h o'me/.aiman/tmp/x.json",
		"~/a b/$(x)":              "/tmp/h o'me/a b/$(x)",
		"/abs/$HOME/x":            "/abs/$HOME/x",
		"plain":                   "plain",
	}
	for in, want := range cases {
		out, err := exec.Command("bash", "-c", "printf %s "+QuoteHomePath(in)).Output()
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(out) != want {
			t.Errorf("QuoteHomePath(%q) expands to %q, want %q", in, out, want)
		}
	}
}
