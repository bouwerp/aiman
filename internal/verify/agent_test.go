package verify

import (
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestPickReviewer(t *testing.T) {
	installed := func(c string) bool { return c == "codex" || c == "claude" || c == "pi" }
	cases := []struct {
		name, worker, workerModel, override string
		agents                              []config.PreferredAgent
		want                                string
		err                                 error
	}{
		{
			name:   "first differing installed",
			agents: []config.PreferredAgent{{Agent: "claude"}, {Agent: "codex"}},
			worker: "Claude Code", want: "codex",
		},
		{
			name:   "skips not installed",
			agents: []config.PreferredAgent{{Agent: "grok"}, {Agent: "claude"}},
			worker: "codex", want: "claude",
		},
		{
			name: "override wins", agents: []config.PreferredAgent{{Agent: "codex"}},
			worker: "claude", override: "claude", want: "claude",
		},
		{
			name: "override not installed", worker: "claude", override: "grok",
			err: ErrNoReviewer,
		},
		{
			name: "none left", agents: []config.PreferredAgent{{Agent: "claude"}},
			worker: "claude", err: ErrNoReviewer,
		},
		{
			name: "no candidates", worker: "claude", err: ErrNoReviewer,
		},
		{
			name: "same binary on another model reviews",
			agents: []config.PreferredAgent{
				{Agent: "pi", Model: "kimi-k3"},
			},
			worker: "pi", workerModel: "glm-5.3", want: "pi",
		},
		{
			name: "same binary and model is skipped",
			agents: []config.PreferredAgent{
				{Agent: "pi", Model: "glm-5.3"},
				{Agent: "pi", Model: "kimi-k3"},
			},
			worker: "pi", workerModel: "glm-5.3", want: "pi",
		},
		{
			name: "a bare entry keeps the binary-only rule",
			agents: []config.PreferredAgent{
				{Agent: "pi"}, {Agent: "claude"},
			},
			worker: "pi", want: "claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PickReviewer(tc.agents, tc.worker, tc.workerModel, tc.override, installed)
			if got.Agent != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got %+v, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

// The picked entry's model and effort travel with it, so the reviewer runs
// the model the smart tier named.
func TestPickReviewerCarriesModel(t *testing.T) {
	installed := func(c string) bool { return c == "pi" }
	got, err := PickReviewer([]config.PreferredAgent{
		{Agent: "codex"},
		{Agent: "pi", Model: "kimi-k3", Effort: "high"},
	}, "claude", "", "", installed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "kimi-k3" || got.Effort != "high" {
		t.Fatalf("pick = %+v", got)
	}
}
