package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/todo"
)

func runTodo(args []string, stdin io.Reader, getenv func(string) string) error {
	if len(args) == 0 {
		return todoUsage()
	}
	if args[0] == "report" {
		return reportTodos(args[1:], stdin, getenv)
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	flags, rest := takeFlags(args[1:])
	sessionID := strings.TrimSpace(flags["session"])
	if sessionID == "" {
		sessionID = strings.TrimSpace(getenv("AIMAN_ID"))
	}
	if sessionID == "" {
		return todoUsage()
	}
	method, params, err := todoParams(args[0], rest, flags)
	if err != nil {
		if errors.Is(err, errUsage) {
			return todoUsage()
		}
		return err
	}
	params["session"] = sessionID
	if as := flags["as"]; as != "" {
		params["as"] = as
	}
	return callTodo(method, params, flags["json"] == "1")
}

func todoUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman todo <list|add|start|done|drop|edit|move|rm|report> [--session NAME] [--as USER] [--json]\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; one of them is required.\n")
	return errUsage
}

// todoParams maps a todo subcommand onto an agent API method. It does not
// resolve the session or authorship: the caller adds session, and as only
// when the user passed --as. Whitespace in text is left for the server.
func todoParams(cmd string, rest []string, flags map[string]string) (string, map[string]any, error) {
	switch cmd {
	case "list":
		if len(rest) != 0 {
			return "", nil, errUsage
		}
		return "todo.get", map[string]any{}, nil
	case "add":
		return todoAddParams(rest, flags)
	case "start", "done", "drop":
		return todoStateParams(cmd, rest)
	case "edit":
		return todoEditParams(rest)
	case "move":
		return todoMoveParams(rest)
	case "rm":
		if len(rest) != 1 || rest[0] == "" {
			return "", nil, errUsage
		}
		return "todo.remove", map[string]any{"id": rest[0]}, nil
	default:
		return "", nil, errUsage
	}
}

func todoAddParams(rest []string, flags map[string]string) (string, map[string]any, error) {
	text := strings.Join(rest, " ")
	if strings.TrimSpace(text) == "" {
		return "", nil, errUsage
	}
	params := map[string]any{"text": text}
	if flags != nil && flags["after"] != "" {
		params["after"] = flags["after"]
	}
	return "todo.add", params, nil
}

func todoStateParams(cmd string, rest []string) (string, map[string]any, error) {
	if len(rest) != 1 || rest[0] == "" {
		return "", nil, errUsage
	}
	state := map[string]string{
		"start": string(domain.TodoActive),
		"done":  string(domain.TodoDone),
		"drop":  string(domain.TodoDropped),
	}[cmd]
	return "todo.update", map[string]any{"id": rest[0], "state": state}, nil
}

func todoEditParams(rest []string) (string, map[string]any, error) {
	if len(rest) < 2 || rest[0] == "" {
		return "", nil, errUsage
	}
	return "todo.update", map[string]any{"id": rest[0], "text": strings.Join(rest[1:], " ")}, nil
}

// todoMoveParams takes a 1-based position. The API index is POS-1.
func todoMoveParams(rest []string) (string, map[string]any, error) {
	if len(rest) != 2 || rest[0] == "" {
		return "", nil, errUsage
	}
	pos, ok := parseOneBased(rest[1])
	if !ok {
		return "", nil, errUsage
	}
	return "todo.move", map[string]any{"id": rest[0], "to_index": pos - 1}, nil
}

func parseOneBased(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n >= 1
}

func callTodo(method string, params map[string]any, asJSON bool) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.CallRaw(sock, method, raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	return printTodoResult(resp.Result, asJSON)
}

func printTodoResult(result any, asJSON bool) error {
	if asJSON {
		return writeJSON(result)
	}
	list, err := todoListFromResult(result)
	if err != nil {
		return err
	}
	fmt.Println(formatTodoLines(list))
	return nil
}

func todoListFromResult(result any) (domain.TodoList, error) {
	m, ok := result.(map[string]any)
	if !ok {
		return domain.TodoList{}, errors.New("todo result is not an object")
	}
	raw, err := json.Marshal(m["list"])
	if err != nil {
		return domain.TodoList{}, err
	}
	var list domain.TodoList
	if err := json.Unmarshal(raw, &list); err != nil {
		return domain.TodoList{}, fmt.Errorf("todo list: %w", err)
	}
	return list, nil
}

// formatTodoLines is one todo.FormatLine per item, including dropped items.
func formatTodoLines(list domain.TodoList) string {
	lines := make([]string, len(list.Items))
	for i, it := range list.Items {
		lines[i] = todo.FormatLine(it)
	}
	return strings.Join(lines, "\n")
}

// reportTodos is the hook entry point. It always returns nil and prints
// nothing. todo.mirror runs only when the payload is a full snapshot and
// AIMAN_ID names the session.
func reportTodos(_ []string, stdin io.Reader, getenv func(string) string) error {
	if getenv == nil || stdin == nil {
		return nil
	}
	sessionID := strings.TrimSpace(getenv("AIMAN_ID"))
	if sessionID == "" {
		return nil
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil
	}
	snap, ok := agenthook.ExtractTodoSnapshot(raw, claudeTaskList)
	if !ok {
		return nil
	}
	mirrorTodos(sessionID, snap)
	return nil
}

func claudeTaskList(listRef string) ([]domain.TodoSnapshotItem, error) {
	if !safeTaskRef(listRef) {
		return nil, errors.New("claude task list ref is not a session id")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home directory: %w", err)
	}
	return agenthook.LoadClaudeTasks(filepath.Join(home, ".claude", "tasks", listRef))
}

func safeTaskRef(ref string) bool {
	if ref == "" || ref == "." || ref == ".." {
		return false
	}
	return !strings.ContainsAny(ref, `/\`)
}

func mirrorTodos(sessionID string, snap []domain.TodoSnapshotItem) {
	raw, err := json.Marshal(map[string]any{
		"session": sessionID,
		"items":   mirrorItems(snap),
	})
	if err != nil {
		return
	}
	sock, err := socketPath()
	if err != nil {
		return
	}
	_, _ = server.CallRaw(sock, "todo.mirror", raw)
}

func mirrorItems(snap []domain.TodoSnapshotItem) []map[string]string {
	items := make([]map[string]string, 0, len(snap))
	for _, it := range snap {
		items = append(items, map[string]string{
			"text":   it.Text,
			"status": string(it.Status),
		})
	}
	return items
}
