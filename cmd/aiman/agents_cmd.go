package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/bouwerp/aiman/internal/server"
)

const agentsUsage = "Usage: aiman agents status [--json] [--refresh]\n       aiman agents update <command>|--all [--json]\n"

// agentsCall is one parsed `aiman agents` invocation.
type agentsCall struct {
	method string
	params map[string]any
	json   bool
}

// parseAgentsArgs maps `aiman agents …` onto agents.status or agents.update.
func parseAgentsArgs(args []string) (agentsCall, error) {
	if len(args) == 0 {
		return agentsCall{}, errors.New("missing subcommand")
	}
	call := agentsCall{params: map[string]any{}}
	var positional []string
	flags := map[string]bool{}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--") {
			flags[strings.TrimPrefix(a, "--")] = true
			continue
		}
		positional = append(positional, a)
	}
	call.json = flags["json"]
	delete(flags, "json")
	switch args[0] {
	case "status":
		call.method = "agents.status"
		if flags["refresh"] {
			call.params["refresh"] = true
			delete(flags, "refresh")
		}
		if len(positional) != 0 {
			return agentsCall{}, fmt.Errorf("status takes no arguments")
		}
	case "update":
		call.method = "agents.update"
		all := flags["all"]
		delete(flags, "all")
		switch {
		case all && len(positional) == 0:
			call.params["all"] = true
		case !all && len(positional) == 1:
			call.params["agent"] = positional[0]
		default:
			return agentsCall{}, fmt.Errorf("update takes one agent command or --all")
		}
	default:
		return agentsCall{}, fmt.Errorf("unknown subcommand %q", args[0])
	}
	for f := range flags {
		return agentsCall{}, fmt.Errorf("unknown flag --%s", f)
	}
	return call, nil
}

// runAgents reports and requests agent CLI updates through serve. It never
// runs an update itself.
func runAgents(args []string, out io.Writer) error {
	call, err := parseAgentsArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aiman agents: %v\n%s", err, agentsUsage)
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	resp, err := server.Call(sock, call.method, call.params)
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
	if call.json {
		return writeJSON(resp.Result)
	}
	if call.method == "agents.status" {
		return formatAgentsStatus(out, resp.Result)
	}
	if n := formatAgentsUpdate(out, resp.Result); n > 0 {
		return fmt.Errorf("%d update(s) not started", n)
	}
	return nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatAgentsStatus prints the agents.status result as a table.
func formatAgentsStatus(w io.Writer, result any) error {
	m, ok := result.(map[string]any)
	if !ok {
		return errors.New("agents result is not an object")
	}
	if _, err := fmt.Fprintf(w, "%s: mode %s, window %s\n", dash(str(m, "host")), dash(str(m, "mode")), dash(str(m, "window"))); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tINSTALLED\tLATEST\tMETHOD\tSTATUS")
	rows, _ := m["agents"].([]any)
	for _, r := range rows {
		a, ok := r.(map[string]any)
		if !ok {
			continue
		}
		status := str(a, "status")
		if held, _ := a["held"].(bool); held {
			status += " (held)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", str(a, "agent"), dash(str(a, "installed")), dash(str(a, "latest")), dash(str(a, "method")), status)
	}
	return tw.Flush()
}

// formatAgentsUpdate prints one line per agents.update target and returns how
// many were not started.
func formatAgentsUpdate(w io.Writer, result any) int {
	m, _ := result.(map[string]any)
	rows, _ := m["results"].([]any)
	if len(rows) == 0 {
		fmt.Fprintln(w, "no outdated agents")
		return 0
	}
	refused := 0
	for _, r := range rows {
		o, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if accepted, _ := o["accepted"].(bool); accepted {
			fmt.Fprintf(w, "%s: update started\n", str(o, "agent"))
			continue
		}
		refused++
		fmt.Fprintf(w, "%s: %s\n", str(o, "agent"), str(o, "reason"))
	}
	return refused
}
