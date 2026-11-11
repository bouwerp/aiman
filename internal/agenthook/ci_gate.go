package agenthook

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"
)

// CIGateMarker identifies the generated local CI gate script.
const CIGateMarker = "ci-gate.sh"

func ciGatePath(home string) string {
	return filepath.Join(home, ".aiman", "hooks", CIGateMarker)
}

// ciGateScript hands a Bash call that may push, open a PR or mark one ready to
// aiman ci gate --hook, which asks serve. The case is a cheap prefilter; the
// Go side parses the command. Anything but a clean allow blocks (exit 2). It
// never runs gh: PR state comes from serve.
const ciGateScript = `#!/bin/sh
[ "${AIMAN_ENV:-}" = 1 ] || exit 0
[ -n "${AIMAN_ID:-}" ] || exit 0
input=$(cat)
case "$input" in
  *push*|*"pr create"*|*"pr new"*|*"pr ready"*) ;;
  *) exit 0 ;;
esac
bin="${AIMAN_BIN_PATH:-}"
if [ -z "$bin" ] || [ ! -x "$bin" ]; then
  if command -v aiman >/dev/null 2>&1; then
    bin=aiman
  elif [ -x "$HOME/.local/bin/aiman" ]; then
    bin="$HOME/.local/bin/aiman"
  else
    echo "aiman ci gate: aiman binary not found" >&2
    exit 2
  fi
fi
if command -v timeout >/dev/null 2>&1; then
  printf '%s' "$input" | timeout 15 "$bin" ci gate --hook
else
  printf '%s' "$input" | "$bin" ci gate --hook
fi
[ $? -eq 0 ] || exit 2
`

// Gate kinds GateQuestionFor reports; they match ci.gate's kind parameter.
const (
	GateKindPush   = "push"
	GateKindCreate = "create"
	GateKindReady  = "ready"
)

// GateQuestion is what a Bash call asks the local CI gate. Command is the
// whole command verbatim, so serve judges compound pushes itself.
type GateQuestion struct {
	Kind    string
	Command string
	Draft   bool
}

// GateQuestionFor reads tool_input.command from a Claude PreToolUse payload
// and reports whether it pushes, opens a PR or marks one ready. A push wins
// over the others: serve refuses a push that is not alone in its command.
// Any other field, and a payload that is not JSON, is never gated.
func GateQuestionFor(payload []byte) (GateQuestion, bool) {
	var p struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || strings.TrimSpace(p.ToolInput.Command) == "" {
		return GateQuestion{}, false
	}
	command := p.ToolInput.Command
	var found GateQuestion
	ok := false
	for _, words := range simpleCommands(command) {
		kind, draft := classifyCommand(words)
		if kind == "" {
			continue
		}
		if kind == GateKindPush {
			return GateQuestion{Kind: kind, Command: command}, true
		}
		if !ok {
			found, ok = GateQuestion{Kind: kind, Command: command, Draft: draft}, true
		}
	}
	return found, ok
}

// classifyCommand names the gate kind of one simple command.
func classifyCommand(words []string) (kind string, draft bool) {
	words = dropAssignments(words)
	if len(words) == 0 {
		return "", false
	}
	switch path.Base(words[0]) {
	case "git":
		if sub := afterGlobalFlags(words[1:], gitValueFlags); len(sub) > 0 && sub[0] == "push" {
			return GateKindPush, false
		}
	case "gh":
		return classifyGH(afterGlobalFlags(words[1:], ghValueFlags))
	}
	return "", false
}

func classifyGH(sub []string) (kind string, draft bool) {
	if len(sub) < 2 || sub[0] != "pr" {
		return "", false
	}
	rest := sub[2:]
	switch sub[1] {
	case "create", "new":
		return GateKindCreate, hasWord(rest, "--draft", "-d")
	case "ready":
		if hasWord(rest, "--undo") {
			return "", false
		}
		return GateKindReady, false
	}
	return "", false
}

// gitValueFlags and ghValueFlags are the global options that take the next
// word as their value.
var (
	gitValueFlags = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true}
	ghValueFlags  = map[string]bool{"-R": true, "--repo": true}
)

// afterGlobalFlags skips leading options (and the values of those in
// valueFlags) and returns the subcommand onwards.
func afterGlobalFlags(words []string, valueFlags map[string]bool) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		if valueFlags[words[0]] && len(words) > 1 {
			words = words[1:]
		}
		words = words[1:]
	}
	return words
}

// dropAssignments skips leading NAME=value words.
func dropAssignments(words []string) []string {
	for len(words) > 0 {
		name, _, ok := strings.Cut(words[0], "=")
		if !ok || name == "" || strings.ContainsAny(name, "-/.$'\"") {
			break
		}
		words = words[1:]
	}
	return words
}

func hasWord(words []string, want ...string) bool {
	for _, w := range words {
		for _, x := range want {
			if w == x {
				return true
			}
		}
	}
	return false
}

// simpleCommands splits a shell command into the words of each simple
// command. Quotes and backslashes group words; ;, &, | and newlines separate
// commands. It reads well-formed commands only well enough to classify them.
func simpleCommands(command string) [][]string {
	var (
		out   [][]string
		words []string
		word  strings.Builder
		in    bool // a word has started, even an empty quoted one
	)
	endWord := func() {
		if in {
			words = append(words, word.String())
			word.Reset()
			in = false
		}
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			out = append(out, words)
			words = nil
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == '\'':
			in = true
			j := strings.IndexByte(command[i+1:], '\'')
			if j < 0 {
				j = len(command) - i - 1
			}
			word.WriteString(command[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			in = true
			i = readDoubleQuoted(command, i+1, &word)
		case c == '\\' && i+1 < len(command):
			in = true
			i++
			if command[i] != '\n' {
				word.WriteByte(command[i])
			}
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')':
			endCommand()
		case c == ' ' || c == '\t':
			endWord()
		default:
			in = true
			word.WriteByte(c)
		}
	}
	endCommand()
	return out
}

// readDoubleQuoted writes the body of a double-quoted string starting at i
// and returns the index of its closing quote.
func readDoubleQuoted(s string, i int, word *strings.Builder) int {
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			return i
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0:
			i++
			if s[i] != '\n' {
				word.WriteByte(s[i])
			}
		default:
			word.WriteByte(c)
		}
	}
	return i
}
