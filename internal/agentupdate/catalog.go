// Package agentupdate knows how each supported coding agent is installed,
// how to read its installed and latest versions without updating it, and how
// to apply an update safely on the host that runs aiman serve.
package agentupdate

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Method is how an agent binary was installed. It decides the update command
// and whether an update can swap the binary under a running session.
type Method string

// Install methods detected from the resolved binary path.
const (
	MethodNative     Method = "native"
	MethodStandalone Method = "standalone"
	MethodManaged    Method = "managed"
	MethodNPM        Method = "npm"
	MethodYarn       Method = "yarn"
	MethodPNPM       Method = "pnpm"
	MethodBun        Method = "bun"
	MethodCurl       Method = "curl"
	MethodUnknown    Method = "unknown"
)

// SwapSafe reports whether an update installs a new binary beside the old
// one, so running sessions keep working. Package-manager prefixes rewrite the
// package directory in place, and an unknown method is treated the same way.
func (m Method) SwapSafe() bool {
	switch m {
	case MethodNative, MethodStandalone, MethodManaged, MethodCurl:
		return true
	default:
		return false
	}
}

func (m Method) packageManager() bool {
	switch m {
	case MethodNPM, MethodYarn, MethodPNPM, MethodBun:
		return true
	default:
		return false
	}
}

// Command is one process to run: argv plus extra environment merged over the
// caller's environment.
type Command struct {
	Argv []string
	Env  map[string]string
}

// String is the argv joined by spaces, used in logs and "needs root" errors.
func (c Command) String() string { return strings.Join(c.Argv, " ") }

// LatestKind selects where the latest published version is read from.
type LatestKind int

// Latest version sources.
const (
	// LatestNone means there is no cheap way to learn the latest version.
	LatestNone LatestKind = iota
	// LatestHTTPText reads a plain-text version from URL.
	LatestHTTPText
	// LatestHTTPJSON reads Fields from a JSON object at URL.
	LatestHTTPJSON
	// LatestFileJSON reads Fields from the JSON file File under $HOME, a
	// cache the agent maintains itself.
	LatestFileJSON
	// LatestNPM runs `npm view <Package> version`.
	LatestNPM
)

// LatestSource describes one latest-version probe.
type LatestSource struct {
	Kind    LatestKind
	URL     string
	File    string
	Fields  []string
	Package string
}

type pathRule struct {
	contains string
	method   Method
}

// Entry is one supported agent.
type Entry struct {
	// Agent is the base command name.
	Agent string
	// Package is the npm package name, used when the agent sits in a
	// package-manager prefix.
	Package string
	// GuardEnv disables the agent's own self-updater. Every probe runs with it.
	GuardEnv map[string]string
	// VersionFile, when set, is read under $HOME instead of running
	// `<agent> --version`.
	VersionFile string
	Latest      LatestSource
	// Update is the agent's own update command.
	Update        Command
	rules         []pathRule
	defaultMethod Method
}

var versionRe = regexp.MustCompile(`v?(\d+(?:\.\d+)+(?:-[0-9A-Za-z][0-9A-Za-z.]*)?)`)

// ParseVersion extracts the first dotted version from agent output.
func (e Entry) ParseVersion(out string) (string, error) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("%s: no version in %q", e.Agent, firstLine(out))
	}
	return strings.TrimRight(m[1], "."), nil
}

// DetectMethod classifies the resolved binary path.
func (e Entry) DetectMethod(path string) Method {
	if strings.Contains(path, "/node_modules/") {
		switch {
		case strings.Contains(path, "/.bun/"):
			return MethodBun
		case strings.Contains(path, "/pnpm/"), strings.Contains(path, "/.pnpm/"):
			return MethodPNPM
		case strings.Contains(path, "/yarn/"), strings.Contains(path, "/.yarn/"):
			return MethodYarn
		default:
			return MethodNPM
		}
	}
	for _, r := range e.rules {
		if strings.Contains(path, r.contains) {
			return r.method
		}
	}
	if e.defaultMethod != "" {
		return e.defaultMethod
	}
	return MethodUnknown
}

// UpdateCommand is the command that updates this agent when it was installed
// by method. Package-manager installs use that manager; everything else uses
// the agent's own updater.
func (e Entry) UpdateCommand(method Method) Command {
	if method.packageManager() && e.Package != "" {
		spec := e.Package + "@latest"
		switch method {
		case MethodYarn:
			return Command{Argv: []string{"yarn", "global", "add", spec}}
		case MethodPNPM:
			return Command{Argv: []string{"pnpm", "add", "-g", spec}}
		case MethodBun:
			return Command{Argv: []string{"bun", "add", "-g", spec}}
		default:
			return Command{Argv: []string{"npm", "install", "-g", spec}}
		}
	}
	return Command{Argv: append([]string(nil), e.Update.Argv...), Env: copyEnv(e.Update.Env)}
}

