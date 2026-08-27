package agenthook

import (
	"fmt"
	"path/filepath"
)

const (
	ActionInstalled = "installed"
	ActionUpdated   = "updated"
	ActionCurrent   = "current"
)

// InstallResult is one path after EnsureOnHost.
type InstallResult struct {
	Path   string
	Action string
}

// EnsureOnHost writes the reporter script and, when the agent config directory
// already exists, registers a SessionStart (or equivalent) hook. Missing agent
// dirs are skipped so unused tools are not given a config file. Ageni is not
// installed: it has no hook system.
func EnsureOnHost(home string) ([]InstallResult, error) {
	var out []InstallResult
	var first error
	record := func(res InstallResult, err error) {
		if err != nil {
			if first == nil {
				first = err
			}
			return
		}
		if res.Path != "" {
			out = append(out, res)
		}
	}

	script := reporterPath(home)
	record(ensureReporter(script))
	record(ensureClaude(home, script))
	record(ensureGrok(home, script))
	record(ensureCursor(home, script))
	record(ensureCodex(home, script))
	record(ensureCopilot(home, script))
	record(ensureGemini(home, script))
	record(ensureKilo(home))
	record(ensurePi(home))
	return out, first
}

func ensureClaude(home, command string) (InstallResult, error) {
	dir := filepath.Join(home, ".claude")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	return upsertJSONFile(filepath.Join(dir, "settings.json"), func(root map[string]any) bool {
		return upsertIdentityHooks(root, command)
	})
}

func ensureGrok(home, command string) (InstallResult, error) {
	if !isDir(filepath.Join(home, ".grok")) {
		return InstallResult{}, nil
	}
	return writeJSONFile(filepath.Join(home, ".grok", "hooks", "aiman.json"), claudeOwnedDoc(command))
}

func ensureCursor(home, command string) (InstallResult, error) {
	dir := filepath.Join(home, ".cursor")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	return upsertJSONFile(filepath.Join(dir, "hooks.json"), func(root map[string]any) bool {
		return upsertCursorEvent(root, command)
	})
}

func ensureCodex(home, command string) (InstallResult, error) {
	dir := filepath.Join(home, ".codex")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	return upsertJSONFile(filepath.Join(dir, "hooks.json"), func(root map[string]any) bool {
		return upsertIdentityHooks(root, command)
	})
}

func ensureCopilot(home, command string) (InstallResult, error) {
	dir := filepath.Join(home, ".copilot")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	return upsertJSONFile(filepath.Join(dir, "settings.json"), func(root map[string]any) bool {
		return upsertIdentityHooks(root, command)
	})
}

func ensureGemini(home, command string) (InstallResult, error) {
	if !isDir(filepath.Join(home, ".gemini")) {
		return InstallResult{}, nil
	}
	return writeJSONFile(filepath.Join(home, ".gemini", "config", "hooks", "aiman.json"), claudeOwnedDoc(command))
}

func ensureKilo(home string) (InstallResult, error) {
	dir := filepath.Join(home, ".config", "kilo")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	return writeTextFile(filepath.Join(dir, "plugin", "aiman-native-session.js"), kiloPlugin, 0o600)
}

func ensurePi(home string) (InstallResult, error) {
	if !isDir(filepath.Join(home, ".pi")) {
		return InstallResult{}, nil
	}
	return writeTextFile(filepath.Join(home, ".pi", "agent", "extensions", "aiman-native-session.ts"), piExtension, 0o600)
}

// Summarize is a one-line serve log of ensure results.
func Summarize(results []InstallResult) string {
	nInst, nUpd, nCur := 0, 0, 0
	for _, r := range results {
		switch r.Action {
		case ActionInstalled:
			nInst++
		case ActionUpdated:
			nUpd++
		default:
			nCur++
		}
	}
	return fmt.Sprintf("installed %d, updated %d, current %d", nInst, nUpd, nCur)
}
