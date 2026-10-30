package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestReviewParams(t *testing.T) {
	env := func(id string) func(string) string {
		return func(k string) string {
			if k == "AIMAN_ID" {
				return id
			}
			return ""
		}
	}
	cases := []struct {
		name   string
		args   []string
		env    string
		method string
		params map[string]any
	}{
		{"reveal from env", []string{"reveal"}, "s1", "review.reveal", map[string]any{"session": "s1"}},
		{"reveal single", []string{"reveal", "--single"}, "s1", "review.reveal", map[string]any{"session": "s1", "single": true}},
		{"reveal named", []string{"reveal", "--session", "pr-7"}, "", "review.reveal", map[string]any{"session": "pr-7"}},
		{"refresh", []string{"refresh"}, "s1", "review.refresh", map[string]any{"session": "s1"}},
		{"refresh forced", []string{"refresh", "--force"}, "s1", "review.refresh", map[string]any{"session": "s1", "force": true}},
		{"post", []string{"post", "review.md"}, "s1", "review.post", map[string]any{"session": "s1", "file": "review.md", "event": "COMMENT"}},
		{"post request changes", []string{"post", "review.md", "--request-changes"}, "s1", "review.post", map[string]any{"session": "s1", "file": "review.md", "event": "REQUEST_CHANGES"}},
		{"post flag first", []string{"post", "--request-changes", "--session", "s2", "r.md"}, "", "review.post", map[string]any{"session": "s2", "file": "r.md", "event": "REQUEST_CHANGES"}},
		{"run from env", []string{"run", "--", "go", "test", "./..."}, "s1", "review.run", map[string]any{"session": "s1", "cmd": []string{"go", "test", "./..."}, "timeout_seconds": 600}},
		{"run named timeout", []string{"run", "--timeout", "5m", "--session", "pr-7", "--", "go", "version"}, "", "review.run", map[string]any{"session": "pr-7", "cmd": []string{"go", "version"}, "timeout_seconds": 300}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method, params, err := reviewRPC(tc.args, env(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if method != tc.method || !reflect.DeepEqual(params, tc.params) {
				t.Fatalf("got %s %v, want %s %v", method, params, tc.method, tc.params)
			}
		})
	}
	for name, args := range map[string][]string{
		"no verb":        nil,
		"unknown verb":   {"approve"},
		"post no file":   {"post"},
		"post two files": {"post", "a.md", "b.md"},
		"reveal request": {"reveal", "--request-changes", "--session", "s1"},
		"no session":     {"reveal"},
		"refresh single": {"refresh", "--single", "--session", "s1"},
		"extra argument": {"reveal", "x", "--session", "s1"},
		"unknown flag":   {"reveal", "--force", "--session", "s1"},
		"bogus flag":     {"refresh", "--bogus", "--session", "s1"},
		"run no cmd":     {"run", "--session", "s1"},
		"run no dash":    {"run", "--session", "s1", "go", "test"},
	} {
		if _, _, err := reviewRPC(args, env("")); !errors.Is(err, errUsage) {
			t.Errorf("%s: err %v, want usage", name, err)
		}
	}
}
