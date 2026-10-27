package main

import (
	"strings"
	"testing"
)

func TestRPCParams(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		raw     string
		wantErr string
	}{
		{"todo.add ok", "todo.add", `{"session":"s1","text":"x","as":"user"}`, ""},
		{"unknown method", "session.forget", `{}`, "unknown method"},
		{"not an object", "todo.get", `[1]`, "object"},
		{"steer.send ok", "steer.send", `{"session":"s1","text":"hi"}`, ""},
		{"handover.write ok", "handover.write", `{"session":"s1","reason":"archive"}`, ""},
		{"handover.point ok", "handover.point", `{"session":"s1"}`, ""},
		{"session.log ok", "session.log", `{"session":"s1"}`, ""},
		{"usage.get ok", "usage.get", `{"session":"s1"}`, ""},
		{"context.put ok", "context.put", `{"title":"T","body":"b"}`, ""},
		{"memory.jobs ok", "memory.jobs", `{}`, ""},
		{"memory.done ok", "memory.done", `{"id":"j1"}`, ""},
		{"context.verify ok", "context.verify", `{"id":"n1"}`, ""},
		{"ci.get ok", "ci.get", `{"session":"s1"}`, ""},
		{"ci.watch ok", "ci.watch", `{"session":"s1","on":false}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rpcParams(tc.method, []byte(tc.raw))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.raw {
				t.Fatalf("params changed: %s", got)
			}
		})
	}
}

func TestRPCMethodsNeverAllowMirror(t *testing.T) {
	if rpcMethods["todo.mirror"] {
		t.Fatal("todo.mirror must not be reachable from the laptop")
	}
}
