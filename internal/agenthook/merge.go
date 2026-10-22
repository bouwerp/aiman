package agenthook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func upsertJSONFile(path string, mutate func(map[string]any) bool) (InstallResult, error) {
	res := InstallResult{Path: path}
	root := map[string]any{}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if trimmed := bytes.TrimSpace(existing); len(trimmed) > 0 {
			if uerr := json.Unmarshal(trimmed, &root); uerr != nil {
				return res, fmt.Errorf("parsing %s: %w", path, uerr)
			}
		}
		res.Action = ActionUpdated
	case os.IsNotExist(err):
		res.Action = ActionInstalled
	default:
		return res, fmt.Errorf("reading %s: %w", path, err)
	}
	if !mutate(root) {
		if err == nil {
			res.Action = ActionCurrent
			return res, nil
		}
		// Missing file and mutate added nothing: do not create an empty config.
		return InstallResult{}, nil
	}
	body, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return res, err
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return res, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	return res, nil
}

func writeJSONFile(path string, doc any) (InstallResult, error) {
	res := InstallResult{Path: path}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, err
	}
	body = append(body, '\n')
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && bytes.Equal(existing, body):
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
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	return res, nil
}

func writeTextFile(path, contents string, mode os.FileMode) (InstallResult, error) {
	res := InstallResult{Path: path}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && string(existing) == contents:
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
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		return res, fmt.Errorf("writing %s: %w", path, err)
	}
	return res, nil
}

func hasMarker(command, marker string) bool {
	return marker != "" && strings.Contains(command, marker)
}

func replaceMarkedCommand(v any, command, marker string) (found bool, changed bool) {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["command"].(string); ok && hasMarker(s, marker) {
			found = true
			if s != command {
				t["command"] = command
				changed = true
			}
			return found, changed
		}
		for _, child := range t {
			f, c := replaceMarkedCommand(child, command, marker)
			found = found || f
			changed = changed || c
		}
		return found, changed
	case []any:
		for _, child := range t {
			f, c := replaceMarkedCommand(child, command, marker)
			found = found || f
			changed = changed || c
		}
		return found, changed
	default:
		return false, false
	}
}

func commandHasMarker(v any, marker string) bool {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["command"].(string); ok && hasMarker(s, marker) {
			return true
		}
		for _, child := range t {
			if commandHasMarker(child, marker) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if commandHasMarker(child, marker) {
				return true
			}
		}
	}
	return false
}

func groupsWithMarker(arr []any, marker string) []map[string]any {
	var out []map[string]any
	for _, item := range arr {
		group, ok := item.(map[string]any)
		if !ok || !commandHasMarker(group, marker) {
			continue
		}
		out = append(out, group)
	}
	return out
}

// rewriteMatcher sets matcher on groups already selected by marker.
// An empty matcher clears one that no longer applies.
func rewriteMatcher(groups []map[string]any, matcher string) bool {
	changed := false
	for _, group := range groups {
		current, _ := group["matcher"].(string)
		if current == matcher {
			continue
		}
		if matcher == "" {
			delete(group, "matcher")
		} else {
			group["matcher"] = matcher
		}
		changed = true
	}
	return changed
}

func claudeHookGroup(command string) map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	}
}

func upsertClaudeHook(root map[string]any, event, command, matcher, marker string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	arr, _ := hooks[event].([]any)
	marked := groupsWithMarker(arr, marker)
	if len(marked) > 0 {
		_, changed := replaceMarkedCommand(arr, command, marker)
		if rewriteMatcher(marked, matcher) {
			changed = true
		}
		hooks[event] = arr
		return changed
	}
	group := claudeHookGroup(command)
	if matcher != "" {
		group["matcher"] = matcher
	}
	hooks[event] = append(arr, group)
	return true
}

// upsertTurnHooks reports Stop and UserPromptSubmit through the session reporter.
func upsertTurnHooks(root map[string]any, command string) bool {
	stop := upsertClaudeHook(root, "Stop", command, "", Marker)
	submit := upsertClaudeHook(root, "UserPromptSubmit", command, "", Marker)
	return stop || submit
}

// upsertTodoHook registers one PostToolUse group for the to-do reporter.
func upsertTodoHook(root map[string]any, command, matcher string) bool {
	return upsertClaudeHook(root, "PostToolUse", command, matcher, TodoMarker)
}

// museHookCommand recovers AIMAN_ID when Muse's hook environment allowlist
// drops it. Launch writes .aiman_session_id in the working directory.
func museHookCommand(reporter string) string {
	return fmt.Sprintf(
		`id="${AIMAN_ID:-}"; if [ -z "$id" ]; then d="${PWD:-}"; while [ -n "$d" ] && [ "$d" != / ]; do if [ -f "$d/.aiman_session_id" ]; then id=$(tr -d '[:space:]' < "$d/.aiman_session_id"); break; fi; d=$(dirname "$d"); done; fi; [ -n "$id" ] || exit 0; AIMAN_ENV=1 AIMAN_ID="$id" exec %s`,
		strconv.Quote(reporter),
	)
}

func upsertMuseIdentityHooks(root map[string]any, command string) bool {
	changed := false
	for _, event := range []string{"SessionStart", "SessionEnd", "Stop", "UserPromptSubmit", "Notification"} {
		if upsertClaudeHook(root, event, command, "*", Marker) {
			changed = true
		}
	}
	return changed
}

func upsertIdentityHooks(root map[string]any, command string) bool {
	a := upsertClaudeHook(root, "SessionStart", command, "", Marker)
	b := upsertClaudeHook(root, "SessionEnd", command, "", Marker)
	c := upsertClaudeHook(root, "Notification", command, "idle_prompt", Marker)
	return a || b || c
}

func cursorCommandEntry(command string) map[string]any {
	return map[string]any{"command": command}
}

// upsertCursorEvent accepts either Cursor's top-level sessionStart array or
// Claude-style hooks.SessionStart (Grok also loads ~/.cursor/hooks.json).
func upsertCursorEvent(root map[string]any, command string) bool {
	if _, ok := root["hooks"]; ok {
		return upsertIdentityHooks(root, command)
	}
	a := upsertTopLevelCommand(root, "sessionStart", command)
	b := upsertTopLevelCommand(root, "sessionEnd", command)
	return a || b
}

func upsertTopLevelCommand(root map[string]any, event, command string) bool {
	arr, _ := root[event].([]any)
	found, changed := replaceMarkedCommand(arr, command, Marker)
	if found {
		root[event] = arr
		return changed
	}
	root[event] = append(arr, cursorCommandEntry(command))
	return true
}

func claudeOwnedDoc(command string) map[string]any {
	root := map[string]any{}
	upsertIdentityHooks(root, command)
	return root
}
