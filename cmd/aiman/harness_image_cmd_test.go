package main

import (
	"errors"
	"strings"
	"testing"
)

const testImageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const imageJSON = `{"type":"harness_image","image":{"digest":"` + testImageDigest + `","ref":"redis:7","state":"%s","scan":{"critical":[],"high":["CVE-1"],"static_hits":[],"mal_ids":[],"report":"/r.md"},"approved_by":"operator","at":"2026-11-17T00:00:00Z"}}`

func imageAnswer(state string) string { return strings.Replace(imageJSON, "%s", state, 1) }

func TestHarnessCmdImageStatus(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.image_status": {imageAnswer("pending_approval")}}}
	out, _, err := runHarnessTest(t, f, nil, "image", "status", "redis:7")
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].params["ref"] != "redis:7" || !strings.Contains(out, "0123456789ab  redis:7  pending_approval") || !strings.Contains(out, "1 high (CVE-1)") {
		t.Fatalf("calls %v, out:\n%s", f.calls, out)
	}
	f = &fakeHarness{t: t, answers: map[string][]any{"harness.image_status": {imageAnswer("approved")}}}
	if _, _, err := runHarnessTest(t, f, nil, "image", "status", testImageDigest); err != nil || f.calls[0].params["digest"] != testImageDigest {
		t.Fatalf("status by digest: %v %v", f.calls, err)
	}
}

func TestHarnessCmdApproveImageNeedsQuote(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.image_approve": {imageAnswer("approved")}}}
	if _, _, err := runHarnessTest(t, f, nil, "approve-image", testImageDigest); !errors.Is(err, errUsage) || len(f.calls) != 0 {
		t.Fatalf("no quote: %v, calls %v", err, f.calls)
	}
	if _, _, err := runHarnessTest(t, f, nil, "approve-image", testImageDigest, "--quote", "ship it, highs accepted"); err != nil {
		t.Fatal(err)
	}
	if p := f.calls[0].params; p["digest"] != testImageDigest || p["quote"] != "ship it, highs accepted" {
		t.Fatalf("params = %v", p)
	}
}

func TestHarnessCmdImageApproveAcceptRisk(t *testing.T) {
	f := &fakeHarness{t: t, answers: map[string][]any{"harness.image_approve": {imageAnswer("approved")}}}
	if _, _, err := runHarnessTest(t, f, nil, "image", "approve", testImageDigest, "--accept-risk"); !errors.Is(err, errUsage) {
		t.Fatalf("accept-risk without reason: %v", err)
	}
	if _, _, err := runHarnessTest(t, f, nil, "image", "approve", testImageDigest, "--accept-risk", "--reason", "pinned fixture"); err != nil {
		t.Fatal(err)
	}
	if p := f.calls[0].params; p["accept_risk"] != true || p["reason"] != "pinned fixture" {
		t.Fatalf("params = %v", p)
	}
}

func TestHarnessCmdUpWaitImagePending(t *testing.T) {
	pending := func(stage string) error {
		return &harnessRPCError{Code: "harness_image_pending", Message: "pending",
			Data: []byte(`{"digest":"` + testImageDigest + `","ref":"redis:7","stage":"` + stage + `","retry_after_ms":15000}`)}
	}
	f := &fakeHarness{t: t, answers: map[string][]any{
		"harness.up": {pending("cve"), pending("static"), runJSON("ready")},
	}}
	_, errOut, err := runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "image redis:7 (0123456789ab): cve") || !strings.Contains(errOut, ": static") {
		t.Fatalf("stages printed:\n%s", errOut)
	}
	// Awaiting approval ends the wait with the refusal.
	f = &fakeHarness{t: t, answers: map[string][]any{"harness.up": {pending("awaiting_approval")}}}
	_, errOut, err = runHarnessTest(t, f, map[string]string{"AIMAN_ID": "s1"}, "up", "refund", "--wait")
	var ce cliExit
	if !errors.As(err, &ce) || !strings.Contains(errOut, "harness_image_pending") || len(f.calls) != 1 {
		t.Fatalf("awaiting approval: %v, calls %d:\n%s", err, len(f.calls), errOut)
	}
}
