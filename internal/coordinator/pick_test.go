package coordinator

import (
	"errors"
	"strings"
	"testing"
)

func TestPickAgentSkipsMissing(t *testing.T) {
	got, err := PickAgent([]string{"codex", "claude"}, []string{"claude", "grok"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "claude" {
		t.Fatalf("PickAgent = %q, want claude", got)
	}
}

func TestPickAgentNoneInstalled(t *testing.T) {
	_, err := PickAgent([]string{"codex", "claude"}, []string{"agy"})
	if !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("err = %v, want ErrNoPreferredAgent", err)
	}
	msg := err.Error()
	i, j := strings.Index(msg, "codex"), strings.Index(msg, "claude")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("error %q must list codex then claude", msg)
	}
}

func TestPickAgentTrimsAndIsCaseSensitive(t *testing.T) {
	got, err := PickAgent([]string{" ", " Claude", " claude "}, []string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "claude" {
		t.Fatalf("PickAgent = %q, want claude", got)
	}
	if _, err := PickAgent(nil, []string{"claude"}); !errors.Is(err, ErrNoPreferredAgent) {
		t.Fatalf("empty list: err = %v, want ErrNoPreferredAgent", err)
	}
}
