package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/verify"
)

func verifySequence(reviews ...verify.Review) func(string, any) (server.Response, error) {
	i := 0
	return func(string, any) (server.Response, error) {
		r := reviews[i]
		if i < len(reviews)-1 {
			i++
		}
		return server.Response{Result: map[string]any{"review": r}}, nil
	}
}

func TestWaitExitCodePassed(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(
		verify.Review{ID: "r1", Status: verify.StatusRunning},
		verify.Review{ID: "r1", Status: verify.StatusComplete, Evidence: verify.Evidence{Command: "make test"}},
	), "r1", time.Second, time.Millisecond, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "verify: passed") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestWaitExitCodeRequired(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(verify.Review{
		ID: "r1", Status: verify.StatusComplete, Required: 1,
		Findings: []verify.Finding{{Severity: verify.SeverityRequired, Title: "Nil map", Location: "a.go:4", Body: "fix it"}},
	}), "r1", time.Second, time.Millisecond, &out)
	if !errors.Is(err, errVerifyNotPassed) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"[REQUIRED] Nil map (a.go:4)", "fix it"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("out missing %q: %q", want, out.String())
		}
	}
}

func TestWaitExitCodeIncomplete(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(verify.Review{ID: "r1", Status: verify.StatusIncomplete}), "r1", time.Second, time.Millisecond, &out)
	if !errors.Is(err, errVerifyNotPassed) || !strings.Contains(out.String(), "review incomplete") {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}

func TestWaitTimeout(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(verify.Review{ID: "r1", Status: verify.StatusRunning}), "r1", 20*time.Millisecond, 5*time.Millisecond, &out)
	if !errors.Is(err, errVerifyNotPassed) || !strings.Contains(out.String(), "still running") {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}

func TestWaitDisabled(t *testing.T) {
	var out bytes.Buffer
	if err := waitForReview(verifySequence(verify.Review{ID: "r1", Status: verify.StatusDisabled}), "r1", time.Second, time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "verify: disabled") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestWaitJSONRequiredStillFails(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(verify.Review{ID: "r1", Status: verify.StatusComplete, Required: 1}), "r1", time.Second, time.Millisecond, &out, true)
	if !errors.Is(err, errVerifyNotPassed) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), `"status": "complete"`) {
		t.Fatalf("out = %q", out.String())
	}
}

func TestWaitRetriesTransientStatusError(t *testing.T) {
	attempts := 0
	call := func(string, any) (server.Response, error) {
		attempts++
		if attempts == 1 {
			return server.Response{}, errors.New("temporary")
		}
		return server.Response{Result: map[string]any{"review": verify.Review{ID: "r1", Status: verify.StatusComplete, Evidence: verify.Evidence{Command: "go test", ExitCode: 0}}}}, nil
	}
	var out bytes.Buffer
	if err := waitForReview(call, "r1", time.Second, time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || !strings.Contains(out.String(), "verify: passed") {
		t.Fatalf("attempts=%d out=%q", attempts, out.String())
	}
}

func TestWaitPrintsEvidence(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(verify.Review{
		ID: "r1", Status: verify.StatusComplete,
		Evidence: verify.Evidence{Command: "go test ./...", ExitCode: 0},
	}), "r1", time.Second, time.Millisecond, &out)
	if err != nil || !strings.Contains(out.String(), "evidence: go test ./... → exit 0") {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}

func TestWaitPrintsMissingCommand(t *testing.T) {
	var out bytes.Buffer
	err := waitForReview(verifySequence(verify.Review{ID: "r1", Status: verify.StatusIncomplete}), "r1", time.Second, time.Millisecond, &out)
	if !errors.Is(err, errVerifyNotPassed) || !strings.Contains(out.String(), "no test command configured") {
		t.Fatalf("err=%v out=%q", err, out.String())
	}
}

func TestGateAllowsPassedReview(t *testing.T) {
	call := func(string, any) (server.Response, error) {
		return server.Response{Result: map[string]any{"review": verify.Review{
			Status: verify.StatusComplete, Evidence: verify.Evidence{Command: "go test", ExitCode: 0},
		}}}, nil
	}
	if err := gateStatus(call, "tree", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGateDeniesWithoutPass(t *testing.T) {
	call := func(string, any) (server.Response, error) {
		return server.Response{Error: &server.Error{Code: server.CodeNotFound, Message: "no review"}}, nil
	}
	if err := gateStatus(call, "tree", ""); !errors.Is(err, errVerifyGateBlocked) {
		t.Fatalf("err = %v", err)
	}
}

func TestGateDisabledIsZero(t *testing.T) {
	call := func(string, any) (server.Response, error) {
		return server.Response{Result: map[string]any{"review": verify.Review{Status: verify.StatusDisabled}}}, nil
	}
	if err := gateStatus(call, "tree", ""); err != nil {
		t.Fatal(err)
	}
}
