package agenthook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func hasMarker(command string) bool {
	return strings.Contains(command, Marker)
}

func replaceMarkedCommand(v any, command string) (found bool, changed bool) {
	switch t := v.(type) {
	case map[string]any:
		if s, ok := t["command"].(string); ok && hasMarker(s) {
			found = true
			if s != command {
				t["command"] = command
				changed = true
			}
			return found, changed
		}
		for _, child := range t {
			f, c := replaceMarkedCommand(child, command)
			found = found || f
			changed = changed || c
		}
		return found, changed
	case []any:
		for _, child := range t {
			f, c := replaceMarkedCommand(child, command)
			found = found || f
			changed = changed || c
		}
		return found, changed
	default:
		return false, false
	}
}

func claudeHookGroup(command string) map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	}
}

func upsertClaudeHook(root map[string]any, event, command, matcher string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	arr, _ := hooks[event].([]any)
	found, changed := replaceMarkedCommand(arr, command)
	if found {
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

func upsertIdentityHooks(root map[string]any, command string) bool {
	a := upsertClaudeHook(root, "SessionStart", command, "")
	b := upsertClaudeHook(root, "SessionEnd", command, "")
	c := upsertClaudeHook(root, "Notification", command, "idle_prompt")
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
	found, changed := replaceMarkedCommand(arr, command)
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
