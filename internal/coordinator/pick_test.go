package coordinator

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestPickAgentSkipsMissing(t *testing.T) {
	got, err := PickAgent(config.PreferredFromCommands("codex", "claude"), []string{"claude", "grok"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" {
		t.Fatalf("PickAgent = %+v, want claude", got)
	}
}

func TestPickAgentNoneInstalled(t *testing.T) {
	_, err := PickAgent(config.PreferredFromCommands("codex", "claude", "codex"), []string{"agy"})
	if !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("err = %v, want ErrNoPreferredAgent", err)
	}
	msg := err.Error()
	i, j := strings.Index(msg, "codex"), strings.Index(msg, "claude")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("error %q must list codex then claude", msg)
	}
	if strings.Count(msg, "codex") != 1 {
		t.Fatalf("error %q repeats codex", msg)
	}
}

func TestPickAgentTrimsAndIsCaseSensitive(t *testing.T) {
	got, err := PickAgent(config.PreferredFromCommands(" ", " Claude", " claude "), []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" {
		t.Fatalf("PickAgent = %+v, want claude", got)
	}
	if _, err := PickAgent(nil, []string{"claude"}); !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("empty list: err = %v, want ErrNoPreferredAgent", err)
	}
}

// The same agent may appear more than once; the first installed entry wins,
// and its model and effort come with it.
func TestPickAgentDuplicateAgentsKeepOrder(t *testing.T) {
	preferred := []config.PreferredAgent{
		{Agent: "codex", Model: "gpt-5", Effort: "high"},
		{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"},
		{Agent: "pi", Model: "deepseek/deepseek-flash"},
		{Agent: "claude"},
	}
	got, err := PickAgent(preferred, []string{"pi", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := config.PreferredAgent{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"}
	if got != want {
		t.Fatalf("PickAgent = %+v, want %+v", got, want)
	}
}
