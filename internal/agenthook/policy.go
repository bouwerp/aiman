package agenthook

import (
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/agent"
)

// GateMarker identifies the generated before-PR gate script.
const GateMarker = "verify-gate.sh"

// SupportsPreToolUse reports whether the agent exposes a pre-tool hook that
// can block a Bash call. Claude does; the others are advisory until their
// hook surface is verified.
func SupportsPreToolUse(agentName string) bool {
	command := strings.TrimSpace(agentName)
	if known, ok := agent.FindKnown(command); ok {
		command = agent.CommandBase(known.Command)
	} else {
		command = agent.CommandBase(command)
	}
	return command == "claude"
}

func gatePath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", GateMarker)
}

// verifyGateScript blocks `gh pr create` when aiman verify --gate fails.
const verifyGateScript = `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || exit 0
[ -n "${AIMAN_ID:-}" ] || exit 0
input=$(cat)
case "$input" in
  *"gh pr create"*) ;;
  *) exit 0 ;;
esac
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
"$bin" verify --gate
`

// upsertPreToolUseHook installs one marker-owned Bash gate group.
func upsertPreToolUseHook(root map[string]any, command string) bool {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	groups, _ := hooks["PreToolUse"].([]any)
	if len(groups) > 0 {
		found, changed := replaceMarkedCommand(groups, command, GateMarker)
		if found {
			return changed
		}
	}
	group := map[string]any{
		"matcher": "Bash",
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	}
	hooks["PreToolUse"] = append(groups, group)
	return true
}
