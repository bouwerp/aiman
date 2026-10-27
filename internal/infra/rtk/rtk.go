// Package rtk builds the remote shell snippets that install RTK
// (github.com/rtk-ai/rtk), register its agent hooks, and read its token
// savings with `rtk gain`.
package rtk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// InstallURL is RTK's install script. It installs into $HOME/.local/bin
// without sudo.
const InstallURL = "https://raw.githubusercontent.com/rtk-ai/rtk/master/install.sh"

// HookTarget is an agent binary RTK can hook user-globally, and the
// `rtk init` arguments that do it.
type HookTarget struct {
	Binary string
	Args   []string
}

// HookTargets lists the agents with user-global RTK hooks. Antigravity and
// Kilo Code are project-scoped in RTK, and the other aiman agents have no RTK
// support, so they are absent.
var HookTargets = []HookTarget{
	{"claude", []string{"--global"}},
	{"codex", []string{"--global", "--codex"}},
	{"cursor-agent", []string{"--global", "--agent", "cursor"}},
	{"gemini", []string{"--global", "--gemini"}},
	{"copilot", []string{"--global", "--copilot"}},
	{"pi", []string{"--global", "--agent", "pi"}},
}

// gainCmd is the only form of `rtk gain` aiman runs: plain `rtk gain` can
// open a live dashboard when the RTK proxy is running.
const gainCmd = "rtk gain --all --format json </dev/null"

// InstallScript installs RTK when it is missing, checks that the `rtk` on
// PATH is RTK and not another tool of the same name, and registers hooks for
// every HookTargets agent installed on the host. Output lines are read by
// ParseInstall; the script exits 3 on a wrong binary.
func InstallScript() string {
	var b strings.Builder
	b.WriteString(`set -e
set -o pipefail
export PATH="$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.cargo/bin:/usr/local/bin:$PATH"
export RTK_TELEMETRY_DISABLED=1
export RTK_EDITION=oss
if ! command -v rtk >/dev/null 2>&1; then
  curl -fsSL ` + InstallURL + ` | sh
fi
if ! command -v rtk >/dev/null 2>&1; then
  echo "rtk is not on PATH after install" >&2
  exit 4
fi
# "cargo install rtk" can install Rust Type Kit, which has no gain command.
if ! ` + gainCmd + ` >/dev/null 2>&1; then
  echo RTK_WRONG_BINARY=1
  exit 3
fi
echo "RTK_VERSION=$(rtk --version 2>/dev/null | head -1)"
`)
	for _, h := range HookTargets {
		fmt.Fprintf(&b, `if command -v %[1]s >/dev/null 2>&1; then
  if rtk init %[2]s --auto-patch </dev/null >/dev/null 2>&1; then echo "RTK_HOOK=%[1]s ok"; else echo "RTK_HOOK=%[1]s failed"; fi
fi
`, h.Binary, strings.Join(h.Args, " "))
	}
	return b.String()
}

// InstallReport is what InstallScript printed. Hooks holds one entry per
// installed agent, true when its hook registered.
type InstallReport struct {
	Version     string
	Hooks       map[string]bool
	WrongBinary bool
}

// ParseInstall reads InstallScript output and ignores lines it does not know.
func ParseInstall(out string) InstallReport {
	r := InstallReport{Hooks: map[string]bool{}}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "RTK_VERSION":
			r.Version = val
		case "RTK_WRONG_BINARY":
			r.WrongBinary = val == "1"
		case "RTK_HOOK":
			if name, status, ok := strings.Cut(val, " "); ok {
				r.Hooks[name] = status == "ok"
			}
		}
	}
	return r
}

// Gain is the all-time summary of `rtk gain --all --format json`.
type Gain struct {
	Commands, InputTokens, OutputTokens, SavedTokens int64
	SavingsPct                                       float64
}

// ParseGain reads the summary object of `rtk gain --all --format json`.
func ParseGain(raw []byte) (Gain, error) {
	var doc struct {
		Summary *struct {
			TotalCommands int64   `json:"total_commands"`
			TotalInput    int64   `json:"total_input"`
			TotalOutput   int64   `json:"total_output"`
			TotalSaved    int64   `json:"total_saved"`
			AvgSavingsPct float64 `json:"avg_savings_pct"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Gain{}, fmt.Errorf("parse rtk gain: %w", err)
	}
	if doc.Summary == nil {
		return Gain{}, errors.New("parse rtk gain: no summary")
	}
	s := doc.Summary
	return Gain{
		Commands:     s.TotalCommands,
		InputTokens:  s.TotalInput,
		OutputTokens: s.TotalOutput,
		SavedTokens:  s.TotalSaved,
		SavingsPct:   s.AvgSavingsPct,
	}, nil
}

// Executor runs a shell command on a host. *ssh.Manager satisfies it.
type Executor interface {
	Execute(ctx context.Context, cmd string) (string, error)
}

const missingMarker = "RTK_MISSING"

// FetchGain reads RTK's savings on the executor's host. A host without rtk
// returns installed=false and no error.
func FetchGain(ctx context.Context, ex Executor) (g Gain, installed bool, err error) {
	cmd := `export PATH="$HOME/.local/bin:$PATH"; command -v rtk >/dev/null 2>&1 || { echo ` + missingMarker + `; exit 0; }; ` + gainCmd
	out, err := ex.Execute(ctx, cmd)
	if err != nil {
		return Gain{}, false, fmt.Errorf("rtk gain: %w", err)
	}
	if hasLine(out, missingMarker) {
		return Gain{}, false, nil
	}
	// SSH output is combined, so stderr noise can surround the JSON object.
	start, end := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return Gain{}, true, fmt.Errorf("rtk gain: no JSON in output %q", strings.TrimSpace(out))
	}
	g, err = ParseGain([]byte(out[start : end+1]))
	return g, true, err
}

// hasLine reports whether out has a line equal to want. A login shell can
// print profile noise around the line the command itself writes.
func hasLine(out, want string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
