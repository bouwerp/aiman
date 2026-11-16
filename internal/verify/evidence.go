package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/cigate"
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
func runEvidence(ctx context.Context, r Review, command string, d Deps) (Evidence, error) {
	return runAt(ctx, r, r.Commit, "evidence", command, d)
}

// runAt runs command in a throwaway detached worktree at commit, beside the
// worker's as <repo>@<label>-<id8>, and removes it afterwards. With Deps.Launch
// set the command runs in its own scope unit (cigate.VerifyUnit), and with
// Deps.TmpDir set it gets TMPDIR=<TmpDir>/verify-<id8>, set on the command's
// environment only and removed when it ends.
func runAt(ctx context.Context, r Review, commit, label, command string, d Deps) (Evidence, error) {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	if strings.TrimSpace(command) == "" {
		return Evidence{}, errNoTestCommand
	}
	repo, _, _ := strings.Cut(filepath.Base(r.RepoWorktree), "@")
	path := filepath.Join(filepath.Dir(r.RepoWorktree), repo+"@"+label+"-"+r.ID[:8])
	if err := AddDetachedWorktree(ctx, r.RepoWorktree, path, commit); err != nil {
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
	if d.TmpDir != "" {
		tmp, release, err := cigate.ClaimTmp(d.TmpDir, "verify-"+r.ID[:8])
		if err != nil {
			return Evidence{}, fmt.Errorf("tmpdir: %w", err)
		}
		defer release()
		cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	}
	ev := Evidence{Command: command, RanAt: now(), TreeHash: r.Tree}
	if d.Launch != nil {
		d.Launch(cigate.VerifyUnit(r.ID, ev.RanAt), cmd)
	}
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
