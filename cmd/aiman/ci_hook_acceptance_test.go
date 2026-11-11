package main

// Acceptance tests for ci-minutes piece C (docs/specs/ci-minutes.md,
// Behaviour 2-3): aiman ci gate --hook, the Go half of the PreToolUse hook.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func claudePayload(command string) string {
	b, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}})
	return string(b)
}

type gateCall struct {
	method string
	params map[string]any
}

func gateCaller(answer string, err error, calls *[]gateCall) ciCaller {
	return func(method string, params map[string]any) (json.RawMessage, error) {
		*calls = append(*calls, gateCall{method, params})
		if err != nil {
			return nil, err
		}
		return json.RawMessage(answer), nil
	}
}

func hookEnv(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestCIGateHookAsksServeWithTheParsedCommand(t *testing.T) {
	var calls []gateCall
	var stderr bytes.Buffer
	code := ciGateHook(strings.NewReader(claudePayload("gh pr create --draft --base main")),
		hookEnv(map[string]string{"AIMAN_ID": "s1"}),
		gateCaller(`{"type":"ci_gate","allow":true}`, nil, &calls), &stderr)
	if code != 0 {
		t.Fatalf("allowed create: exit %d (%s)", code, stderr.String())
	}
	if len(calls) != 1 || calls[0].method != "ci.gate" {
		t.Fatalf("calls %+v, want one ci.gate", calls)
	}
	p := calls[0].params
	if p["session"] != "s1" || p["kind"] != "create" || p["draft"] != true || p["command"] != "gh pr create --draft --base main" {
		t.Fatalf("ci.gate params %+v", p)
	}
}

func TestCIGateHookBlocksWithReason(t *testing.T) {
	var calls []gateCall
	var stderr bytes.Buffer
	code := ciGateHook(strings.NewReader(claudePayload("git push")),
		hookEnv(map[string]string{"AIMAN_ID": "s1"}),
		gateCaller(`{"type":"ci_gate","allow":false,"reason":"aiman: run aiman ci local --wait first"}`, nil, &calls), &stderr)
	if code != 2 {
		t.Fatalf("refused push: exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "aiman: run aiman ci local --wait first") {
		t.Fatalf("stderr %q does not carry serve's reason", stderr.String())
	}
	if calls[0].params["kind"] != "push" || calls[0].params["command"] != "git push" {
		t.Fatalf("ci.gate params %+v", calls[0].params)
	}
}

func TestCIGateHookFailsClosedWhenServeDoesNotAnswer(t *testing.T) {
	var calls []gateCall
	var stderr bytes.Buffer
	code := ciGateHook(strings.NewReader(claudePayload("gh pr ready")),
		hookEnv(map[string]string{"AIMAN_ID": "s1"}),
		gateCaller("", errors.New("server_not_running"), &calls), &stderr)
	if code != 2 || stderr.Len() == 0 {
		t.Fatalf("serve down: exit %d stderr %q, want 2 with a reason", code, stderr.String())
	}
}

func TestCIGateHookLetsUnrelatedCommandsThrough(t *testing.T) {
	var calls []gateCall
	var stderr bytes.Buffer
	for _, payload := range []string{claudePayload("go test ./..."), claudePayload("gh pr view"), "not json"} {
		if code := ciGateHook(strings.NewReader(payload), hookEnv(map[string]string{"AIMAN_ID": "s1"}),
			gateCaller(`{"allow":false}`, nil, &calls), &stderr); code != 0 {
			t.Fatalf("%q: exit %d", payload, code)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("serve asked about unrelated commands: %+v", calls)
	}
}
