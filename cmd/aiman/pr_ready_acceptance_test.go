package main

// Acceptance tests for ci-minutes piece C (docs/specs/ci-minutes.md,
// Behaviour 2): aiman pr ready.

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPRReadyCallsCIReady(t *testing.T) {
	var calls []gateCall
	var out bytes.Buffer
	err := prMain([]string{"ready"}, hookEnv(map[string]string{"AIMAN_ID": "s1"}),
		gateCaller(`{"type":"ci_ready","ready":true,"number":42}`, nil, &calls), &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].method != "ci.ready" || calls[0].params["session"] != "s1" {
		t.Fatalf("calls %+v, want ci.ready for s1", calls)
	}
	if !strings.Contains(out.String(), "#42") {
		t.Fatalf("output %q does not name the PR", out.String())
	}
}

func TestPRReadySessionFlagWins(t *testing.T) {
	var calls []gateCall
	var out bytes.Buffer
	if err := prMain([]string{"ready", "--session", "other"}, hookEnv(map[string]string{"AIMAN_ID": "s1"}),
		gateCaller(`{"type":"ci_ready","ready":true,"number":7}`, nil, &calls), &out); err != nil {
		t.Fatal(err)
	}
	if calls[0].params["session"] != "other" {
		t.Fatalf("params %+v", calls[0].params)
	}
}

func TestPRReadyReturnsServeRefusal(t *testing.T) {
	var calls []gateCall
	var out bytes.Buffer
	err := prMain([]string{"ready"}, hookEnv(map[string]string{"AIMAN_ID": "s1"}),
		gateCaller("", errors.New("ci_not_ready: no passing local gate for abc1234"), &calls), &out)
	if err == nil || !strings.Contains(err.Error(), "no passing local gate") {
		t.Fatalf("err %v, want serve's refusal", err)
	}
}

func TestPRReadyNeedsASession(t *testing.T) {
	var calls []gateCall
	var out bytes.Buffer
	if err := prMain([]string{"ready"}, hookEnv(nil), gateCaller(`{}`, nil, &calls), &out); err == nil || len(calls) != 0 {
		t.Fatalf("no session: err %v calls %d, want a usage error and no call", err, len(calls))
	}
	if err := prMain([]string{"create"}, hookEnv(map[string]string{"AIMAN_ID": "s1"}), gateCaller(`{}`, nil, &calls), &out); err == nil || len(calls) != 0 {
		t.Fatalf("unknown subcommand: err %v calls %d", err, len(calls))
	}
}
