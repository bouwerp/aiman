package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

func runReview(args []string, getenv func(string) string) error {
	method, params, err := reviewRPC(args, getenv)
	if err != nil {
		return err
	}
	if method == "review.run" {
		return callReviewRun(params)
	}
	return callReview(method, params)
}

func reviewUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman review reveal [--single] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman review refresh [--force] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman review post <file> [--request-changes] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman review run [--timeout 10m] [--session NAME] -- <cmd...>\n")
	fmt.Fprintf(os.Stderr, "       aiman review debug [--lang go|node|react] [--target <package, script or dev command>] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman review coverage [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; one of them is required.\n")
	return errUsage
}

// reviewRunCLIError is a sandbox command's exit code, so main can os.Exit
// with it and skip the extra Error: line.
type reviewRunCLIError struct{ exit int }

func (e reviewRunCLIError) Error() string {
	return fmt.Sprintf("review run exit %d", e.exit)
}

// reviewRPC maps the CLI arguments onto review.* methods.
// Only reveal takes --single, only refresh takes --force, and only post takes
// a file and --request-changes (the default event is COMMENT).
func reviewRPC(args []string, getenv func(string) string) (string, map[string]any, error) {
	if len(args) == 0 {
		return "", nil, reviewUsage()
	}
	if args[0] == "run" {
		return reviewRunRPC(args[1:], getenv)
	}
	if args[0] == "debug" {
		return reviewDebugRPC(args[1:], getenv)
	}
	if args[0] != "reveal" && args[0] != "refresh" && args[0] != "post" && args[0] != "coverage" {
		return "", nil, reviewUsage()
	}
	verb := args[0]
	session, file, single, force, changes := "", "", false, false, false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--request-changes" && verb == "post":
			changes = true
		case verb == "post" && file == "" && !strings.HasPrefix(args[i], "-"):
			file = args[i]
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
	if session == "" || (verb == "post") != (file != "") {
		return "", nil, reviewUsage()
	}
	params := map[string]any{"session": session}
	if single {
		params["single"] = true
	}
	if force {
		params["force"] = true
	}
	if verb == "post" {
		params["file"] = file
		params["event"] = "COMMENT"
		if changes {
			params["event"] = "REQUEST_CHANGES"
		}
	}
	return "review." + verb, params, nil
}

func reviewRunRPC(args []string, getenv func(string) string) (string, map[string]any, error) {
	session, timeout := "", 10*time.Minute
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			cmd := append([]string(nil), args[i+1:]...)
			if session == "" {
				session = strings.TrimSpace(getenv("AIMAN_ID"))
			}
			if session == "" || len(cmd) == 0 {
				return "", nil, reviewUsage()
			}
			return "review.run", map[string]any{
				"session": session, "cmd": cmd, "timeout_seconds": int(timeout / time.Second),
			}, nil
		case args[i] == "--session" && i+1 < len(args):
			session = strings.TrimSpace(args[i+1])
			i++
		case args[i] == "--timeout" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return "", nil, reviewUsage()
			}
			timeout = d
			i++
		default:
			return "", nil, reviewUsage()
		}
	}
	return "", nil, reviewUsage()
}

func reviewDebugRPC(args []string, getenv func(string) string) (string, map[string]any, error) {
	session, lang, target := "", "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--lang" && i+1 < len(args):
			lang = strings.TrimSpace(args[i+1])
			i++
		case args[i] == "--target" && i+1 < len(args):
			target = strings.TrimSpace(args[i+1])
			i++
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
	if session == "" || lang == "" {
		return "", nil, reviewUsage()
	}
	params := map[string]any{"session": session, "lang": lang}
	if target != "" {
		params["target"] = target
	}
	return "review.debug", params, nil
}

func callReviewRun(params map[string]any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	wait := server.ReviewRunTimeout + 15*time.Second
	if n, ok := params["timeout_seconds"].(int); ok && n > 0 {
		if d := time.Duration(n)*time.Second + 15*time.Second; d > wait {
			wait = d
		}
	}
	resp, err := server.CallRawWait(sock, "review.run", raw, wait)
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
	var res struct {
		Output string `json:"output"`
		Exit   int    `json:"exit"`
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, res.Output)
	if res.Exit == 0 {
		return nil
	}
	return reviewRunCLIError{exit: res.Exit}
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
