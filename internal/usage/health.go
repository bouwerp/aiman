package usage

import (
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Health is per-session tool-use health derived from the transcript.
// Known is false when the transcript holds no Claude or Codex tool records
// we can read; every count is then zero and callers must not show it.
type Health struct {
	Known            bool      `json:"known"`
	ToolCalls        int       `json:"tool_calls"`
	ReadCalls        int       `json:"read_calls"`
	EditCalls        int       `json:"edit_calls"`
	EditsWithoutRead int       `json:"edits_without_read"`
	ReadEditRatio    float64   `json:"read_edit_ratio"`
	FirstTestAt      time.Time `json:"first_test_at,omitempty"`
	Retries          int       `json:"retries"`
}

// DefaultTestCommands is the first-test match list when usage.test_commands
// is empty.
var DefaultTestCommands = []string{"go test", "pytest", "npm test", "make test", "just test"}

// healthAcc accumulates Health plus the set of paths already read, which
// EditsWithoutRead needs across incremental scans. seen dedupes repeated
// Claude assistant records that share a message id.
type healthAcc struct {
	h     Health
	reads map[string]bool
	seen  map[string]bool
	// fileTool is true when at least one Read/Edit/Write targeted a real
	// worktree path. Shell-only rollouts (exec_command, local_shell_call)
	// do not make health known.
	fileTool bool
}

func newHealthAcc() *healthAcc {
	return &healthAcc{reads: map[string]bool{}, seen: map[string]bool{}}
}

func (a *healthAcc) finish() Health {
	a.h.ReadEditRatio = 0
	if a.h.EditCalls > 0 {
		a.h.ReadEditRatio = float64(a.h.ReadCalls) / float64(a.h.EditCalls)
	}
	if !a.fileTool {
		a.h.Known = false
	}
	return a.h
}

// ClaudeHealth scans a Claude transcript for tool health.
// A malformed tool block is skipped, never fatal.
func ClaudeHealth(r io.Reader, worktree string, testCommands []string) (Health, error) {
	acc := newHealthAcc()
	_, err := readLines(r, func(line []byte) error {
		acc.claudeLine(line, worktree, testCommands)
		return nil
	})
	if err != nil {
		return Health{}, err
	}
	return acc.finish(), nil
}

// CodexHealth scans a Codex rollout for tool health.
func CodexHealth(r io.Reader, worktree string, testCommands []string) (Health, error) {
	acc := newHealthAcc()
	_, err := readLines(r, func(line []byte) error {
		acc.codexLine(line, worktree, testCommands)
		return nil
	})
	if err != nil {
		return Health{}, err
	}
	return acc.finish(), nil
}

type claudeBlock struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

type claudeHealthRow struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID      string        `json:"id"`
		Content []claudeBlock `json:"content"`
	} `json:"message"`
}

func (a *healthAcc) claudeLine(line []byte, worktree string, cmds []string) {
	var row claudeHealthRow
	if err := json.Unmarshal(line, &row); err != nil {
		return
	}
	if row.Type != "assistant" && row.Type != "user" {
		return
	}
	if row.Type == "assistant" && row.Message.ID != "" {
		if a.seen[row.Message.ID] {
			return
		}
		a.seen[row.Message.ID] = true
	}
	at := parseHealthTime(row.Timestamp)
	for _, block := range row.Message.Content {
		switch block.Type {
		case "tool_use":
			a.claudeTool(block, worktree, cmds, at)
		case "tool_result":
			if isRetryText(blockText(block.Content)) {
				a.h.Known = true
				a.h.Retries++
			}
		}
	}
}

func (a *healthAcc) claudeTool(block claudeBlock, worktree string, cmds []string, at time.Time) {
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
	}
	if len(block.Input) > 0 {
		if err := json.Unmarshal(block.Input, &input); err != nil {
			return
		}
	}
	path := input.FilePath
	if path == "" {
		path = input.Path
	}
	a.applyTool(block.Name, path, input.Command, worktree, cmds, at)
}

