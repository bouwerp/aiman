package usecase

import (
	"reflect"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestResolveWorktreeAgentCandidates(t *testing.T) {
	tests := []struct {
		name    string
		session domain.Session
		hints   []string
		want    []string
	}{
		{
			name:    "known agent name wins with no hints",
			session: domain.Session{AgentName: "Claude Code"},
			want:    []string{"Claude Code"},
		},
		{
			name:  "hints alone resolve to candidates",
			hints: []string{"Claude Sonnet 5 <noreply@anthropic.com>", "Codex <noreply@openai.com>"},
			want:  []string{"Claude Code", "Codex CLI"},
		},
		{
			name:    "known agent plus a different hint both appear, deduped",
			session: domain.Session{AgentName: "Claude Code"},
			hints:   []string{"Claude Sonnet 5 <noreply@anthropic.com>", "Codex <noreply@openai.com>"},
			want:    []string{"Claude Code", "Codex CLI"},
		},
		{
			name:  "unrecognizable hints yield no candidates",
			hints: []string{"Jane Doe <jane@example.com>"},
			want:  nil,
		},
		{
			name: "no signal at all yields no candidates",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveWorktreeAgentCandidates(tt.session, tt.hints)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
