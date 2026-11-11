package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/server"
)

// ciPollEvery is how often aiman ci local --wait asks ci.status.
const ciPollEvery = 5 * time.Second

// ciCaller sends one ci.* call and returns its result.
type ciCaller func(method string, params map[string]any) (json.RawMessage, error)

func runCI(args []string, getenv func(string) string) error {
	return ciMain(args, getenv, callCIMethod, os.Stdout, time.Sleep)
}

func ciUsage() error {
	fmt.Fprintf(os.Stderr, "Usage: aiman ci local [--wait] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman ci status [--json] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "       aiman ci gate --kind push|create|ready [--command CMD] [--draft] [--session NAME]\n")
	fmt.Fprintf(os.Stderr, "--session defaults to $AIMAN_ID; one of them is required.\n")
	fmt.Fprintf(os.Stderr, "ci watch and ci get: aiman rpc ci.watch / ci.get.\n")
	return errUsage
}

type ciFlags struct {
	session, kind, command string
	wait, json, draft      bool
}

func parseCIFlags(args []string, getenv func(string) string) (ciFlags, error) {
	var f ciFlags
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		switch a {
		case "--wait":
			f.wait = true
		case "--json":
			f.json = true
		case "--draft":
			f.draft = true
		case "--session", "--kind", "--command":
			if len(args) == 0 {
				return f, ciUsage()
			}
			v := args[0]
			args = args[1:]
			switch a {
			case "--session":
				f.session = v
			case "--kind":
				f.kind = v
			default:
				f.command = v
			}
		default:
			return f, ciUsage()
		}
	}
	if f.session = strings.TrimSpace(f.session); f.session == "" {
		f.session = strings.TrimSpace(getenv("AIMAN_ID"))
	}
	if f.session == "" {
		return f, ciUsage()
	}
	return f, nil
}

func ciMain(args []string, getenv func(string) string, call ciCaller, out io.Writer, sleep func(time.Duration)) error {
	if len(args) == 0 {
		return ciUsage()
	}
	f, err := parseCIFlags(args[1:], getenv)
	if err != nil {
		return err
	}
	switch args[0] {
	case "local":
		return ciLocal(f, call, out, sleep)
	case "status":
		return ciStatusCmd(f, call, out)
	case "gate":
		return ciGate(f, call, out)
	default:
		return ciUsage()
	}
}

func ciLocal(f ciFlags, call ciCaller, out io.Writer, sleep func(time.Duration)) error {
	raw, err := call("ci.local", map[string]any{"session": f.session, "wait": f.wait})
	if err != nil {
		return err
	}
	var res struct {
		Record cigate.Record `json:"record"`
		Done   bool          `json:"done"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	if !f.wait {
		_, _ = fmt.Fprintf(out, "local CI started for %s; aiman ci status shows the result\n", short(res.Record.SHA))
		return nil
	}
	sha := res.Record.SHA
	for !res.Done {
		sleep(ciPollEvery)
		st, err := fetchCIStatus(f.session, call)
		if err != nil {
			return err
		}
		if st.SHA != sha {
			return fmt.Errorf("HEAD moved from %s to %s while the gate ran", short(sha), short(st.SHA))
		}
		if st.Record != nil && st.Record.Status != cigate.StatusRunning {
			return printCIStatus(out, st)
		}
	}
	st, err := fetchCIStatus(f.session, call)
	if err != nil {
		return err
	}
	return printCIStatus(out, st)
}

func fetchCIStatus(session string, call ciCaller) (server.CIStatus, error) {
	raw, err := call("ci.status", map[string]any{"session": session})
	if err != nil {
		return server.CIStatus{}, err
	}
	var res struct {
		Status server.CIStatus `json:"status"`
	}
	err = json.Unmarshal(raw, &res)
	return res.Status, err
}

func ciStatusCmd(f ciFlags, call ciCaller, out io.Writer) error {
	st, err := fetchCIStatus(f.session, call)
	if err != nil {
		return err
	}
	if f.json {
		b, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	return printCIStatus(out, st)
}

// errCIFailed makes the CLI exit non-zero after it printed a failed record.
var errCIFailed = errors.New("local CI did not pass for HEAD")

func printCIStatus(out io.Writer, st server.CIStatus) error {
	head := "HEAD " + short(st.SHA)
	if !st.Clean {
		head += " (worktree has uncommitted changes)"
	}
	switch {
	case !st.HasList:
		_, _ = fmt.Fprintf(out, "%s: no local CI list (.aiman/ci-local.yaml or repos.<slug>.ci.local)\n", head)
		return nil
	case st.Record == nil:
		_, _ = fmt.Fprintf(out, "%s: no local CI run; aiman ci local --wait\n", head)
		return errCIFailed
	}
	r := st.Record
	_, _ = fmt.Fprintf(out, "%s: %s\n", head, r.Status)
	for _, c := range r.Commands {
		_, _ = fmt.Fprintf(out, "  %-20s exit %d  %s\n", c.Name, c.Exit, (time.Duration(c.Millis) * time.Millisecond).Round(time.Second))
	}
	if r.Error != "" {
		_, _ = fmt.Fprintf(out, "  error: %s\n", r.Error)
	}
	if r.Status == cigate.StatusPass {
		return nil
	}
	if r.Failed != "" {
		_, _ = fmt.Fprintf(out, "\nfailed: %s (log %s)\n", r.Failed, r.Commands[len(r.Commands)-1].Log)
	}
	if st.Tail != "" {
		_, _ = fmt.Fprintf(out, "--- last lines ---\n%s\n", st.Tail)
	}
	if r.Status == cigate.StatusRunning {
		return nil
	}
	return errCIFailed
}

func ciGate(f ciFlags, call ciCaller, out io.Writer) error {
	if f.kind == "" {
		return ciUsage()
	}
	params := map[string]any{"session": f.session, "kind": f.kind}
	if f.command != "" {
		params["command"] = f.command
	}
	if f.draft {
		params["draft"] = true
	}
	raw, err := call("ci.gate", params)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, string(raw))
	var res struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	if !res.Allow {
		return errors.New(res.Reason)
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func callCIMethod(method string, params map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	sock, err := socketPath()
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return nil, err
	}
	resp, err := server.CallRaw(sock, method, raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeInvalidParams
		}
		writeCLIError(code, err.Error())
		return nil, err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return nil, errors.New(resp.Error.Message)
	}
	return json.Marshal(resp.Result)
}