type codexHealthRow struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type      string          `json:"type"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Action    json.RawMessage `json:"action"`
		Command   string          `json:"command"`
	} `json:"payload"`
}

func (a *healthAcc) codexLine(line []byte, worktree string, cmds []string) {
	var row codexHealthRow
	if err := json.Unmarshal(line, &row); err != nil {
		return
	}
	switch row.Type {
	case "session_meta", "turn_context", "event_msg", "response_item", "token_usage_record":
	default:
		return
	}
	at := parseHealthTime(row.Timestamp)
	switch row.Payload.Type {
	case "function_call":
		path, command := codexArgs(row.Payload.Arguments)
		a.applyTool(row.Payload.Name, path, command, worktree, cmds, at)
	case "custom_tool_call":
		if row.Payload.Name == "exec" {
			path, command := codexArgs(row.Payload.Arguments)
			a.applyTool("Bash", path, command, worktree, cmds, at)
		}
	case "local_shell_call":
		a.applyTool("Bash", "", codexActionCommand(row.Payload.Action, row.Payload.Command), worktree, cmds, at)
	}
	if isRetryText(string(line)) && row.Payload.Type != "function_call" && row.Payload.Type != "local_shell_call" {
		// Retry markers live in tool outputs; count the line once.
		if strings.Contains(string(line), "tool") || strings.Contains(string(line), "output") {
			a.h.Known = true
			a.h.Retries++
		}
	}
}

// applyTool records one tool call. A path outside worktree is unknown:
// it still counts as a tool call but never as an edit.
func (a *healthAcc) applyTool(name, path, command, worktree string, cmds []string, at time.Time) {
	a.h.ToolCalls++
	a.h.Known = true
	switch name {
	case "Read":
		a.h.ReadCalls++
		if key, ok := healthPathKey(worktree, path); ok && path != "" {
			a.reads[key] = true
			a.fileTool = true
		}
	case "Edit", "Write":
		key, ok := healthPathKey(worktree, path)
		if path != "" && !ok {
			return
		}
		if path != "" && ok {
			a.fileTool = true
		}
		a.h.EditCalls++
		if key != "" && !a.reads[key] {
			a.h.EditsWithoutRead++
		}
	}
	if command != "" && a.h.FirstTestAt.IsZero() && isTestCommand(command, cmds) {
		a.h.FirstTestAt = at
	}
}

// healthPathKey normalises path against worktree. ok is false when path is
// set but outside worktree. An empty path is ok with an empty key.
func healthPathKey(worktree, path string) (string, bool) {
	if path == "" {
		return "", true
	}
	clean := path
	if !filepath.IsAbs(clean) && worktree != "" {
		clean = filepath.Join(worktree, clean)
	}
	clean = filepath.Clean(clean)
	if worktree == "" {
		return clean, true
	}
	rel, err := filepath.Rel(filepath.Clean(worktree), clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
}

func isTestCommand(command string, cmds []string) bool {
	if len(cmds) == 0 {
		cmds = DefaultTestCommands
	}
	cmd := strings.TrimSpace(command)
	for _, want := range cmds {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if cmd == want || strings.HasPrefix(cmd, want+" ") || strings.HasPrefix(cmd, want+"\t") {
			return true
		}
	}
	return false
}

func isRetryText(text string) bool {
	return strings.Contains(strings.ToLower(text), "retry")
}

func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

func codexArgs(raw json.RawMessage) (path, command string) {
	if len(raw) == 0 {
		return "", ""
	}
	var text string
	body := raw
	if err := json.Unmarshal(raw, &text); err == nil {
		body = []byte(text)
	}
	var args struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Command  string `json:"command"`
		Cmd      string `json:"cmd"`
		Workdir  string `json:"workdir"`
	}
	if err := json.Unmarshal(body, &args); err != nil {
		return "", ""
	}
	path = args.FilePath
	if path == "" {
		path = args.Path
	}
	if path == "" {
		path = args.Workdir
	}
	command = args.Command
	if command == "" {
		command = args.Cmd
	}
	return path, command
}

func codexActionCommand(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		return text
	}
	var action struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &action); err == nil && action.Command != "" {
		return action.Command
	}
	return fallback
}

// accFromStored rebuilds an accumulator from stored counts and read paths.
// A stored known health with file-tool counts keeps shell-only appends known.
func accFromStored(h Health, readsJSON string) *healthAcc {
	acc := newHealthAcc()
	acc.h = h
	acc.fileTool = h.ReadCalls > 0 || h.EditCalls > 0
	if readsJSON != "" {
		var paths []string
		if err := json.Unmarshal([]byte(readsJSON), &paths); err == nil {
			for _, p := range paths {
				acc.reads[p] = true
			}
		}
	}
	return acc
}

func (a *healthAcc) readsJSON() string {
	if len(a.reads) == 0 {
		return ""
	}
	paths := make([]string, 0, len(a.reads))
	for p := range a.reads {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	raw, err := json.Marshal(paths)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseHealthTime(value string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at
}
