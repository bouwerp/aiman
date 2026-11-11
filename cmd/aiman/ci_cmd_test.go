package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCILocalWaitPollsUntilDone(t *testing.T) {
	statuses := []string{
		`{"status":{"sha":"abc","clean":true,"has_list":true,"record":{"sha":"abc","status":"running","commands":[]}}}`,
		`{"status":{"sha":"abc","clean":true,"has_list":true,"record":{"sha":"abc","status":"fail","failed":"lint","commands":[{"name":"lint","exit":2,"log":"/l"}]},"tail":"boom"}}`,
	}
	var methods []string
	call := func(method string, params map[string]any) (json.RawMessage, error) {
		methods = append(methods, method)
		if method == "ci.local" {
			if params["wait"] != true || params["session"] != "s1" {
				t.Fatalf("params %v", params)
			}
			return json.RawMessage(`{"record":{"sha":"abc","status":"running"},"done":false}`), nil
		}
		s := statuses[0]
		statuses = statuses[1:]
		return json.RawMessage(s), nil
	}
	var out bytes.Buffer
	getenv := func(k string) string {
		if k == "AIMAN_ID" {
			return "s1"
		}
		return ""
	}
	err := ciMain([]string{"local", "--wait"}, getenv, call, &out, func(time.Duration) {})
	if !errors.Is(err, errCIFailed) {
		t.Fatalf("err = %v, want errCIFailed", err)
	}
	if got := strings.Join(methods, ","); got != "ci.local,ci.status,ci.status" {
		t.Fatalf("calls %s", got)
	}
	if s := out.String(); !strings.Contains(s, "failed: lint") || !strings.Contains(s, "boom") {
		t.Fatalf("output %q", s)
	}
}

func TestCIStatusPassExitsZero(t *testing.T) {
	call := func(string, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"status":{"sha":"abc","clean":true,"has_list":true,"record":{"sha":"abc","status":"pass","commands":[{"name":"t","exit":0}]}}}`), nil
	}
	var out bytes.Buffer
	if err := ciMain([]string{"status", "--session", "s1"}, func(string) string { return "" }, call, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pass") {
		t.Fatalf("output %q", out.String())
	}
}

func TestCIUsageNeedsASession(t *testing.T) {
	if err := ciMain([]string{"status"}, func(string) string { return "" }, nil, &bytes.Buffer{}, nil); !errors.Is(err, errUsage) {
		t.Fatalf("err = %v", err)
	}
}
