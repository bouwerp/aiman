package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

func TestTodoMovePositionIsOneBased(t *testing.T) {
	method, params, err := todoParams("move", []string{"t4", "2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if method != "todo.move" {
		t.Fatalf("method %s", method)
	}
	if params["id"] != "t4" {
		t.Fatalf("id %#v", params["id"])
	}
	if params["to_index"] != 1 {
		t.Fatalf("to_index %#v, want 1", params["to_index"])
	}
}

func TestTodoParamsCommands(t *testing.T) {
	after := map[string]string{"after": "t1"}
	cases := []struct {
		name   string
		cmd    string
		rest   []string
		flags  map[string]string
		method string
		key    string
		want   any
	}{
		{name: "list", cmd: "list", method: "todo.get"},
		{name: "add", cmd: "add", rest: []string{"buy", "milk"}, flags: after, method: "todo.add", key: "text", want: "buy milk"},
		{name: "add keeps spacing", cmd: "add", rest: []string{"buy  milk"}, method: "todo.add", key: "text", want: "buy  milk"},
		{name: "start", cmd: "start", rest: []string{"t2"}, method: "todo.update", key: "state", want: "active"},
		{name: "done", cmd: "done", rest: []string{"t2"}, method: "todo.update", key: "state", want: "done"},
		{name: "drop", cmd: "drop", rest: []string{"t2"}, method: "todo.update", key: "state", want: "dropped"},
		{name: "edit", cmd: "edit", rest: []string{"t2", "new", "text"}, method: "todo.update", key: "text", want: "new text"},
		{name: "rm", cmd: "rm", rest: []string{"t9"}, method: "todo.remove", key: "id", want: "t9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method, params, err := todoParams(tc.cmd, tc.rest, tc.flags)
			if err != nil {
				t.Fatal(err)
			}
			if method != tc.method {
				t.Fatalf("method %s, want %s", method, tc.method)
			}
			if tc.key != "" && params[tc.key] != tc.want {
				t.Fatalf("%s = %#v, want %#v", tc.key, params[tc.key], tc.want)
			}
			if _, ok := params["as"]; ok {
				t.Fatal("todoParams must not invent as")
			}
		})
	}
	_, params, err := todoParams("add", []string{"buy", "milk"}, after)
	if err != nil {
		t.Fatal(err)
	}
	if params["after"] != "t1" {
		t.Fatalf("after %#v", params["after"])
	}
}

func TestTodoRequiresSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AIMAN_SOCKET_PATH", filepath.Join(home, "missing.sock"))
	err := runTodo([]string{"list"}, strings.NewReader(""), func(string) string { return "" })
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
	err = runTodo([]string{"list", "--session", "s1"}, strings.NewReader(""), func(string) string { return "" })
	if err == nil || errors.Is(err, errUsage) {
		t.Fatalf("--session should pass usage and fail on the socket, got %v", err)
	}
}

func TestTodoJSONIsBooleanFlag(t *testing.T) {
	flags, rest := takeFlags([]string{"--json", "t1", "new text"})
	if flags["json"] != "1" {
		t.Fatalf("json flag %#v", flags["json"])
	}
	if strings.Join(rest, " ") != "t1 new text" {
		t.Fatalf("rest %q", rest)
	}
}

func TestFormatTodoLinesIncludesDropped(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{
		{ID: "t5", Text: "text", State: domain.TodoDropped},
		{ID: "t1", Text: "keep", State: domain.TodoPending},
	}}
	got := formatTodoLines(list)
	want := todo.FormatLine(list.Items[0]) + "\n" + todo.FormatLine(list.Items[1])
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !strings.Contains(got, "t5 \u2013 text") {
		t.Fatalf("dropped line %q", got)
	}
}

