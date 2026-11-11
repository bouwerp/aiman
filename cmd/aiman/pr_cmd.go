package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func runPR(args []string, getenv func(string) string) error {
	return prMain(args, getenv, callCIMethod, os.Stdout)
}

func prUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman pr ready [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "Marks the session's PR ready once the tree is clean, HEAD is pushed and the PR's head, and the local gate (and verify, when on) passed for HEAD.\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; one of them is required.\n")
	return errUsage
}

// prMain is `aiman pr ready`: serve checks the PR is ready (ci.ready) and
// runs gh pr ready itself.
func prMain(args []string, getenv func(string) string, call ciCaller, out io.Writer) error {
	if len(args) == 0 || args[0] != "ready" {
		return prUsage()
	}
	flags, rest := takeFlags(args[1:])
	if len(rest) != 0 {
		return prUsage()
	}
	session := strings.TrimSpace(flags["session"])
	if session == "" {
		session = strings.TrimSpace(getenv("AIMAN_ID"))
	}
	if session == "" {
		return prUsage()
	}
	raw, err := call("ci.ready", map[string]any{"session": session})
	if err != nil {
		return err
	}
	var res struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "PR #%d is ready for review; its CI runs now\n", res.Number)
	return err
}
