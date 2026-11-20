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
)

const (
	// overviewPollEvery is how often `aiman overview run --wait` asks.
	overviewPollEvery = 5 * time.Second
	// overviewWaitFor outlasts the default run timeout.
	overviewWaitFor = 11 * time.Minute
)

const overviewUsage = "Usage: aiman overview [--run ID] [--json]\n       aiman overview run [--agent A] [--model M] [--wait] [--json]\n       aiman overview list [--json]\n"

// runOverview is `aiman overview`: the newest overview, a run, or the list.
func runOverview(args []string, out io.Writer) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	flags, rest := takeFlags(args)
	if len(rest) != 0 || (sub != "" && sub != "run" && sub != "list") {
		fmt.Fprint(os.Stderr, overviewUsage)
		return errUsage
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	asJSON := flags["json"] == "1"
	switch sub {
	case "list":
		res, err := callOverview(sock, "overview.list", map[string]any{})
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(res)
		}
		var runs struct {
			Runs []server.OverviewRun `json:"runs"`
		}
		if err := decodeInto(res, &runs); err != nil {
			return err
		}
		for _, r := range runs.Runs {
			fmt.Fprintf(out, "%s  %s  %-8s %s\n", r.ID, r.StartedAt.Local().Format("2006-01-02 15:04"), r.State, r.Agent)
		}
		return nil
	case "run":
		params := map[string]any{}
		for _, k := range []string{"agent", "model"} {
			if v := flags[k]; v != "" {
				params[k] = v
			}
		}
		res, err := callOverview(sock, "overview.run", params)
		if err != nil {
			return err
		}
		run, err := overviewRunOf(res)
		if err != nil {
			return err
		}
		if flags["wait"] != "1" {
			if asJSON {
				return writeJSON(res)
			}
			fmt.Fprintf(out, "overview %s started with %s (%d sessions); aiman overview shows it when done\n", run.ID, run.Agent, run.Sessions)
			return nil
		}
		for waited := time.Duration(0); waited < overviewWaitFor; waited += overviewPollEvery {
			time.Sleep(overviewPollEvery)
			res, err = callOverview(sock, "overview.get", map[string]any{"run": run.ID})
			if err != nil {
				return err
			}
			if run, err = overviewRunOf(res); err != nil {
				return err
			}
			if run.State == server.OverviewDone || run.State == server.OverviewFailed {
				break
			}
		}
		if asJSON {
			return writeJSON(res)
		}
		return formatOverview(out, run)
	default:
		params := map[string]any{}
		if v := flags["run"]; v != "" {
			params["run"] = v
		}
		res, err := callOverview(sock, "overview.get", params)
		if err != nil {
			return err
		}
		if asJSON {
			return writeJSON(res)
		}
		run, err := overviewRunOf(res)
		if err != nil {
			return err
		}
		return formatOverview(out, run)
	}
}

func callOverview(sock, method string, params map[string]any) (any, error) {
	resp, err := server.Call(sock, method, params)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return nil, err
	}
	if resp.Error != nil {
		writeCLIResponseError(resp.Error)
		return nil, errors.New(resp.Error.Message)
	}
	return resp.Result, nil
}

func decodeInto(v any, into any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

func overviewRunOf(res any) (server.OverviewRun, error) {
	var wrapped struct {
		Run server.OverviewRun `json:"run"`
	}
	err := decodeInto(res, &wrapped)
	return wrapped.Run, err
}

// formatOverview prints a run: its report when done, else its state.
func formatOverview(out io.Writer, run server.OverviewRun) error {
	when := run.StartedAt.Local().Format("2006-01-02 15:04")
	switch run.State {
	case server.OverviewDone:
		fmt.Fprintf(out, "overview %s · %s · %s\n\n%s", run.ID, when, run.Agent, run.Markdown)
		if !strings.HasSuffix(run.Markdown, "\n") {
			fmt.Fprintln(out)
		}
	case server.OverviewFailed:
		fmt.Fprintf(out, "overview %s · %s · failed: %s\n", run.ID, when, run.Error)
	default:
		fmt.Fprintf(out, "overview %s · %s · %s with %s (%d sessions)\n", run.ID, when, run.State, run.Agent, run.Sessions)
	}
	return nil
}
