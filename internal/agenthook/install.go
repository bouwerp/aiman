package agenthook

import (
	"errors"
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

// grokTodoMatcher selects grok's todo_write; the Claude alias name stays in
// case a grok release starts reporting it.
const grokTodoMatcher = "todo_write|TodoWrite"

// InstallResult is one path after EnsureOnHost.
type InstallResult struct {
	Path   string
	Action string
}

// Options selects the optional parts of EnsureOnHostWith.
type Options struct {
	// BeforePR installs the verify gate on Claude's PR creation.
	BeforePR bool
	// ClaudeStatusline wraps Claude's status line; off restores it.
	ClaudeStatusline bool
}

// EnsureOnHost is EnsureOnHostWith with only the before-PR option.
func EnsureOnHost(home string, beforePR ...bool) ([]InstallResult, error) {
	return EnsureOnHostWith(home, Options{BeforePR: len(beforePR) > 0 && beforePR[0]})
}

// EnsureOnHostWith writes the session reporter and the to-do reporter and, when
// an agent config directory already exists, registers that agent's hooks.
// Missing agent dirs are skipped so unused tools are not given a config file.
// Ageni is not installed: it has no hook system.
func EnsureOnHostWith(home string, opts Options) ([]InstallResult, error) {
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
	if opts.BeforePR {
		record(ensureHookScript(gatePath(home), verifyGateScript))
	} else if err := os.Remove(gatePath(home)); err != nil && !os.IsNotExist(err) {
		record(InstallResult{}, err)
	}
	record(ensureDeepcodeLauncher(home))
	record(ensureClaude(home, script, opts))
	record(ensureGrok(home, script, todoReporterPath(home)))
	record(ensureCursor(home, script))
	record(ensureCodex(home, script))
	record(ensureCopilot(home, script))
	record(ensureGemini(home, script))
	record(ensureKilo(home))
	record(ensurePi(home))
	record(ensureMuse(home))
	return out, first
}

// aimanBin is the binary the status line wrapper records through: the running
// one while its path still exists, else the install path.
func aimanBin(home string) string {
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	return filepath.Join(home, ".local", "bin", "aiman")
}

// ensureClaude folds the hook and status line edits into one settings write. A
// status line failure is returned after the other hook changes are written.
func ensureClaude(home, command string, opts Options) (InstallResult, error) {
	dir := filepath.Join(home, ".claude")
	if !isDir(dir) {
		return InstallResult{}, nil
	}
	todo := todoReporterPath(home)
	var statusErr error
	editStatusLine := UnwrapStatusLine
	if opts.ClaudeStatusline {
		editStatusLine = WrapStatusLine
		if _, err := WriteStatusLineWrapper(home, aimanBin(home)); err != nil {
			statusErr = err
			editStatusLine = nil
		}
	}
	res, err := upsertJSONFile(filepath.Join(dir, "settings.json"), func(root map[string]any) bool {
		identity := upsertIdentityHooks(root, command)
		compact := upsertCompactHook(root, command)
		turns := upsertTurnHooks(root, command)
		todos := upsertTodoHook(root, todo, claudeTodoMatcher)
		gate := false
		if opts.BeforePR {
			gate = upsertPreToolUseHook(root, gatePath(home))
		} else {
			gate = removePreToolUseHook(root)
		}
		status := false
		if editStatusLine != nil {
			status, statusErr = editStatusLine(home, root)
		}
		return identity || compact || turns || todos || gate || status
	})
	// On any error settings may still point at the script, so it stays.
	if !opts.ClaudeStatusline && err == nil && statusErr == nil {
		if rerr := os.Remove(statusLineScriptPath(home)); rerr != nil && !os.IsNotExist(rerr) {
			statusErr = rerr
		}
	}
	return res, errors.Join(err, statusErr)
}

func ensureGrok(home, command, todoReporter string) (InstallResult, error) {
	if !isDir(filepath.Join(home, ".grok")) {
		return InstallResult{}, nil
	}
	root := claudeOwnedDoc(command)
	upsertTodoHook(root, todoReporter, grokTodoMatcher)
	return writeJSONFile(filepath.Join(home, ".grok", "hooks", "aiman.json"), root)
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
