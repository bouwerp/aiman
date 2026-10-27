package agenthook

import (
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/agent"
)

// GateMarker identifies the generated before-PR gate script.
const GateMarker = "verify-gate.sh"

// GuardsPRCreate reports whether the agent has a PreToolUse hook that can
// block a Bash call. Claude does; the others are advisory until their hook
// surface is verified.
func GuardsPRCreate(agentName string) bool {
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
  *"gh pr create"*|*"gh pr new"*|*"gh -R "*" pr create"*|*"gh -R "*" pr new"*|*"gh --repo "*" pr create"*|*"gh --repo "*" pr new"*) ;;
  *) exit 0 ;;
esac
bin="${AIMAN_BIN_PATH:-}"
if [ -z "$bin" ] || [ ! -x "$bin" ]; then
  if command -v aiman >/dev/null 2>&1; then
    bin=aiman
  elif [ -x "$HOME/.local/bin/aiman" ]; then
    bin="$HOME/.local/bin/aiman"
  else
    echo "aiman verify gate: aiman binary not found" >&2
    exit 2
  fi
fi
if command -v timeout >/dev/null 2>&1; then
  timeout 15 "$bin" verify --gate
else
  "$bin" verify --gate
fi
`

// removePreToolUseHook drops marker-owned Bash gate groups.
func removePreToolUseHook(root map[string]any) bool {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return false
	}
	groups, _ := hooks["PreToolUse"].([]any)
	kept := make([]any, 0, len(groups))
	changed := false
	for _, group := range groups {
		if commandHasMarker(group, GateMarker) {
			changed = true
			continue
		}
		kept = append(kept, group)
	}
	if !changed {
		return false
	}
	if len(kept) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = kept
	}
	return true
}

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
