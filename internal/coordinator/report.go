package coordinator

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
)

// errNoEvidence is a report whose Evidence section cites neither a repo path
// nor a test command.
var errNoEvidence = errors.New("report cites no repo-relative file path and no test command")

// reportTestCommands extends usage.DefaultTestCommands with other common
// runners a child may name in its report.
var reportTestCommands = append(append([]string(nil), usage.DefaultTestCommands...),
	"make ci", "cargo test", "npm run test", "pnpm test", "yarn test", "bun test")

// errNoSurprises and errNoEvidenceSection name a report heading that is
// absent.
var (
	errNoSurprises       = errors.New("report has no ## Surprises section; add it, with \"none\" if nothing affects other pieces")
	errNoEvidenceSection = errors.New("report has no ## Evidence section; list each command run with its exit code")
)

// ValidateReport checks a child's .aiman_report.md: it needs `## Evidence`
// and `## Surprises` sections, and the Evidence section must cite at least
// one repo-relative file path or one test command. It checks for citations,
// not whether they are true.
func ValidateReport(md string) error {
	evidence, hasEvidence := reportSection(md, "Evidence")
	_, hasSurprises := reportSection(md, "Surprises")
	switch {
	case !hasEvidence:
		return errNoEvidenceSection
	case !hasSurprises:
		return errNoSurprises
	}
	lower := strings.ToLower(evidence)
	for _, cmd := range reportTestCommands {
		if strings.Contains(lower, cmd) {
			return nil
		}
	}
	for _, tok := range strings.Fields(evidence) {
		if isRepoPath(strings.Trim(tok, "`'\"()[]{}<>,.;:*")) {
			return nil
		}
	}
	return errNoEvidence
}

// ReportSurprises returns the text of the report's `## Surprises` section and
// whether it says anything: "none", "n/a" and their punctuated or bulleted
// forms are not surprises.
func ReportSurprises(md string) (string, bool) {
	text, ok := reportSection(md, "Surprises")
	if !ok {
		return "", false
	}
	norm := strings.ToLower(strings.Trim(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text), "-*")), " .!"))
	if norm == "" || norm == "none" || norm == "n/a" {
		return "", false
	}
	return text, true
}

// reportSection returns the trimmed body of the `## name` section, up to the
// next `## ` heading.
func reportSection(md, name string) (string, bool) {
	var body strings.Builder
	in, found := false, false
	for _, line := range strings.Split(md, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "## "); ok {
			in = strings.EqualFold(strings.TrimSpace(h), name)
			found = found || in
			continue
		}
		if in {
			body.WriteString(line + "\n")
		}
	}
	return strings.TrimSpace(body.String()), found
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
