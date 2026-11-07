package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSessionCreateRefusesInvalidBase(t *testing.T) {
	creator := &fakeCreator{}
	resp := createOnFakeCreator(t, creator, map[string]any{
		"repo": "o/r", "branch": "feat/x", "base": "--upload-pack=x", "agent": "codex",
	})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want invalid_params, got %+v", resp.Error)
	}
	if creator.calls != 0 {
		t.Fatalf("creator ran %d times", creator.calls)
	}
}

type bashExec struct{ dir string }

func (b bashExec) Execute(ctx context.Context, cmd string) (string, error) {
	c := exec.CommandContext(ctx, "bash", "-c", cmd) // #nosec G204 -- the point of the test
	c.Dir = b.dir
	out, err := c.CombinedOutput()
	return string(out), err
}

// A stored base and directory reach a shell; neither may run anything.
func TestDiffStatDoesNotExpandBaseOrDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x$(touch dir-ran)")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	diffStat(context.Background(), bashExec{root}, dir, "$(touch base-ran)")
	for _, f := range []string{"dir-ran", "base-ran"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			t.Errorf("%s: the shell ran it", f)
		}
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s: the shell ran it inside the directory", f)
		}
	}
}
