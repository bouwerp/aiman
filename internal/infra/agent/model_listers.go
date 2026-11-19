package agent

import (
	"encoding/json"
	"strings"
)

// modelLister is how one agent CLI lists its models on the host.
type modelLister struct {
	// bin is the binary to run; empty is the agent's own.
	bin string
	// args is the listing command after the binary name.
	args  string
	parse func(out string) []ModelOption
}

// modelListers are the agents whose models a host can list. Claude has no
// listing command, so serve answers for it (`aiman agents models claude`:
// its aliases plus the versioned ids its transcripts on the host have used).
// Copilot, Muse and Deep Code have none, and Cursor's needs a login the host
// may not have, so their pickers read LaunchCatalog.
var modelListers = map[string]modelLister{
	"pi":     {args: "--list-models", parse: ParsePiModels},
	"kilo":   {args: "models", parse: ParseProviderModels},
	"codex":  {args: "debug models", parse: ParseCodexModels},
	"grok":   {args: "models", parse: ParseGrokModels},
	"agy":    {args: "models", parse: ParseAgyModels},
	"claude": {bin: "aiman", args: "agents models claude", parse: ParseModelLines},
}

// AgentLister reports whether the agent's own CLI lists its models on the
// host, as opposed to serve answering for it.
func AgentLister(command string) bool {
	l, ok := modelListers[CommandBase(command)]
	return ok && l.bin == ""
}

// ParseModelLines reads one model id per line, with an optional tab and
// label (`aiman agents models`).
func ParseModelLines(out string) []ModelOption {
	var opts []ModelOption
	for _, line := range strings.Split(out, "\n") {
		id, label, _ := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if id == "" || strings.ContainsAny(id, " ") {
			continue
		}
		label = strings.TrimSpace(label)
		if label == "" {
			label = id
		} else {
			label = id + "  " + label
		}
		opts = append(opts, ModelOption{ID: id, Label: label})
	}
	return opts
}

// listModelsCmd runs an agent's listing command from $HOME with the usual
// user bin directories on PATH, so a non-login SSH shell still finds the
// binary, under a 30s limit. stderr is dropped so warnings do not reach the
// parser.
func listModelsCmd(base, args string) string {
	return `bash -lc 'export PATH="$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.kilo/bin:/usr/local/bin:/opt/homebrew/bin"; cd "$HOME" && timeout 30 ` +
		base + " " + args + ` 2>/dev/null'`
}

// ParseProviderModels reads one "<provider>/<model>" id per line (`kilo
// models`). The provider is the first path segment.
func ParseProviderModels(out string) []ModelOption {
	var opts []ModelOption
	for _, line := range strings.Split(out, "\n") {
		id := strings.TrimSpace(line)
		provider, _, ok := strings.Cut(id, "/")
		if !ok || provider == "" || strings.ContainsAny(id, " \t") {
			continue
		}
		opts = append(opts, ModelOption{Provider: provider, ID: id, Label: id})
	}
	return opts
}

// ParseCodexModels reads `codex debug models` JSON and keeps the models
// Codex lists in its own picker (visibility "list").
func ParseCodexModels(out string) []ModelOption {
	var doc struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		return nil
	}
	var opts []ModelOption
	for _, m := range doc.Models {
		if m.Slug == "" || (m.Visibility != "" && m.Visibility != "list") {
			continue
		}
		label := m.Slug
		if m.DisplayName != "" && m.DisplayName != m.Slug {
			label += "  " + m.DisplayName
		}
		opts = append(opts, ModelOption{ID: m.Slug, Label: label})
	}
	return opts
}

// ParseGrokModels reads the "Available models:" list of `grok models`:
// "  - <id>" rows, and "  * <id> (default)" for the default.
func ParseGrokModels(out string) []ModelOption {
	var opts []ModelOption
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		marker, rest, ok := strings.Cut(t, " ")
		if !ok || (marker != "-" && marker != "*") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		label := f[0]
		if marker == "*" {
			label += "  default"
		}
		opts = append(opts, ModelOption{ID: f[0], Label: label})
	}
	return opts
}

// ParseAgyModels reads `agy models`: "<id>\t<display name>" rows after a
// "Fetching…" line.
func ParseAgyModels(out string) []ModelOption {
	var opts []ModelOption
	for _, line := range strings.Split(out, "\n") {
		id, name, ok := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		if !ok || id == "" || strings.ContainsAny(id, " ") {
			continue
		}
		label := id
		if name = strings.TrimSpace(name); name != "" {
			label += "  " + name
		}
		opts = append(opts, ModelOption{ID: id, Label: label})
	}
	return opts
}
