package agenthook

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	ActionInstalled = "installed"
	ActionUpdated   = "updated"
	ActionCurrent   = "current"
)

// claudeTodoMatcher selects the to-do tools whose full list the reporter mirrors.
// Tasks stay available from Claude; aiman does not set CLAUDE_CODE_ENABLE_TASKS.
const claudeTodoMatcher = "TodoWrite|TaskCreate|TaskUpdate"

// InstallResult is one path after EnsureOnHost.
type InstallResult struct {
	Path   string
	Action string
}

// EnsureOnHost writes the session reporter and the to-do reporter and, when an
// agent config directory already exists, registers that agent's hooks. Missing
// agent dirs are skipped so unused tools are not given a config file. Ageni is
// not installed: it has no hook system.
func EnsureOnHost(home string, beforePR ...bool) ([]InstallResult, error) {
	policy := len(beforePR) > 0 && beforePR[0]
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
	record(ensureHookScript(script, reporterScript))
	record(ensureHookScript(todoReporterPath(home), hookScript("todo report --from-stdin")))
	if policy {
		record(ensureHookScript(gatePath(home), verifyGateScript))
	} else if err := os.Remove(gatePath(home)); err != nil && !os.IsNotExist(err) {
		record(InstallResult{}, err)
	}
	record(ensureDeepcodeLauncher(home))
	record(ensureClaude(home, script, policy))
	record(ensureGrok(home, script))
	record(ensureCursor(home, script))
	record(ensureCodex(home, script))
	record(ensureCopilot(home, script))
	record(ensureGemini(home, script))
	record(ensureKilo(home))
	record(ensurePi(home))
	record(ensureMuse(home))
	return out, first
}

func ensureClaude(home, command string, beforePR bool) (InstallResult, error) {
	dir := filepath.Join(home, ".claude")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	todo := todoReporterPath(home)
	return upsertJSONFile(filepath.Join(dir, "settings.json"), func(root map[string]any) bool {
		identity := upsertIdentityHooks(root, command)
		compact := upsertCompactHook(root, command)
		turns := upsertTurnHooks(root, command)
		todos := upsertTodoHook(root, todo, claudeTodoMatcher)
		gate := false
		if beforePR {
			gate = upsertPreToolUseHook(root, gatePath(home))
		} else {
			gate = removePreToolUseHook(root)
		}
		return identity || compact || turns || todos || gate
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
		identity := upsertIdentityHooks(root, command)
		compact := upsertCompactHook(root, command)
		turns := upsertTurnHooks(root, command)
		return identity || compact || turns
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

func ensureMuse(home string) (InstallResult, error) {
	dir := filepath.Join(home, ".config", "muse")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	return upsertJSONFile(filepath.Join(dir, "settings.json"), func(root map[string]any) bool {
		changed := false
		if _, ok := root["schema_version"]; !ok {
			// A settings file without this key fails every muse command.
			root["schema_version"] = 1
			changed = true
		}
		return upsertMuseIdentityHooks(root, museHookCommand(reporterPath(home))) || changed
	})
}

func ensureDeepcodeLauncher(home string) (InstallResult, error) {
	return writeTextFile(filepath.Join(home, ".aiman", "hooks", "deepcode-launch.sh"), DeepcodeLaunchScript, 0o700)
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
