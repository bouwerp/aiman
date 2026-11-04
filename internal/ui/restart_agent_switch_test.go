package ui

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/local"
)

func TestWorkspaceStatusReportsMissingTools(t *testing.T) {
	root := t.TempDir()
	main := git.ComputeMainRepoPath(root, "o/r")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	msg := workspaceStatus(context.Background(), local.NewExecutor(""), root, "o/r", []string{"sh", "no-such-tool-xyz"}, "claude")
	if !msg.exists || msg.path != main {
		t.Fatalf("status %+v", msg)
	}
	if !slices.Equal(msg.missing, []string{"no-such-tool-xyz"}) {
		t.Fatalf("missing %v", msg.missing)
	}

	msg = workspaceStatus(context.Background(), local.NewExecutor(""), root, "o/r", nil, "claude")
	if len(msg.missing) != 0 {
		t.Fatalf("missing %v with no requires", msg.missing)
	}
}
