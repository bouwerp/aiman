package agent

import (
	"path"
	"regexp"
	"strings"
)

// SelfUpdateGuard is what keeps one agent CLI from updating itself inside an
// aiman session. Env goes into the session environment and Flags onto the
// launch command. aiman serve owns updates instead, so a session never
// restarts or blocks on an updater mid-task.
type SelfUpdateGuard struct {
	Env   map[string]string
	Flags []string
}

// Empty reports whether the guard sets nothing.
func (g SelfUpdateGuard) Empty() bool {
	return len(g.Env) == 0 && len(g.Flags) == 0
}

// selfUpdateGuards is keyed by canonical base. Deep Code has no switch; its
// launcher clears the pending update instead (agenthook.DeepcodeLaunchScript).
var selfUpdateGuards = map[string]SelfUpdateGuard{
	// DISABLE_UPDATES would also block a manual `claude update`.
	"claude": {Env: map[string]string{"DISABLE_AUTOUPDATER": "1"}},
	"agy":    {Env: map[string]string{"AGY_CLI_DISABLE_AUTO_UPDATE": "1"}},
	"grok": {
		Env:   map[string]string{"GROK_DISABLE_AUTOUPDATER": "1"},
		Flags: []string{"--no-auto-update"},
	},
	"codex": {Flags: []string{"-c check_for_update_on_startup=false", "--disable in_app_updates"}},
	"kilo":  {Env: map[string]string{"KILO_DISABLE_AUTOUPDATE": "1"}},
	"copilot": {
		Env:   map[string]string{"COPILOT_AUTO_UPDATE": "false"},
		Flags: []string{"--no-auto-update"},
	},
	"cursor-agent": {Flags: []string{"--disable-auto-update"}},
	// PI_OFFLINE would also cut the model provider off.
	"pi":       {Env: map[string]string{"PI_SKIP_VERSION_CHECK": "1"}},
	"muse":     {Env: map[string]string{"MUSE_NO_AUTO_UPDATE": "1"}},
	"deepcode": {},
}

// canonicalBase folds a command base onto the key selfUpdateGuards uses.
func canonicalBase(base string) string {
	base = strings.ToLower(strings.TrimSpace(base))
	switch {
	case base == "claude-code":
		return "claude"
	case base == "grok-build":
		return "grok"
	case base == "kilocode":
		return "kilo"
	case strings.Contains(base, "cursor"):
		return "cursor-agent"
	case base == "deepcode-launch.sh":
		return "deepcode"
	}
	return base
}

// GuardFor returns the self-update guard for a binary base (see BaseOf).
// Unknown agents get an empty guard. The returned value is a copy.
func GuardFor(base string) SelfUpdateGuard {
	g, ok := selfUpdateGuards[canonicalBase(base)]
	if !ok {
		return SelfUpdateGuard{}
	}
	out := SelfUpdateGuard{}
	if len(g.Env) > 0 {
		out.Env = make(map[string]string, len(g.Env))
		for k, v := range g.Env {
			out.Env[k] = v
		}
	}
	out.Flags = append([]string(nil), g.Flags...)
	return out
}

// GuardedAgents lists the canonical bases that have a guard entry.
func GuardedAgents() []string {
	out := make([]string, 0, len(selfUpdateGuards))
	for k := range selfUpdateGuards {
		out = append(out, k)
	}
	return out
}

// UpdateDialog is a blocking update prompt an agent can draw at startup. Re
// is an extended regular expression that is valid for both Go regexp and
// grep -E, since the boot wait runs it in a remote shell. Key is the key that
// dismisses the prompt without installing anything.
type UpdateDialog struct {
	Agent string
	Re    string
	Key   string
}

// UpdateDialogs lists the startup update prompts the boot wait and the steer
// gate recognise. Enter or a digit would pick "Update now" or "Install" in
// these menus, so every Key skips instead.
var UpdateDialogs = []UpdateDialog{
	// The modal reads "Update available · A → B" over "› 1. Update now",
	// "2. Skip", "3. Skip until next version". Codex also prints a
	// non-blocking "✨ Update available! A -> B" banner above a ready
	// composer, so only the menu row identifies the modal.
	{Agent: "codex", Re: `Skip until next version`, Key: "esc"},
	// "Deep Code latest version has been released: A -> B" over a numbered
	// menu and "Esc to ignore once".
	{Agent: "deepcode", Re: `latest version has been released`, Key: "esc"},
}

// UpdateDialogTailLines is how many rows at the bottom of a rendered screen
// are searched for an update dialog. A live dialog is the last thing drawn.
// Deep Code leaves a dismissed prompt in the primary screen and scrollback,
// so matching the whole screen would see it long after it was answered.
const UpdateDialogTailLines = 12

// MatchUpdateDialog reports the update dialog drawn at the bottom of screen.
func MatchUpdateDialog(screen string) (UpdateDialog, bool) {
	tail := screenTail(screen, UpdateDialogTailLines)
	for _, d := range UpdateDialogs {
		if updateDialogRes[d.Agent].MatchString(tail) {
			return d, true
		}
	}
	return UpdateDialog{}, false
}

var updateDialogRes = func() map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp, len(UpdateDialogs))
	for _, d := range UpdateDialogs {
		out[d.Agent] = regexp.MustCompile(d.Re)
	}
	return out
}()

// screenTail is the last n rows of screen once trailing blank rows are
// dropped, the same rows `aiman pty capture --lines n` returns.
func screenTail(screen string, n int) string {
	rows := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

// BaseOf returns the canonical agent binary a launch command runs. Leading
// KEY=value assignments are skipped, a path is reduced to its last element,
// and the Deep Code launcher script maps to deepcode.
func BaseOf(cmd string) string {
	for _, f := range strings.Fields(strings.TrimSpace(cmd)) {
		if isEnvAssign(f) {
			continue
		}
		return canonicalBase(path.Base(strings.Trim(f, `"'`)))
	}
	return ""
}

// isEnvAssign reports whether a command token is a KEY=value assignment.
func isEnvAssign(tok string) bool {
	key, _, ok := strings.Cut(tok, "=")
	if !ok || key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
