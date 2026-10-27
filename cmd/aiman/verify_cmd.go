package main

import (
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

const (
	defaultVerifyWaitTimeout = 9 * time.Minute
	defaultVerifyPollEvery   = 5 * time.Second
)

// runVerify starts a review and optionally waits for it.
func runVerify(args []string) error {
	flags, rest := takeFlags(args)
	if len(rest) != 0 {
		fmt.Fprintln(os.Stderr, "Usage: aiman verify [--session NAME] [--agent NAME] [--wait] [--if-enabled] [--timeout DUR] [--json]")
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
			return err
		}
		review, err := decodeVerifyReview(resp)
		if err != nil {
			return err
		}
		if !review.Status.Active() {
			if len(asJSON) > 0 && asJSON[0] {
				return printVerifyJSON(out, review)
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
		fmt.Fprintln(out, "review incomplete")
		return errVerifyNotPassed
	default:
		fmt.Fprintf(out, "review %s\n", review.Status)
		return errVerifyNotPassed
	}
}

func printVerifyJSON(out io.Writer, review verify.Review) error {
	body, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(body))
	return err
}