func copyEnv(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cmd(argv ...string) Command { return Command{Argv: argv} }

// catalog holds one row per supported agent. Rows follow the research table
// in docs/superpowers/plans/2026-10-28-nl-14-agent-updates.md.
var catalog = []Entry{
	{
		Agent:    "claude",
		Package:  "@anthropic-ai/claude-code",
		GuardEnv: map[string]string{"DISABLE_AUTOUPDATER": "1"},
		Latest:   LatestSource{Kind: LatestHTTPText, URL: "https://downloads.claude.ai/claude-code-releases/stable"},
		Update:   cmd("claude", "update"),
		rules:    []pathRule{{"/.local/share/claude/versions/", MethodNative}, {"/.claude/local/", MethodNative}},
	},
	{
		Agent:         "agy",
		GuardEnv:      map[string]string{"AGY_CLI_DISABLE_AUTO_UPDATE": "1"},
		Update:        cmd("agy", "update"),
		defaultMethod: MethodStandalone,
	},
	{
		Agent:    "grok",
		GuardEnv: map[string]string{"GROK_DISABLE_AUTOUPDATER": "1"},
		// grok's own `update --check` is an update subcommand, so the probe
		// reads the cache it writes instead.
		Latest: LatestSource{Kind: LatestFileJSON, File: ".grok/version.json", Fields: []string{"latest_version", "latest"}},
		Update: cmd("grok", "update"),
		rules:  []pathRule{{"/.grok/downloads/", MethodManaged}},
	},
	{
		Agent:   "codex",
		Package: "@openai/codex",
		Latest:  LatestSource{Kind: LatestFileJSON, File: ".codex/version.json", Fields: []string{"latest_version"}},
		Update:  cmd("codex", "update"),
		rules:   []pathRule{{"/.codex/packages/standalone/", MethodStandalone}},
	},
	{
		Agent:    "kilo",
		Package:  "@kilocode/cli",
		GuardEnv: map[string]string{"KILO_DISABLE_AUTOUPDATE": "1"},
		Latest:   LatestSource{Kind: LatestNPM, Package: "@kilocode/cli"},
		Update:   cmd("kilo", "upgrade"),
		rules:    []pathRule{{"/.kilo/bin/", MethodCurl}},
	},
	{
		Agent:         "copilot",
		Package:       "@github/copilot",
		GuardEnv:      map[string]string{"COPILOT_AUTO_UPDATE": "false"},
		Latest:        LatestSource{Kind: LatestNPM, Package: "@github/copilot"},
		Update:        cmd("copilot", "update"),
		defaultMethod: MethodStandalone,
	},
	{
		Agent:  "cursor-agent",
		Update: cmd("cursor-agent", "update"),
		rules:  []pathRule{{"/cursor-agent/versions/", MethodManaged}},
	},
	{
		Agent:       "pi",
		GuardEnv:    map[string]string{"PI_SKIP_VERSION_CHECK": "1"},
		VersionFile: ".pi/agent/install/current-version",
		Latest:      LatestSource{Kind: LatestHTTPJSON, URL: "https://pi.dev/api/latest-version", Fields: []string{"version"}},
		Update:      cmd("pi", "update", "--self"),
		rules:       []pathRule{{"/.pi/agent/install/", MethodManaged}},
	},
	{
		// muse self-updates on every invocation, including --version, unless
		// MUSE_NO_AUTO_UPDATE is set.
		Agent:    "muse",
		GuardEnv: map[string]string{"MUSE_NO_AUTO_UPDATE": "1"},
		Latest:   LatestSource{Kind: LatestHTTPJSON, URL: "https://api.meta.ai/muse-code/channels/muse-stable", Fields: []string{"version"}},
		Update:   Command{Argv: []string{"muse"}, Env: map[string]string{"MUSE_LAUNCHER_INSTALL": "1"}},
		rules:    []pathRule{{"/muse-bin-", MethodManaged}},
	},
	{
		Agent:   "deepcode",
		Package: "@vegamo/deepcode-cli",
		Latest:  LatestSource{Kind: LatestNPM, Package: "@vegamo/deepcode-cli"},
		Update:  cmd("npm", "install", "-g", "@vegamo/deepcode-cli@latest"),
	},
	{
		// ageni ignores unknown flags and installs, so the only probe it ever
		// sees is --version.
		Agent:         "ageni",
		Update:        cmd("ageni", "update"),
		defaultMethod: MethodStandalone,
	},
}

// Catalog returns a copy of every supported agent.
func Catalog() []Entry { return append([]Entry(nil), catalog...) }

// Lookup returns the entry for an agent base command.
func Lookup(agent string) (Entry, bool) {
	for _, e := range catalog {
		if e.Agent == agent {
			return e, true
		}
	}
	return Entry{}, false
}

// CompareVersions orders two dotted versions numerically: -1 when a < b, 0
// when equal, 1 when a > b. Missing segments count as zero. A suffix after
// "-" breaks ties; a version without one sorts after a version with one.
func CompareVersions(a, b string) int {
	am, as, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	bm, bs, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	ap, bp := strings.Split(am, "."), strings.Split(bm, ".")
	for i := 0; i < len(ap) || i < len(bp); i++ {
		x, y := segment(ap, i), segment(bp, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case as == bs:
		return 0
	case as == "":
		return 1
	case bs == "":
		return -1
	default:
		return strings.Compare(as, bs)
	}
}

func segment(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0
	}
	return n
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

var errNoVersion = errors.New("no version")
