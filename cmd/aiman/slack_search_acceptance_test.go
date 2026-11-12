package main

import "testing"

// Acceptance tests for slack-parity piece B (CLI). Do not edit.

func TestSlackSearchCommand(t *testing.T) {
	f, _ := newSlackFake(t, map[string]string{
		"assistant.search.context": `{"ok":true,"results":{"messages":[{"author_user_id":"U2","channel_id":"C1","message_ts":"1.0","content":"hi","permalink":"p"}]},"response_metadata":{"next_cursor":"n"}}`,
		"emoji.list":               `{"ok":true,"emoji":{"shipit":"https://e/s.png"}}`,
	})
	res := runSlackJSON(t, "search", "release freeze", "--private", "--in", "C1", "--from", "U2", "--limit", "5")
	if res["engine"] != "realtime" || res["next_cursor"] != "n" {
		t.Fatalf("search %v", res)
	}
	if hits, _ := res["hits"].([]any); len(hits) != 1 {
		t.Fatalf("hits %v", res)
	}
	runSlackJSON(t, "search", "eng", "--channels")
	em := runSlackJSON(t, "emoji", "ship")
	if list, _ := em["emoji"].([]any); len(list) != 1 {
		t.Fatalf("emoji %v", em)
	}
	if got := f.seen(); len(got) != 3 {
		t.Fatalf("calls %v", got)
	}
}
