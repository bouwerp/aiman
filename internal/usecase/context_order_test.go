package usecase

import (
	"context"
	"strings"
	"testing"
)

type packRemote struct{ pack string }

func (r *packRemote) Execute(_ context.Context, cmd string) (string, error) {
	if strings.Contains(cmd, "context pack") {
		return `{"text":"` + r.pack + `"}`, nil
	}
	return "", nil
}
func (r *packRemote) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

// Agents act on the first instruction they read and treat what follows as
// detail, so a task ahead of the pointer to earlier notes gets started before
// the notes are opened — which is the whole point of writing them.
func TestInjectSharedContextPutsTheContextPointerFirst(t *testing.T) {
	r := &packRemote{pack: "# Shared context\nsome earlier notes"}
	got := InjectSharedContext(context.Background(), r, "/w", "grp", "org/repo", "Implement WTB-1912.")

	ctxAt := strings.Index(got, ContextPackPrompt)
	taskAt := strings.Index(got, "Implement WTB-1912.")
	if ctxAt < 0 || taskAt < 0 {
		t.Fatalf("both parts should survive: %q", got)
	}
	if ctxAt > taskAt {
		t.Errorf("the context pointer must come first: %q", got)
	}
}

// Nothing to inject means the prompt is handed back untouched.
func TestInjectSharedContextLeavesThePromptAloneWithoutAPack(t *testing.T) {
	r := &packRemote{pack: ""}
	if got := InjectSharedContext(context.Background(), r, "/w", "grp", "org/repo", "Do the thing."); got != "Do the thing." {
		t.Errorf("got %q", got)
	}
}
