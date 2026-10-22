package agenthook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// ExtractTodoSnapshot returns the agent's whole task list from a hook payload.
// ok is false unless the payload is a full list: TodoWrite's todos array, or
// a Claude TaskCreate/TaskUpdate whose list readTasks can read. A Tasks
// payload without that list is not ok, so a mirror never replaces the list
// with the single task the hook named. update_plan is not a full list.
// An empty TodoWrite array is ok: the agent cleared its list.
//
// readTasks is given the Claude session id. The files live in
// ~/.claude/tasks/<session_id>/.
func ExtractTodoSnapshot(raw []byte, readTasks func(listRef string) ([]domain.TodoSnapshotItem, error)) ([]domain.TodoSnapshotItem, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var payload struct {
		ToolName  string          `json:"tool_name"`
		SessionID string          `json:"session_id"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	switch payload.ToolName {
	case "TodoWrite":
		return todoWriteSnapshot(payload.ToolInput)
	case "TaskCreate", "TaskUpdate":
		return claudeTasksSnapshot(payload.SessionID, readTasks)
	default:
		return nil, false
	}
}

func todoWriteSnapshot(toolInput json.RawMessage) ([]domain.TodoSnapshotItem, bool) {
	if len(bytes.TrimSpace(toolInput)) == 0 {
		return nil, false
	}
	var input struct {
		Todos json.RawMessage `json:"todos"`
	}
	if err := json.Unmarshal(toolInput, &input); err != nil {
		return nil, false
	}
	todos := bytes.TrimSpace(input.Todos)
	if len(todos) == 0 || todos[0] != '[' {
		return nil, false
	}
	var rows []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(todos, &rows); err != nil {
		return nil, false
	}
	items := make([]domain.TodoSnapshotItem, len(rows))
	for i, row := range rows {
		items[i] = domain.TodoSnapshotItem{
			Text:   row.Content,
			Status: domain.NormalizeVendorTodoStatus(row.Status),
		}
	}
	return items, true
}

func claudeTasksSnapshot(sessionID string, readTasks func(string) ([]domain.TodoSnapshotItem, error)) ([]domain.TodoSnapshotItem, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if readTasks == nil || sessionID == "" {
		return nil, false
	}
	items, err := readTasks(sessionID)
	if err != nil {
		return nil, false
	}
	if items == nil {
		return []domain.TodoSnapshotItem{}, true
	}
	return items, true
}

// LoadClaudeTasks reads one Claude task JSON file per item from dir, in
// numeric id order. Status deleted is omitted. A missing directory or an
// unreadable file is an error: the caller must not mirror a partial list.
// Non-JSON entries such as .lock are ignored.
func LoadClaudeTasks(dir string) ([]domain.TodoSnapshotItem, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read claude tasks: %w", err)
	}
	names := jsonTaskFiles(entries)
	sort.Slice(names, func(i, j int) bool { return taskFileLess(names[i], names[j]) })
	root := os.DirFS(dir)
	var out []domain.TodoSnapshotItem
	for _, name := range names {
		item, include, err := readClaudeTask(root, name)
		if err != nil {
			return nil, err
		}
		if include {
			out = append(out, item)
		}
	}
	return out, nil
}

func jsonTaskFiles(entries []os.DirEntry) []string {
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func taskFileLess(a, b string) bool {
	ai, aok := taskFileIndex(a)
	bi, bok := taskFileIndex(b)
	if aok && bok && ai != bi {
		return ai < bi
	}
	if aok != bok {
		return aok
	}
	return a < b
}

func taskFileIndex(name string) (int, bool) {
	stem := strings.TrimSuffix(name, ".json")
	if stem == "" {
		return 0, false
	}
	n := 0
	for _, c := range stem {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func readClaudeTask(root fs.FS, name string) (domain.TodoSnapshotItem, bool, error) {
	b, err := fs.ReadFile(root, name)
	if err != nil {
		return domain.TodoSnapshotItem{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	var rec struct {
		Subject string `json:"subject"`
		Status  string `json:"status"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return domain.TodoSnapshotItem{}, false, fmt.Errorf("parse %s: %w", name, err)
	}
	if rec.Deleted || deletedTaskStatus(rec.Status) {
		return domain.TodoSnapshotItem{}, false, nil
	}
	return domain.TodoSnapshotItem{
		Text:   rec.Subject,
		Status: domain.NormalizeVendorTodoStatus(rec.Status),
	}, true, nil
}

func deletedTaskStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "deleted")
}
