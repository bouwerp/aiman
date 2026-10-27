package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func claudeToolLine(id, name, input string) string {
	return `{"type":"assistant","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"` + id + `","model":"claude-opus-5-5","content":[{"type":"tool_use","name":"` + name + `","input":{` + input + `}}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
}

func TestHealthClaudeReadThenEdit(t *testing.T) {
	var b strings.Builder
	b.WriteString(claudeToolLine("m1", "Read", `"file_path":"/wt/a.go"`))
	b.WriteString(claudeToolLine("m2", "Edit", `"file_path":"/wt/a.go"`))
	h, err := ClaudeHealth(strings.NewReader(b.String()), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Known || h.ToolCalls != 2 || h.ReadCalls != 1 || h.EditCalls != 1 {
		t.Fatalf("health = %+v", h)
	}
	if h.EditsWithoutRead != 0 || h.ReadEditRatio != 1.0 {
		t.Fatalf("health = %+v, want ratio 1.0 and 0 edits-without-read", h)
	}
}

func TestHealthClaudeEditWithoutRead(t *testing.T) {
	h, err := ClaudeHealth(strings.NewReader(claudeToolLine("m1", "Edit", `"file_path":"/wt/a.go"`)), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.EditsWithoutRead != 1 || h.EditCalls != 1 || h.ReadCalls != 0 {
		t.Fatalf("health = %+v, want 1 edit-without-read", h)
	}
}

func TestHealthClaudeOutsideWorktreeNotEdit(t *testing.T) {
	h, err := ClaudeHealth(strings.NewReader(claudeToolLine("m1", "Edit", `"file_path":"/elsewhere/a.go"`)), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.EditCalls != 0 || h.EditsWithoutRead != 0 {
		t.Fatalf("health = %+v, want outside edit ignored", h)
	}
}

func TestHealthClaudeFirstTestAndRetry(t *testing.T) {
	var b strings.Builder
	b.WriteString(claudeToolLine("m1", "Bash", `"command":"go test ./internal/usage/"`))
	b.WriteString(`{"type":"user","timestamp":"2026-10-21T07:26:15.630Z","message":{"role":"user","content":[{"type":"tool_result","content":"ModelRetry scheduled"}]}}` + "\n")
	h, err := ClaudeHealth(strings.NewReader(b.String()), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 21, 7, 25, 15, 630_000_000, time.UTC)
	if !h.FirstTestAt.Equal(want) {
		t.Fatalf("FirstTestAt = %v, want %v", h.FirstTestAt, want)
	}
	if h.Retries != 1 {
		t.Fatalf("Retries = %d, want 1", h.Retries)
	}
}

func TestHealthCodexExecCommand(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-22T06:08:01.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./internal/usage/\",\"workdir\":\"/wt\"}"}}` + "\n")
	b.WriteString(`{"timestamp":"2026-10-22T06:08:02.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","arguments":"{\"cmd\":\"go build ./...\",\"workdir\":\"/wt\"}"}}` + "\n")
	h, err := CodexHealth(strings.NewReader(b.String()), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Known || h.ToolCalls != 2 || h.ReadCalls != 0 || h.EditCalls != 0 {
		t.Fatalf("health = %+v", h)
	}
	if h.FirstTestAt.IsZero() {
		t.Fatalf("FirstTestAt zero, want exec_command test time: %+v", h)
	}
}

func TestHealthCodexReadThenEdit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-22T06:08:01.000Z","type":"response_item","payload":{"type":"function_call","name":"Read","arguments":"{\"file_path\":\"/wt/a.go\"}"}}` + "\n")
	b.WriteString(`{"timestamp":"2026-10-22T06:08:02.000Z","type":"response_item","payload":{"type":"function_call","name":"Edit","arguments":"{\"file_path\":\"/wt/a.go\"}"}}` + "\n")
	b.WriteString(`{"timestamp":"2026-10-22T06:08:03.000Z","type":"response_item","payload":{"type":"local_shell_call","action":{"command":"go test ./..."}}}` + "\n")
	h, err := CodexHealth(strings.NewReader(b.String()), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Known || h.ToolCalls != 3 || h.ReadCalls != 1 || h.EditCalls != 1 || h.EditsWithoutRead != 0 {
		t.Fatalf("health = %+v", h)
	}
	if h.FirstTestAt.IsZero() {
		t.Fatalf("FirstTestAt zero, want shell test time: %+v", h)
	}
}

func TestHealthUnknownShape(t *testing.T) {
	h, err := ClaudeHealth(strings.NewReader("{\"foo\":1}\n{\"bar\":2}\n"), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Known || h.ToolCalls != 0 {
		t.Fatalf("health = %+v, want Known=false and zero counts", h)
	}
}

func TestTrackerStoresHealth(t *testing.T) {
	dir := claudeProjectDir(t)
	worktree := t.TempDir()
	path := filepath.Join(dir, "00000000-0000-4000-8000-000000000001.jsonl")
	var b strings.Builder
	b.WriteString(claudeToolLine("m1", "Read", `"file_path":"`+filepath.Join(worktree, "a.go")+`"`))
	b.WriteString(claudeToolLine("m2", "Edit", `"file_path":"`+filepath.Join(worktree, "a.go")+`"`))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	env := newScanEnv(domain.Session{
		ID: "s1", AgentName: "claude", AgentSessionPath: path, WorktreePath: worktree,
	})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := env.store.meta("s1")
	if !got.HealthKnown || got.HealthReadCalls != 1 || got.HealthEditCalls != 1 || got.HealthEditsWithoutRead != 0 {
		t.Fatalf("meta health = %+v", got)
	}
	rep, err := (Reporter{Store: env.store}).HealthFor(context.Background(), Query{SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Known || rep.ReadEditRatio != 1.0 {
		t.Fatalf("health = %+v", rep)
	}
}

func TestHealthSkipsMalformedToolBlock(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"type":"assistant","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"m0","model":"claude-opus-5-5","content":[{"type":"tool_use","name":"Read","input":"oops"}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")
	b.WriteString(claudeToolLine("m1", "Read", `"file_path":"/wt/a.go"`))
	h, err := ClaudeHealth(strings.NewReader(b.String()), "/wt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.ReadCalls != 1 {
		t.Fatalf("health = %+v, want malformed block skipped", h)
	}
}
