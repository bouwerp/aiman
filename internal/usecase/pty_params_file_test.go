//go:build acceptance

package usecase

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellRemote stands in for ssh.Manager on a host whose home is a temp dir:
// Execute runs bash there, and WriteFile writes the way ssh.Manager does
// (`cat > %q` through a shell started in the home directory) under the usual
// sshd umask of 022.
type shellRemote struct{ home string }

func (r shellRemote) run(ctx context.Context, script string, stdin []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Dir = r.home
	cmd.Env = append(os.Environ(), "HOME="+r.home)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r shellRemote) Execute(ctx context.Context, cmd string) (string, error) {
	return r.run(ctx, "umask 022; "+cmd, nil)
}

func (r shellRemote) WriteFile(ctx context.Context, path string, content []byte) error {
	out, err := r.run(ctx, fmt.Sprintf("umask 022; cat > %q", path), content)
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// fakeAiman copies the params file it is handed, mode included, before
// CreatePTYSession's rm removes it.
const fakeAiman = `#!/bin/sh
[ "$1 $2 $3" = "pty create --params-file" ] || exit 2
printf '%s' "$4" > "$HOME/seen.path"
cp -p "$4" "$HOME/seen.json"
`

func installFakeAiman(t *testing.T, home string) {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "aiman"), []byte(fakeAiman), 0o700); err != nil { //nolint:gosec // an executable test double
		t.Fatal(err)
	}
}

// The params file carries the session env (secrets included), so it lives in
// a private directory under ~/.aiman/tmp and is readable by its owner only.
func TestCreatePTYSessionParamsFileIsPrivate(t *testing.T) {
	home := t.TempDir()
	installFakeAiman(t, home)
	spec := PTYSpec{ID: "sid", Dir: "/d", Command: "bash", Env: map[string]string{"TOKEN": "x"}}
	if err := CreatePTYSession(context.Background(), shellRemote{home: home}, spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	rawPath, err := os.ReadFile(filepath.Join(home, "seen.path")) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatalf("aiman pty create never saw a params file: %v", err)
	}
	path := string(rawPath)
	if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	// home is itself a temp dir (often under /tmp), so the check is that the
	// file sits in the home's private dir, not that the path avoids "/tmp".
	wantDir := filepath.Join(home, ".aiman", "tmp")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("params file %s, want it in %s (never the shared /tmp)", path, wantDir)
	}
	assertMode(t, filepath.Join(home, "seen.json"), 0o600)
	assertMode(t, wantDir, 0o700)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode %#o, want %#o", path, got, want)
	}
}
