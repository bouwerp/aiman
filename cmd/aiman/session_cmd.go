package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// socketPath resolves the agent API socket for this host.
//
// AIMAN_SOCKET_PATH is honoured only when it actually points at something. It
// used to be trusted unconditionally, and older builds injected the *creating*
// machine's path into remote sessions — so a session on a remote was told the
// socket lived under the laptop's /Users/... home and every call failed with
// server_not_running while the real server was healthy a few directories away.
//
// A path that does not exist is never the right answer, and the running agents
// in those sessions cannot be re-environed without a restart, so falling back
// to this host's own default repairs them in place.
func socketPath() (string, error) {
	dir, dirErr := config.GetDir()

	if p := os.Getenv("AIMAN_SOCKET_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		// Only override a broken value when we have somewhere better to point.
		if dirErr == nil {
			if fallback := server.SocketPath(dir); fallback != p {
				if _, err := os.Stat(fallback); err == nil {
					return fallback, nil
				}
			}
		}
		// Nothing better available: keep it so the error names what was asked for.
		return p, nil
	}

	if dirErr != nil {
		return "", dirErr
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
		fmt.Fprintf(os.Stderr, "Usage: aiman session <list|get|create|rename|move|prompt|wait|read|report-agent-session> …\n")
		fmt.Fprintf(os.Stderr, "Are you an AI? Run: aiman --skill\n")
		return errUsage
	}
	if args[0] == "report-agent-session" {
		return runReportAgentSession(args[1:])
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
		// A params file keeps arbitrary text — the initial prompt above all —
		// out of argv, so no shell ever parses it.
		if pf := flags["params-file"]; pf != "" {
			raw, rerr := os.ReadFile(pf)
			if rerr != nil {
				writeCLIError(server.CodeInvalidParams, "params-file unreadable: "+rerr.Error())
				return errUsage
			}
			return callAndPrintRaw(sock, "session.create", raw)
		}
		params := map[string]any{}
		for _, k := range []string{"name", "group", "parent", "repo", "branch", "agent", "dir", "prompt", "issue", "base", "backend"} {
			if flags[k] != "" {
				params[k] = flags[k]
			}
		}
		if _, ok := flags["quick"]; ok {
			params["quick"] = true
		}
		if _, ok := flags["orphan"]; ok {
			params["orphan"] = true
		}
		if _, ok := flags["existing"]; ok {
			params["existing"] = true
		}
		if _, ok := flags["existing-branch"]; ok {
			params["existing_branch"] = true
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

var boolSessionFlags = map[string]bool{
	"wait": true, "force": true, "quick": true, "existing": true, "from-stdin": true, "ended": true, "dry-run": true,
	"existing-branch": true, "orphan": true,
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
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") && !boolSessionFlags[key] {
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

func runReportAgentSession(args []string) error {
	flags, _ := takeFlags(args)
	rep := agenthook.Report{
		Native:  agenthook.Native{ID: flags["id"], Path: flags["path"]},
		State:   domain.AgentState(flags["state"]),
		Source:  flags["source"],
		Message: flags["message"],
		Title:   flags["title"],
		Ended:   flags["ended"] == "1",
	}
	if _, ok := flags["from-stdin"]; ok {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		got := agenthook.ExtractReport(raw)
		if rep.ID == "" {
			rep.ID = got.ID
		}
		if rep.Path == "" {
			rep.Path = got.Path
		}
		if rep.State == "" {
			rep.State = got.State
		}
		if rep.Source == "" {
			rep.Source = got.Source
		}
		if rep.Message == "" {
			rep.Message = got.Message
		}
		if rep.Title == "" {
			rep.Title = got.Title
		}
		if !rep.Ended {
			rep.Ended = got.Ended
		}
		if got.Seq != 0 {
			rep.Seq = got.Seq
		}
	}
	sessionID := strings.TrimSpace(os.Getenv("AIMAN_ID"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(flags["session"])
	}
	if sessionID == "" || (rep.ID == "" && rep.State == "" && !rep.Ended && rep.Title == "") {
		return nil
	}
	if dir, err := config.GetDir(); err == nil {
		_ = agenthook.WriteStored(dir, sessionID, rep)
	}
	sock, err := socketPath()
	if err != nil {
		return writeJSON(reportAgentSessionResult(sessionID, rep))
	}
	if err := reportNativeToServe(sock, sessionID, rep); err != nil {
		writeCLIError(server.CodeInvalidParams, err.Error())
		return err
	}
	return writeJSON(reportAgentSessionResult(sessionID, rep))
}

func reportAgentSessionResult(sessionID string, r agenthook.Report) map[string]any {
	return map[string]any{
		"type":               "agent_session",
		"id":                 sessionID,
		"agent_session_id":   r.ID,
		"agent_session_path": r.Path,
		"state":              string(r.State),
		"title":              r.Title,
		"ended":              r.Ended,
	}
}

func reportNativeToServe(sock, sessionID string, r agenthook.Report) error {
	resp, err := server.Call(sock, "session.report_agent_session", map[string]any{
		"id":                 sessionID,
		"agent_session_id":   r.ID,
		"agent_session_path": r.Path,
		"state":              string(r.State),
		"source":             r.Source,
		"message":            r.Message,
		"title":              r.Title,
		"ended":              r.Ended,
		"seq":                r.Seq,
	})
	if err != nil {
		if errors.Is(err, server.ErrServerNotRunning) {
			return nil
		}
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	return nil
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
