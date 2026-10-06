package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/agenthook"
)

// remoteWriter is the slice of a remote that can drop the Deep Code launcher
// on the host that will run it. Session create does this itself: the script is
// also written when serve starts, and a serve process from before that write
// leaves the path missing.
type remoteWriter interface {
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

// InstallDeepcodeLauncher writes ~/.aiman/hooks/deepcode-launch.sh on the
// remote and marks it executable. WriteFile's mode is not the remote mode
// (local writes are 0600, ssh cat follows umask), so the chmod is required:
// the session shell execs the script directly.
func InstallDeepcodeLauncher(ctx context.Context, remote remoteWriter) error {
	if remote == nil {
		return fmt.Errorf("deepcode launcher: no remote")
	}
	homeOut, err := remote.Execute(ctx, `printf %s "$HOME"`)
	if err != nil {
		return fmt.Errorf("deepcode launcher home: %w", err)
	}
	home := strings.TrimSpace(homeOut)
	if home == "" || strings.ContainsAny(home, "\r\n") {
		return fmt.Errorf("deepcode launcher: empty home")
	}
	path := filepath.Join(home, ".aiman", "hooks", "deepcode-launch.sh")
	if _, err := remote.Execute(ctx, fmt.Sprintf("mkdir -p %q", filepath.Dir(path))); err != nil {
		return fmt.Errorf("deepcode launcher dir: %w", err)
	}
	if err := remote.WriteFile(ctx, path, []byte(agenthook.DeepcodeLaunchScript)); err != nil {
		return fmt.Errorf("deepcode launcher: %w", err)
	}
	if _, err := remote.Execute(ctx, fmt.Sprintf("chmod 700 %q", path)); err != nil {
		return fmt.Errorf("deepcode launcher mode: %w", err)
	}
	return nil
}
