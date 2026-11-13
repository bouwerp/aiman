package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

// runHostDaemonsCmd is `aiman host daemons [--json]`, `aiman host logs
// <daemon> [--lines N]` and `aiman host restart <daemon> [--force]`.
func runHostDaemonsCmd(args []string, out io.Writer) error {
	method, params, asJSON, ok := hostDaemonsArgs(args)
	if !ok {
		fmt.Fprint(os.Stderr, hostUsage)
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
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
		writeCLIResponseError(resp.Error)
		return errors.New(resp.Error.Message)
	}
	if asJSON {
		return writeJSON(resp.Result)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	switch method {
	case "host.daemons":
		var r server.HostDaemonsResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		return printHostDaemons(out, r, time.Now())
	case "host.logs":
		var r server.HostLogsResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		for _, l := range r.Lines {
			fmt.Fprintln(out, l)
		}
		if r.Redacted > 0 {
			fmt.Fprintf(os.Stderr, "(%d redacted, source %s)\n", r.Redacted, r.Source)
		}
		return nil
	default:
		var r server.HostRestartResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if r.Accepted {
			_, err = fmt.Fprintf(out, "accepted: restarting %s\n", r.Daemon)
			return err
		}
		reason := r.Reason
		if reason == "busy" {
			reason = "busy (long calls, logins, handovers or agent updates running); --force restarts anyway"
		}
		_, err = fmt.Fprintf(out, "refused: %s\n", reason)
		return err
	}
}

// hostDaemonsArgs parses the three subcommands into a method and params.
func hostDaemonsArgs(args []string) (method string, params map[string]any, asJSON, ok bool) {
	if len(args) == 0 {
		return "", nil, false, false
	}
	params = map[string]any{}
	rest := args[1:]
	switch args[0] {
	case "daemons":
		for _, a := range rest {
			if a != "--json" {
				return "", nil, false, false
			}
			asJSON = true
		}
		return "host.daemons", params, asJSON, true
	case "logs", "restart":
		method = "host." + args[0]
	default:
		return "", nil, false, false
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--json":
			asJSON = true
		case a == "--force" && method == "host.restart":
			params["force"] = true
		case (a == "--lines" || a == "-n") && method == "host.logs" && i+1 < len(rest):
			n, err := strconv.Atoi(rest[i+1])
			if err != nil || n <= 0 {
				return "", nil, false, false
			}
			params["lines"] = n
			i++
		case strings.HasPrefix(a, "-") || params["daemon"] != nil:
			return "", nil, false, false
		default:
			params["daemon"] = a
		}
	}
	if params["daemon"] == nil {
		return "", nil, false, false
	}
	return method, params, asJSON, true
}

// printHostDaemons is one row per daemon.
func printHostDaemons(w io.Writer, r server.HostDaemonsResult, now time.Time) error {
	fmt.Fprintf(w, "host %s\n", dash(r.Host))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DAEMON\tSTATE\tVERSION\tDRIVER\tPID\tUP\tRSS\tLAST RESTART")
	for _, d := range r.Daemons {
		if !d.Installed {
			fmt.Fprintf(tw, "%s\tnot installed\t-\t-\t-\t-\t-\t-\n", d.Kind)
			continue
		}
		up, pid, rss := "-", "-", "-"
		if !d.StartedAt.IsZero() {
			up = now.Sub(d.StartedAt).Truncate(time.Second).String()
		}
		if d.PID > 0 {
			pid = strconv.Itoa(d.PID)
		}
		if d.RSSBytes > 0 {
			rss = fmt.Sprintf("%.1fM", float64(d.RSSBytes)/(1<<20))
		}
		last := "-"
		if rs := d.Restart; rs != nil {
			last = fmt.Sprintf("%s %s", rs.Status, rs.At.Local().Format("2006-01-02 15:04"))
			if rs.Exit != 0 {
				last += fmt.Sprintf(" (exit %d)", rs.Exit)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Kind, dash(d.Active), dash(d.Version), dash(d.Driver), pid, up, rss, last)
	}
	return tw.Flush()
}
