package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// BootstrapResult reports what ApplyWorktreeBootstrap did. SetupLog is the
// absolute path of the background setup log, empty when no setup ran.
type BootstrapResult struct {
	Copied, Linked, Skipped []string
	SetupLog                string
}

// bootstrapScript builds one bash script that copies and links paths from
// mainRepo into worktree and starts setup detached. A missing source or an
// existing destination is reported as SKIPPED and never overwritten.
func bootstrapScript(mainRepo, worktree string, b config.RepoBootstrap) string {
	var sb strings.Builder
	sb.WriteString("set -e\n")
	fmt.Fprintf(&sb, "m=%s\nw=%s\n", shellQuote(mainRepo), shellQuote(worktree))

	for _, p := range b.Copy {
		p = filepath.Clean(p)
		q := shellQuote(p)
		fmt.Fprintf(&sb, `if [ -e "$m"/%[1]s ] && [ ! -e "$w"/%[1]s ] && [ ! -L "$w"/%[1]s ]; then
  mkdir -p "$(dirname "$w"/%[1]s)" && cp -a "$m"/%[1]s "$w"/%[1]s
  printf 'COPIED %%s\n' %[1]s
else
  printf 'SKIPPED %%s\n' %[1]s
fi
`, q)
	}

	if len(b.Symlink) > 0 {
		// A directory pattern such as "node_modules/" does not match a
		// symlink, so each link is excluded explicitly; the exclude file is
		// shared by every worktree of the repository.
		sb.WriteString(`ex="$(cd "$w" && cd "$(git rev-parse --git-common-dir)" && pwd)/info/exclude"
mkdir -p "$(dirname "$ex")"
`)
	}
	for _, p := range b.Symlink {
		p = filepath.Clean(p)
		q := shellQuote(p)
		fmt.Fprintf(&sb, `if [ -e "$m"/%[1]s ] && [ ! -e "$w"/%[1]s ] && [ ! -L "$w"/%[1]s ]; then
  mkdir -p "$(dirname "$w"/%[1]s)" && ln -s "$m"/%[1]s "$w"/%[1]s
  grep -qxF %[2]s "$ex" 2>/dev/null || printf '%%s\n' %[2]s >>"$ex"
  printf 'LINKED %%s\n' %[1]s
else
  printf 'SKIPPED %%s\n' %[1]s
fi
`, q, shellQuote("/"+filepath.ToSlash(p)))
	}

	if strings.TrimSpace(b.Setup) != "" {
		// The log lives in the worktree's git dir so it is never committed.
		// The redirections sit on the backgrounded group, not the inner
		// command: bash forks the whole "cd && ..." list, and that fork would
		// otherwise hold the caller's stdout open until setup exits.
		fmt.Fprintf(&sb, `log="$(git -C "$w" rev-parse --absolute-git-dir)/aiman-setup.log"
(cd "$w" && exec nohup setsid bash -lc %s) >"$log" 2>&1 </dev/null &
printf 'SETUP_LOG %%s\n' "$log"
`, shellQuote(b.Setup))
	}
	return sb.String()
}

// ApplyWorktreeBootstrap validates b and applies it to worktree in one
// Execute. Setup is started in the background and is not waited on.
func ApplyWorktreeBootstrap(ctx context.Context, remote domain.RemoteExecutor, mainRepo, worktree string, b config.RepoBootstrap) (BootstrapResult, error) {
	if err := b.Validate(); err != nil {
		return BootstrapResult{}, err
	}
	if len(b.Copy) == 0 && len(b.Symlink) == 0 && strings.TrimSpace(b.Setup) == "" {
		return BootstrapResult{}, nil
	}
	if mainRepo == "" || worktree == "" {
		return BootstrapResult{}, fmt.Errorf("bootstrap needs both the main checkout and the worktree path")
	}
	out, err := remote.Execute(ctx, bootstrapScript(mainRepo, worktree, b))
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("worktree bootstrap: %w: %s", err, strings.TrimSpace(out))
	}
	return parseBootstrapOutput(out), nil
}

func parseBootstrapOutput(out string) BootstrapResult {
	var res BootstrapResult
	for _, line := range strings.Split(out, "\n") {
		kind, rest, ok := strings.Cut(strings.TrimRight(line, "\r"), " ")
		if !ok {
			continue
		}
		switch kind {
		case "COPIED":
			res.Copied = append(res.Copied, rest)
		case "LINKED":
			res.Linked = append(res.Linked, rest)
		case "SKIPPED":
			res.Skipped = append(res.Skipped, rest)
		case "SETUP_LOG":
			res.SetupLog = rest
		}
	}
	return res
}
