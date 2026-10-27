package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// EvidenceTimeout caps one run of verify.test_command.
	EvidenceTimeout = 15 * time.Minute
	// evidenceLines is how much of the command's output is kept.
	evidenceLines = 200
)

// errNoTestCommand makes an unverifiable review incomplete rather than a pass.
var errNoTestCommand = errors.New("no test command configured (verify.test_command)")

// Evidence is the result of the test command run by the manager, never the
// reviewer, on the reviewed snapshot.
type Evidence struct {
	Command  string    `json:"command"`
	ExitCode int       `json:"exit_code"`
	Output   string    `json:"output"` // last 200 lines, verbatim
	RanAt    time.Time `json:"ran_at"`
	TreeHash string    `json:"tree_hash"`
}

// runEvidence runs command in a fresh detached worktree at the review's
// snapshot commit, so nothing the reviewer did to its own worktree can make
// the run pass. A non-zero exit is evidence; a command that cannot start is
// an error.
func runEvidence(ctx context.Context, r Review, command string, now func() time.Time) (Evidence, error) {
	if strings.TrimSpace(command) == "" {
		return Evidence{}, errNoTestCommand
	}
	repo, _, _ := strings.Cut(filepath.Base(r.RepoWorktree), "@")
	path := filepath.Join(filepath.Dir(r.RepoWorktree), repo+"@evidence-"+r.ID[:8])
	if err := AddDetachedWorktree(ctx, r.RepoWorktree, path, r.Commit); err != nil {
		return Evidence{}, err
	}
	defer func() { _ = RemoveWorktree(context.WithoutCancel(ctx), r.RepoWorktree, path) }()

	runCtx, cancel := context.WithTimeout(ctx, EvidenceTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "bash", "-c", command) //nolint:gosec // the command comes from the operator's own verify config
	cmd.Dir = filepath.Join(path, r.Dir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	ev := Evidence{Command: command, RanAt: now(), TreeHash: r.Tree}
	err := cmd.Run()
	ev.Output = lastLines(out.String(), evidenceLines)
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		ev.ExitCode = exit.ExitCode()
	case err != nil:
		return Evidence{}, fmt.Errorf("run %q: %w", command, err)
	}
	return ev, nil
}

// lastLines keeps the last n lines of s, with its trailing newline.
func lastLines(s string, n int) string {
	trimmed := strings.TrimSuffix(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}
