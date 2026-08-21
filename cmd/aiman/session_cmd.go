package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func socketPath() (string, error) {
	if p := os.Getenv("AIMAN_SOCKET_PATH"); p != "" {
		return p, nil
	}
	dir, err := config.GetDir()
	if err != nil {
		return "", err
	}
	return server.SocketPath(dir), nil
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeCLIError(code, msg string) {
	_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func runSession(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: aiman session <list|get|create|rename|move|prompt|wait|read> …\n")
		fmt.Fprintf(os.Stderr, "Are you an AI? Run: aiman --skill\n")
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		params := map[string]any{}
		flags, _ := takeFlags(args[1:])
		if g := flags["group"]; g != "" {
			params["group"] = g
		}
		return callAndPrint(sock, "session.list", params)
	case "get":
		if len(args) < 2 {
			writeCLIError(server.CodeInvalidParams, "session get requires a target")
			return errUsage
		}
		return callAndPrint(sock, "session.get", map[string]any{"id": args[1]})
	case "read":
		if len(args) < 2 {
			writeCLIError(server.CodeInvalidParams, "session read requires a target")
			return errUsage
		}
		flags, _ := takeFlags(args[2:])
		params := map[string]any{"id": args[1]}
		if n := flags["lines"]; n != "" {
			params["lines"] = atoi(n)
		}
		return callAndPrint(sock, "session.read", params)
	case "prompt":
		if len(args) < 3 {
			writeCLIError(server.CodeInvalidParams, "session prompt <target> TEXT")
			return errUsage
		}
		flags, rest := takeFlags(args[2:])
		text := strings.Join(rest, " ")
		params := map[string]any{"id": args[1], "text": text}
		if _, ok := flags["wait"]; ok {
			params["wait"] = true
		}
		if _, ok := flags["force"]; ok {
			params["force"] = true
		}
		if u := flags["until"]; u != "" {
			params["until"] = u
		}
		if t := flags["timeout"]; t != "" {
			params["timeout_ms"] = parseTimeoutMS(t)
		}
		return callAndPrint(sock, "session.prompt", params)
	case "wait":
		if len(args) < 2 {
			writeCLIError(server.CodeInvalidParams, "session wait requires a target")
			return errUsage
		}
		flags, _ := takeFlags(args[2:])
		params := map[string]any{"id": args[1]}
		if u := flags["until"]; u != "" {
			params["until"] = u
		}
		if t := flags["timeout"]; t != "" {
			params["timeout_ms"] = parseTimeoutMS(t)
		}
		return callAndPrint(sock, "session.wait", params)
	case "create":
		flags, _ := takeFlags(args[1:])
		params := map[string]any{}
		for _, k := range []string{"name", "group", "repo", "branch", "agent", "dir", "prompt", "issue", "base"} {
			if flags[k] != "" {
				params[k] = flags[k]
			}
		}
		if _, ok := flags["quick"]; ok {
			params["quick"] = true
		}
		if _, ok := flags["existing"]; ok {
			params["existing"] = true
		}
		return callAndPrint(sock, "session.create", params)
	case "rename":
		if len(args) < 3 {
			writeCLIError(server.CodeInvalidParams, "session rename <target> NEW-NAME")
			return errUsage
		}
		return callAndPrint(sock, "session.rename", map[string]any{"id": args[1], "name": args[2]})
	case "move":
		flags, rest := takeFlags(args[1:])
		target := ""
		if len(rest) > 0 {
			target = rest[0]
		}
		if target == "" || flags["group"] == "" {
			writeCLIError(server.CodeInvalidParams, "session move <target> --group GROUP")
			return errUsage
		}
		return callAndPrint(sock, "session.move", map[string]any{"id": target, "group": flags["group"]})
	default:
		writeCLIError(server.CodeInvalidParams, "unknown session command "+args[0])
		return errUsage
	}
}

func takeFlags(args []string) (map[string]string, []string) {
	flags := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") && key != "wait" && key != "force" && key != "quick" && key != "existing" {
			flags[key] = args[i+1]
			i++
			continue
		}
		flags[key] = "1"
	}
	return flags, rest
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func parseTimeoutMS(s string) int {
	mult := 1
	if strings.HasSuffix(s, "ms") {
		s = strings.TrimSuffix(s, "ms")
	} else if strings.HasSuffix(s, "s") {
		s = strings.TrimSuffix(s, "s")
		mult = 1000
	}
	return atoi(s) * mult
}

func callAndPrint(sock, method string, params any) error {
	resp, err := server.Call(sock, method, params)
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

func blockBareTUI(aimanEnv string, tty bool) bool {
	return strings.TrimSpace(aimanEnv) == "1" || !tty
}
