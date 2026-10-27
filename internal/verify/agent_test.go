package verify

import (
	"errors"
	"testing"
)

func TestPickReviewer(t *testing.T) {
	installed := func(c string) bool { return c == "codex" || c == "claude" }
	cases := []struct {
		name, worker, override, want string
		agents                       []string
		err                          error
	}{
		{name: "first differing installed", agents: []string{"claude", "codex"}, worker: "Claude Code", want: "codex"},
		{name: "skips not installed", agents: []string{"grok", "claude"}, worker: "codex", want: "claude"},
		{name: "override wins", agents: []string{"codex"}, worker: "claude", override: "claude", want: "claude"},
		{name: "override not installed", agents: nil, worker: "claude", override: "grok", err: ErrNoReviewer},
		{name: "none left", agents: []string{"claude"}, worker: "claude", err: ErrNoReviewer},
		{name: "no candidates", agents: nil, worker: "claude", err: ErrNoReviewer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PickReviewer(tc.agents, tc.worker, tc.override, installed)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}
