package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/infra/remotefile"
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
// remote when its content differs and marks it executable. WriteFile's mode is
// not the remote mode (local writes are 0600, ssh cat follows umask), so a
// written script needs the chmod: the session shell execs it directly. An
// unchanged script kept the mode it was given when it was written.
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
	changed, err := remotefile.WriteIfChanged(ctx, remote, path, []byte(agenthook.DeepcodeLaunchScript))
	if err != nil {
		return fmt.Errorf("deepcode launcher: %w", err)
	}
	if !changed {
		return nil
	}
	if _, err := remote.Execute(ctx, fmt.Sprintf("chmod 700 %q", path)); err != nil {
		return fmt.Errorf("deepcode launcher mode: %w", err)
	}
	return nil
}
