package server

import (
	"slices"
	"testing"
)

// TestOpenAPIHasTranscript checks the phone contract for the transcript
// methods, their result types, the transcript event and the item kinds.
func TestOpenAPIHasTranscript(t *testing.T) {
	spec := loadOpenAPI(t)

	methods := specEnum(t, spec, "RpcRequest", "properties", "method")
	for _, m := range []string{"session.transcript", "session.transcript_item", "session.process_output"} {
		if !slices.Contains(methods, m) {
			t.Errorf("RpcRequest.method lacks %s", m)
		}
	}

	codes := specEnum(t, spec, "RpcError", "properties", "code")
	for _, c := range []string{"forbidden", "not_found", "unavailable", "session_scope", "invalid_params"} {
		if !slices.Contains(codes, c) {
			t.Errorf("RpcError.code lacks %s", c)
		}
	}

	results := resultSchemasByType(t, spec)
	page, ok := results["transcript"]
	if !ok {
		t.Fatal("RpcResponse.result has no schema with type const transcript")
	}
	assertProps(t, "transcript", page,
		"type", "session", "subagent", "source", "capabilities",
		"items", "changed", "removed", "before", "after", "reset")
	assertProps(t, "transcript.capabilities", mapAt(spec, page, "properties", "capabilities"),
		"agent", "scrollback", "tool_results", "thinking", "subagents", "processes")

	item, ok := results["transcript_item"]
	if !ok {
		t.Fatal("RpcResponse.result has no schema with type const transcript_item")
	}
	assertProps(t, "transcript_item", item, "type", "item")

	proc, ok := results["process_output"]
	if !ok {
		t.Fatal("RpcResponse.result has no schema with type const process_output")
	}
	assertProps(t, "process_output", proc, "type", "data", "offset", "size", "state", "exit_code", "truncated")

	kinds := specEnum(t, spec, "TranscriptItem", "properties", "kind")
	for _, k := range []string{"user", "assistant", "thinking", "tool_call", "subagent", "process", "system", "pane"} {
		if !slices.Contains(kinds, k) {
			t.Errorf("TranscriptItem.kind lacks %s", k)
		}
	}
	tItem := mapAt(spec, spec, "components", "schemas", "TranscriptItem")
	assertProps(t, "TranscriptItem", tItem,
		"id", "kind", "at", "rev", "text", "truncated", "tool_call", "subagent", "process")
	if _, ok := mapAt(spec, page, "properties", "items", "items", "properties")["kind"]; !ok {
		t.Error("transcript.items[] is not a TranscriptItem")
	}

	events := specEnum(t, spec, "SessionEvent", "properties", "type")
	if !slices.Contains(events, "transcript") {
		t.Error("SessionEvent.type lacks transcript")
	}
	assertProps(t, "SessionEvent", mapAt(spec, spec, "components", "schemas", "SessionEvent"), "id", "subagent", "rev")
}
