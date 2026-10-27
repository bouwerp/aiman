package coordinator

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
)

// errNoEvidence is a report that cites neither a repo path nor a test command.
var errNoEvidence = errors.New("report cites no repo-relative file path and no test command")

// reportTestCommands extends usage.DefaultTestCommands with other common
// runners a child may name in its report.
var reportTestCommands = append(append([]string(nil), usage.DefaultTestCommands...),
	"make ci", "cargo test", "npm run test", "pnpm test", "yarn test", "bun test")

// ValidateReport checks a child's .aiman_report.md for evidence: at least one
// repo-relative file path or one test command. It checks for citations, not
// whether they are true.
func ValidateReport(md string) error {
	lower := strings.ToLower(md)
	for _, cmd := range reportTestCommands {
		if strings.Contains(lower, cmd) {
			return nil
		}
	}
	for _, tok := range strings.Fields(md) {
		if isRepoPath(strings.Trim(tok, "`'\"()[]{}<>,.;:*")) {
			return nil
		}
	}
	return errNoEvidence
}

// isRepoPath reports whether tok looks like a path inside a repository: a
// relative path with a directory and a file extension, not a URL.
func isRepoPath(tok string) bool {
	if !strings.Contains(tok, "/") || strings.Contains(tok, "://") ||
		strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~") {
		return false
	}
	ext := path.Ext(tok)
	return len(ext) > 1 && path.Base(tok) != ext
}

// reportMarker separates login-shell noise from the report in ExecReport's
// output.
const reportMarker = "__AIMAN_REPORT__"

// Executor runs a shell command on the host that holds the child's worktree.
type Executor interface {
	Execute(ctx context.Context, cmd string) (string, error)
}

// ExecReport returns a Tracker.Report that reads the child's report from its
// working directory (else its worktree) through x. A missing file reads as
// empty, which ValidateReport rejects.
func ExecReport(x Executor) func(ctx context.Context, child domain.Session) (string, error) {
	return func(ctx context.Context, child domain.Session) (string, error) {
		dir := child.WorkingDirectory
		if dir == "" {
			dir = child.WorktreePath
		}
		if dir == "" {
			return "", errors.New("session has no working directory")
		}
		file := path.Join(dir, domain.AimanReportFileName)
		out, err := x.Execute(ctx, "echo "+reportMarker+"; cat -- '"+strings.ReplaceAll(file, "'", `'\''`)+"' 2>/dev/null || true")
		if err != nil {
			return "", err
		}
		_, report, ok := strings.Cut(out, reportMarker+"\n")
		if !ok {
			return "", errors.New("report read: marker missing from output")
		}
		return report, nil
	}
}
