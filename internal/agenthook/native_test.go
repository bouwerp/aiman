package agenthook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeSessionID(t *testing.T) {
	t.Parallel()
	if SafeSessionID("4881040e-e03c-41e7-b36f-f1381450875a") == "" {
		t.Fatal("uuid")
	}
	if SafeSessionID("../etc/passwd") != "" {
		t.Fatal("dotdot")
	}
	if SafeSessionID("a/b") != "" {
		t.Fatal("slash")
	}
	if SafeSessionID("") != "" {
		t.Fatal("empty")
	}
}

func TestWriteAndParseStored(t *testing.T) {
	dir := t.TempDir()
	n := Report{Native: Native{ID: "conv-1", Path: "/tmp/x.jsonl"}}
	if err := WriteStored(dir, "sess-1", n); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, nativeDirName, "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	got := ParseStored(raw)
	if got.ID != "conv-1" || got.Path != "/tmp/x.jsonl" {
		t.Fatalf("%+v", got)
	}
}

func TestWriteStoredRejectsUnsafeID(t *testing.T) {
	dir := t.TempDir()
	if err := WriteStored(dir, "../x", Report{Native: Native{ID: "n"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseStoredFallsBackToHookPayload(t *testing.T) {
	got := ParseStored([]byte(`{"session_id":"abc"}`))
	if got.ID != "abc" {
		t.Fatalf("%+v", got)
	}
}

func TestParseSidecarDump(t *testing.T) {
	raw := "ID sess-1\n{\"id\":\"n1\",\"state\":\"idle\",\"source\":\"idle_prompt\"}\nEND\nID sess-2\n{\"id\":\"n2\",\"ended\":true}\nEND\n"
	got := ParseSidecarDump(raw)
	if got["sess-1"].State != "idle" || !got["sess-2"].Ended {
		t.Fatalf("%+v", got)
	}
}

func TestInferAgentNameFromText(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Claude Sonnet 5 <noreply@anthropic.com>", "Claude Code"},
		{"Codex <noreply@openai.com>", "Codex CLI"},
		{"GitHub Copilot <copilot@github.com>", "GitHub Copilot CLI"},
		{"Cursor Agent <cursor@cursor.sh>", "Cursor"},
		{"Grok Build <grok@x.ai>", "Grok Build CLI"},
		{"Antigravity <agy@google.com>", "Antigravity CLI"},
		{"Ageni <ageni@example.com>", "Ageni"},
		{"Some Human <human@example.com>", ""},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := InferAgentNameFromText(tt.text); got != tt.want {
			t.Errorf("InferAgentNameFromText(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestInferAgentName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/home/dev/.claude/projects/-home-dev-repo/abc-123.jsonl", "Claude Code"},
		{"/home/dev/.codex/sessions/2024/01/01/session.jsonl", "Codex CLI"},
		{"/home/dev/.copilot/sessions/session.json", "GitHub Copilot CLI"},
		{"/home/dev/.cursor/chats/abc.json", "Cursor"},
		{"/home/dev/.grok/sessions/abc.json", "Grok Build CLI"},
		{"/home/dev/.opencode/storage/session/abc.json", ""}, // no vendor hint mapped
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := InferAgentName(tt.path); got != tt.want {
			t.Errorf("InferAgentName(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
