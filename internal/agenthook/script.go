package agenthook

import (
	"fmt"
	"os"
	"path/filepath"
)

const reporterName = "report-agent-session.sh"

// reporterScript is invoked by every vendor hook. It always exits 0 so a
// missing serve or a non-Aiman environment cannot fail the agent session.
// AIMAN_BIN_PATH is honoured when set but never relied on: it used to be
// injected by the session creator, which for the TUI is the laptop, so remote
// sessions received a laptop-only path and every hook report silently failed.
// Resolving the binary here works wherever the session actually runs.
const reporterScript = `#!/bin/sh
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
"$bin" session report-agent-session --from-stdin >/dev/null 2>&1 || true
exit 0
`

func reporterPath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", reporterName)
}

func ensureReporter(path string) (InstallResult, error) {
	res := InstallResult{Path: path}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && string(existing) == reporterScript:
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
	if err := os.WriteFile(path, []byte(reporterScript), 0o600); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: hook script must be executable by the agent
		return res, fmt.Errorf("chmod %s: %w", path, err)
	}
	return res, nil
}
