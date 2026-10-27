package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/verify"
)

// errVerifyNotPassed is returned after the findings have been written to
// stdout, so main can exit 1 without an extra Error: line.
var errVerifyNotPassed = errors.New("verify: not passed")

// errVerifyGateBlocked is returned with exit status 2 for a PreToolUse gate.
var errVerifyGateBlocked = errors.New("verify: gate blocked")

const (
	defaultVerifyWaitTimeout = 9 * time.Minute
	defaultVerifyPollEvery   = 5 * time.Second
)

// runVerify starts a review and optionally waits for it.
func runVerify(args []string) error {
	flags, rest := takeFlags(args)
	if len(rest) != 0 {
		fmt.Fprintln(os.Stderr, "Usage: aiman verify [--session NAME] [--agent NAME] [--wait] [--if-enabled] [--gate] [--timeout DUR] [--json]")
		return errUsage
	}
	timeout := defaultVerifyWaitTimeout
	if raw := flags["timeout"]; raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify: invalid --timeout %q: %v\n", raw, err)
			return errUsage
		}
		timeout = d
	}
	params := map[string]any{}
	if flags["gate"] == "1" {
		return runVerifyGate(flags)
	}
	if v := strings.TrimSpace(flags["session"]); v != "" {
		params["session"] = v
	}
	if v := strings.TrimSpace(flags["agent"]); v != "" {
		params["agent"] = v
	}
	if flags["if-enabled"] == "1" {
		params["if_enabled"] = true
	}

	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	call := func(method string, p any) (server.Response, error) {
		return server.Call(sock, method, p)
	}
	resp, err := call("verify.start", params)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	review, err := decodeVerifyReview(resp)
	if err != nil {
		return err
	}
	if review.Status == verify.StatusDisabled {
		fmt.Fprintln(os.Stdout, "verify: disabled")
		return nil
	}
	if flags["wait"] != "1" {
		return printVerifyJSON(os.Stdout, review)
	}
	return waitForReview(call, review.ID, timeout, defaultVerifyPollEvery, os.Stdout, flags["json"] == "1")
}

// waitForReview polls verify.status until the review is no longer active.
func waitForReview(call func(method string, params any) (server.Response, error), id string, timeout, every time.Duration, out io.Writer, asJSON ...bool) error {
	if every <= 0 {
		every = defaultVerifyPollEvery
	}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := call("verify.status", map[string]any{"review": id})
		if err != nil {
			if !time.Now().Before(deadline) {
				return err
			}
			time.Sleep(every)
			continue
		}
		review, err := decodeVerifyReview(resp)
		if err != nil {
			return err
		}
		if !review.Status.Active() {
			if len(asJSON) > 0 && asJSON[0] {
				if err := printVerifyJSON(out, review); err != nil {
					return err
				}
				return verifyResultError(review)
			}
			return writeFinalReview(out, review)
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintln(out, "still running; run `aiman verify --wait` again")
			return errVerifyNotPassed
		}
		remain := time.Until(deadline)
		if remain < every {
			every = remain
		}
		if every <= 0 {
			fmt.Fprintln(out, "still running; run `aiman verify --wait` again")
			return errVerifyNotPassed
		}
		time.Sleep(every)
	}
}

func decodeVerifyReview(resp server.Response) (verify.Review, error) {
	if resp.Error != nil {
		return verify.Review{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return verify.Review{}, err
	}
	var body struct {
		Review *verify.Review `json:"review"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return verify.Review{}, fmt.Errorf("decode verify review: %w", err)
	}
	if body.Review == nil {
		return verify.Review{}, errors.New("decode verify review: no review object")
	}
	return *body.Review, nil
}

func writeFinalReview(out io.Writer, review verify.Review) error {
	switch review.Status {
	case verify.StatusDisabled:
		fmt.Fprintln(out, "verify: disabled")
		return nil
	case verify.StatusComplete:
		writeEvidence(out, review.Evidence)
		if review.Passed() {
			fmt.Fprintln(out, "verify: passed")
			return nil
		}
		fmt.Fprintln(out, "verify: not passed")
		for _, f := range review.Findings {
			if f.Severity == verify.SeverityRequired {
				fmt.Fprintf(out, "[REQUIRED] %s (%s)\n%s\n\n", f.Title, f.Location, f.Body)
			}
		}
		return errVerifyNotPassed
	case verify.StatusIncomplete:
		writeEvidence(out, review.Evidence)
		fmt.Fprintln(out, "review incomplete")
		return errVerifyNotPassed
	default:
		fmt.Fprintf(out, "review %s\n", review.Status)
		return errVerifyNotPassed
	}
}

// verifyResultError is the exit status for a final review, independent of
// whether it was printed as text or JSON.
// runVerifyGate checks the current tree against the latest passing review.
func runVerifyGate(flags map[string]string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	hash, err := verify.TreeHash(context.Background(), cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: cannot hash the current tree: %v\n", err)
		return errVerifyGateBlocked
	}
	sock, err := socketPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify: no passing review; run `aiman verify --wait`")
		return errVerifyGateBlocked
	}
	call := func(method string, p any) (server.Response, error) {
		return server.Call(sock, method, p)
	}
	return gateStatus(call, hash, strings.TrimSpace(flags["session"]))
}

func gateStatus(call func(string, any) (server.Response, error), hash, session string) error {
	params := map[string]any{"tree_hash": hash}
	if session != "" {
		params["session"] = session
	}
	resp, err := call("verify.status", params)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify: no passing review; run `aiman verify --wait`")
		return errVerifyGateBlocked
	}
	review, err := decodeVerifyReview(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify: no passing review; run `aiman verify --wait`")
		return errVerifyGateBlocked
	}
	if review.Status == verify.StatusDisabled || review.Passed() {
		return nil
	}
	required := review.Required
	if required == 0 {
		fmt.Fprintln(os.Stderr, "verify: review is not passed; run `aiman verify --wait`")
		return errVerifyGateBlocked
	}
	fmt.Fprintf(os.Stderr, "verify: blocked by %d REQUIRED finding(s); run `aiman verify --wait` and fix them\n", required)
	return errVerifyGateBlocked
}

func writeEvidence(out io.Writer, ev verify.Evidence) {
	if ev.Command == "" {
		fmt.Fprintln(out, "no test command configured")
		return
	}
	fmt.Fprintf(out, "evidence: %s → exit %d\n", ev.Command, ev.ExitCode)
	if ev.ExitCode != 0 {
		lines := strings.Split(strings.TrimSuffix(ev.Output, "\n"), "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		for _, line := range lines {
			fmt.Fprintln(out, line)
		}
	}
}

func verifyResultError(review verify.Review) error {
	if review.Status == verify.StatusDisabled || review.Passed() {
		return nil
	}
	return errVerifyNotPassed
}

func printVerifyJSON(out io.Writer, review verify.Review) error {
	body, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(body))
	return err
}
