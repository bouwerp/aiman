package main

import (
	"encoding/json"
	"io"
	"testing"
)

// --repo reaches serve on ci local, ci status and pr ready.
func TestCIRepoFlag(t *testing.T) {
	var got []map[string]any
	call := func(method string, params map[string]any) (json.RawMessage, error) {
		got = append(got, params)
		switch method {
		case "ci.local":
			return json.RawMessage(`{"record":{"sha":"abc"},"done":false}`), nil
		case "ci.ready":
			return json.RawMessage(`{"ready":true,"number":2}`), nil
		}
		return json.RawMessage(`{"status":{"sha":"abc","has_list":false}}`), nil
	}
	env := func(string) string { return "s1" }
	if err := ciMain([]string{"local", "--repo", "o/b"}, env, call, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if err := ciMain([]string{"status", "--repo", "o/b"}, env, call, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if err := prMain([]string{"ready", "--repo", "o/b"}, env, call, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("calls %v", got)
	}
	for _, p := range got {
		if p["repo"] != "o/b" || p["session"] != "s1" || p["cwd"] == nil {
			t.Fatalf("params %v, want repo o/b, session s1 and cwd", p)
		}
	}
}