func TestTodoReportNoopsOnGarbage(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var reqs atomic.Int32
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			reqs.Add(1)
			br := bufio.NewReader(c)
			_, _ = br.ReadBytes('\n')
			_, _ = c.Write([]byte("{\"id\":\"1\",\"result\":{}}\n"))
			_ = c.Close()
		}
	}()
	t.Setenv("AIMAN_SOCKET_PATH", sock)

	stdout, stderr := captureStdIO(t, func() {
		err = runTodo([]string{"report", "--from-stdin"}, strings.NewReader("garbage"), func(k string) string {
			if k == "AIMAN_ID" {
				return "x"
			}
			return ""
		})
	})
	if err != nil {
		t.Fatalf("report returned %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
	time.Sleep(50 * time.Millisecond)
	if n := reqs.Load(); n != 0 {
		t.Fatalf("listener saw %d requests", n)
	}
}

func TestTodoAddSendsTextAndAs(t *testing.T) {
	body, out := captureTodoRequest(t, func() error {
		return runTodo([]string{"add", "--as", "user", "--session", "s1", "buy", "milk"}, strings.NewReader(""), func(string) string { return "" })
	})
	if !strings.Contains(body, `"method":"todo.add"`) {
		t.Fatalf("method missing in %s", body)
	}
	if !strings.Contains(body, `"text":"buy milk"`) || !strings.Contains(body, `"as":"user"`) || !strings.Contains(body, `"session":"s1"`) {
		t.Fatalf("params %s", body)
	}
	if !strings.Contains(out, "t1") {
		t.Fatalf("stdout %q should list the result", out)
	}
}

func TestTodoReportMirrorsFullList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".claude", "tasks", "sess-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	task := `{"id":"1","subject":"Outline README","status":"in_progress"}`
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(task), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"PostToolUse","session_id":"sess-1","tool_name":"TaskUpdate","tool_input":{"taskId":"1","status":"in_progress"}}`
	body, out := captureTodoRequest(t, func() error {
		return runTodo([]string{"report", "--from-stdin"}, strings.NewReader(payload), func(k string) string {
			if k == "AIMAN_ID" {
				return "aim-1"
			}
			return ""
		})
	})
	if out != "" {
		t.Fatalf("report printed %q", out)
	}
	if !strings.Contains(body, `"method":"todo.mirror"`) {
		t.Fatalf("method missing in %s", body)
	}
	if !strings.Contains(body, `"session":"aim-1"`) || !strings.Contains(body, `"text":"Outline README"`) || !strings.Contains(body, `"status":"active"`) {
		t.Fatalf("mirror params %s", body)
	}
}

func captureTodoRequest(t *testing.T, fn func() error) (string, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		line, _ := br.ReadBytes('\n')
		got <- string(line)
		list := `{"type":"todo_list","list":{"session_id":"s1","items":[{"id":"t1","text":"buy milk","state":"pending"}]}}`
		_, _ = c.Write([]byte(`{"id":"1","result":` + list + "}\n"))
	}()
	t.Setenv("AIMAN_SOCKET_PATH", sock)
	var runErr error
	out, _ := captureStdIO(t, func() { runErr = fn() })
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	select {
	case body := <-got:
		return body, out
	case <-time.After(2 * time.Second):
		t.Fatal("no request")
	}
	return "", ""
}

func captureStdIO(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	fn()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var stdout, stderr bytes.Buffer
	if _, err := io.Copy(&stdout, outR); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(&stderr, errR); err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String()
}

func TestTodoParamsGateCommands(t *testing.T) {
	method, params, err := todoParams("gate", nil, nil)
	if err != nil || method != "todo.gate" || len(params) != 0 {
		t.Fatalf("gate = %s %v %v", method, params, err)
	}
	method, params, err = todoParams("approve-design", nil, map[string]string{"quote": " go ahead "})
	if err != nil || method != "todo.approve_design" || params["quote"] != "go ahead" {
		t.Fatalf("approve-design = %s %v %v", method, params, err)
	}
	if _, _, err = todoParams("approve-design", nil, map[string]string{}); err == nil {
		t.Fatal("approve-design without a quote must fail")
	}
}

func TestTodoGateReviewFlag(t *testing.T) {
	for _, args := range [][]string{
		{"gate", "--session", "s1", "--review"},
		{"gate", "--review", "--session", "s1"},
	} {
		body, _ := captureTodoRequest(t, func() error {
			return runTodo(args, strings.NewReader(""), func(string) string { return "" })
		})
		if !strings.Contains(body, `"method":"todo.gate"`) || !strings.Contains(body, `"ref":"gate:review"`) {
			t.Fatalf("%v sent %s", args, body)
		}
	}
	body, _ := captureTodoRequest(t, func() error {
		return runTodo([]string{"approve-review", "--session", "s1", "--quote", "lgtm"}, strings.NewReader(""), func(string) string { return "" })
	})
	if !strings.Contains(body, `"method":"todo.approve_review"`) || !strings.Contains(body, `"quote":"lgtm"`) {
		t.Fatalf("approve-review sent %s", body)
	}
}
