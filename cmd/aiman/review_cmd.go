package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bouwerp/aiman/internal/server"
)

func runReview(args []string, getenv func(string) string) error {
	method, params, err := reviewRPC(args, getenv)
	if err != nil {
		return err
	}
	return callReview(method, params)
}

func reviewUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman review reveal [--single] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman review refresh [--force] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; one of them is required.\n")
	return errUsage
}

// reviewRPC maps the CLI arguments onto review.reveal or review.refresh.
// Only reveal takes --single and only refresh takes --force.
func reviewRPC(args []string, getenv func(string) string) (string, map[string]any, error) {
	if len(args) == 0 || (args[0] != "reveal" && args[0] != "refresh") {
		return "", nil, reviewUsage()
	}
	verb := args[0]
	session, single, force := "", false, false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--single" && verb == "reveal":
			single = true
		case args[i] == "--force" && verb == "refresh":
			force = true
		case args[i] == "--session" && i+1 < len(args):
			session = strings.TrimSpace(args[i+1])
			i++
		default:
			return "", nil, reviewUsage()
		}
	}
	if session == "" {
		session = strings.TrimSpace(getenv("AIMAN_ID"))
	}
	if session == "" {
		return "", nil, reviewUsage()
	}
	params := map[string]any{"session": session}
	if single {
		params["single"] = true
	}
	if force {
		params["force"] = true
	}
	return "review." + verb, params, nil
}

func callReview(method string, params map[string]any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.CallRaw(sock, method, raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	return writeJSON(resp.Result)
}
