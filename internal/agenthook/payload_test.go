package agenthook

import "testing"

func TestExtractNativeClaudeSessionStart(t *testing.T) {
	got := ExtractNative([]byte(`{"hook_event_name":"SessionStart","session_id":"4881040e-e03c-41e7-b36f-f1381450875a","cwd":"/wt"}`))
	if got.ID != "4881040e-e03c-41e7-b36f-f1381450875a" {
		t.Fatalf("id %q", got.ID)
	}
}

func TestExtractNativeGrokCamelCase(t *testing.T) {
	got := ExtractNative([]byte(`{"hookEventName":"session_start","sessionId":"01a020a7-8cff-7980-a99f-ea05c6879838","cwd":"/wt"}`))
	if got.ID != "01a020a7-8cff-7980-a99f-ea05c6879838" {
		t.Fatalf("id %q", got.ID)
	}
}

func TestExtractNativeNestedConversation(t *testing.T) {
	got := ExtractNative([]byte(`{"conversation":{"id":"conv-9","path":"/tmp/x.jsonl"}}`))
	if got.ID != "conv-9" {
		t.Fatalf("id %q", got.ID)
	}
	if got.Path != "/tmp/x.jsonl" {
		t.Fatalf("path %q", got.Path)
	}
}

func TestExtractNativeIgnoresGarbage(t *testing.T) {
	if got := ExtractNative([]byte("not json")); got.ID != "" {
		t.Fatalf("%+v", got)
	}
	if got := ExtractNative(nil); got.ID != "" {
		t.Fatalf("%+v", got)
	}
}
