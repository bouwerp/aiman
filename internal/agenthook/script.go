package agenthook

import (
	"fmt"
	"os"
	"path/filepath"
)

const reporterName = "report-agent-session.sh"

const todoReporterName = "todo-report"

// hookScript is invoked by vendor hooks. It always exits 0 so a missing serve
// or a non-Aiman environment cannot fail the agent session.
// AIMAN_BIN_PATH is honoured when it points at an executable, but the script
// resolves aiman itself otherwise: a session creator may inject a path from a
// different machine than the one where the session runs.
func hookScript(subcommand string) string {
	return `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || exit 0
[ -n "${AIMAN_ID:-}" ] || exit 0
bin="${AIMAN_BIN_PATH:-}"
if [ -z "$bin" ] || [ ! -x "$bin" ]; then
  if command -v aiman >/dev/null 2>&1; then
    bin=aiman
  elif [ -x "$HOME/.local/bin/aiman" ]; then
    bin="$HOME/.local/bin/aiman"
  else
    exit 0
  fi
fi
"$bin" ` + subcommand + ` >/dev/null 2>&1 || true
exit 0
`
}

// reporterScript is the session-lifecycle hook (identity and turn events).
var reporterScript = hookScript("session report-agent-session --from-stdin")

func reporterPath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", reporterName)
}

func todoReporterPath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", todoReporterName)
}

func ensureHookScript(path, script string) (InstallResult, error) {
	res := InstallResult{Path: path}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && string(existing) == script:
		res.Action = ActionCurrent
		return res, nil
	case err == nil:
		res.Action = ActionUpdated
	case os.IsNotExist(err):
		res.Action = ActionInstalled
	default:
		return res, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return res, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: hook script must be executable by the agent
		return res, fmt.Errorf("chmod %s: %w", path, err)
	}
	return res, nil
}
