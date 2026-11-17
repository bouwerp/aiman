package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/server"
)

// harnessPollEvery is how often `harness up --wait` asks harness.status while
// the run is starting.
const harnessPollEvery = 2 * time.Second

// harnessUpWaitLimit bounds `harness up --wait` polling after serve answered.
const harnessUpWaitLimit = 15 * time.Minute

// harnessRPCError is serve's refusal: its code, message and data.
type harnessRPCError struct {
	Code    string
	Message string
	Data    json.RawMessage
}

func (e *harnessRPCError) Error() string { return e.Code + ": " + e.Message }

// harnessCaller sends one harness.* call to the host's serve.
type harnessCaller func(method string, params map[string]any) (json.RawMessage, error)

type harnessIO struct {
	getenv func(string) string
	call   harnessCaller
	out    io.Writer
	errOut io.Writer
	sleep  func(time.Duration)
}

// runHarness dispatches `aiman harness`: the hidden edge, the laptop console,
// and the host commands that call serve.
func runHarness(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "edge":
			return runHarnessEdge(args[1:])
		case "console":
			return runHarnessConsole(args[1:])
		case "scan":
			return runHarnessScan(args[1:])
		}
	}
	return harnessMain(args, harnessIO{
		getenv: os.Getenv, call: callHarnessMethod, out: os.Stdout, errOut: os.Stderr, sleep: time.Sleep,
	})
}

// callHarnessMethod calls serve on this host.
func callHarnessMethod(method string, params map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	sock, err := socketPath()
	if err != nil {
		return nil, &harnessRPCError{Code: server.CodeServerNotRunning, Message: err.Error()}
	}
	resp, err := server.CallRaw(sock, method, raw)
	if err != nil {
		code := server.CodeServerNotRunning
		if !errors.Is(err, server.ErrServerNotRunning) {
			code = server.CodeTimeout
		}
		return nil, &harnessRPCError{Code: code, Message: err.Error()}
	}
	if resp.Error != nil {
		e := &harnessRPCError{Code: resp.Error.Code, Message: resp.Error.Message}
		if resp.Error.Data != nil {
			e.Data, _ = json.Marshal(resp.Error.Data)
		}
		return nil, e
	}
	return json.Marshal(resp.Result)
}

const harnessUsageText = `Usage: aiman harness list [--json]
       aiman harness up <scenario> [--dirty] [--force] [--wait] [--json]
       aiman harness status [--run ID] [--json]
       aiman harness check [<name>] [--run ID] [--json]
       aiman harness logs <service> [--lines N] [--run ID] [--json]
       aiman harness result [<run>] [--run ID] [--json]
       aiman harness verdict <step|all> pass|fail|skip [--note TEXT] [--run ID] [--json]   (operator)
       aiman harness down [--run ID] [--json]
       aiman harness console [--remote NAME] [--session NAME] [--run ID]   (laptop)
Every host command takes --session NAME, which defaults to $AIMAN_ID; --run ID names a run instead.
`

func harnessUsage(w io.Writer) error {
	_, _ = io.WriteString(w, harnessUsageText)
	return errUsage
}

// harnessArgs are the parsed flags and positional arguments of a subcommand.
type harnessArgs struct {
	session, run, note, remote string
	lines                      int
	json, dirty, force, wait   bool
	pos                        []string
}

var harnessValueFlags = map[string]bool{"--session": true, "--run": true, "--note": true, "--lines": true, "--remote": true}

func parseHarnessArgs(args []string) (harnessArgs, error) {
	var a harnessArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, val, hasVal := strings.Cut(arg, "=")
		if harnessValueFlags[name] {
			if !hasVal {
				if i+1 >= len(args) {
					return a, fmt.Errorf("%s needs a value", name)
				}
				i++
				val = args[i]
			}
			if err := a.setValue(name, val); err != nil {
				return a, err
			}
			continue
		}
		if !a.setBool(arg) {
			if strings.HasPrefix(arg, "-") {
				return a, fmt.Errorf("unknown flag %s", arg)
			}
			a.pos = append(a.pos, arg)
		}
	}
	return a, nil
}

func (a *harnessArgs) setValue(name, val string) error {
	val = strings.TrimSpace(val)
	switch name {
	case "--session":
		a.session = val
	case "--run":
		a.run = val
	case "--note":
		a.note = val
	case "--remote":
		a.remote = val
	case "--lines":
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return errors.New("--lines must be a positive number")
		}
		a.lines = n
	}
	return nil
}

func (a *harnessArgs) setBool(arg string) bool {
	switch arg {
	case "--json":
		a.json = true
	case "--dirty":
		a.dirty = true
	case "--force":
		a.force = true
	case "--wait":
		a.wait = true
	default:
		return false
	}
	return true
}

// target is the run or session params every run call carries.
func (a harnessArgs) target() map[string]any {
	p := map[string]any{}
	if a.run != "" {
		p["run"] = a.run
	} else if a.session != "" {
		p["session"] = a.session
	}
	return p
}

type harnessSub struct {
	// minPos and maxPos bound the positional arguments.
	minPos, maxPos int
	// needsRun accepts --run in place of a session.
	needsRun bool
	run      func(h harnessIO, a harnessArgs) error
}

var harnessSubs = map[string]harnessSub{
	"list":    {0, 0, false, harnessList},
	"up":      {1, 1, false, harnessUp},
	"status":  {0, 0, true, harnessStatus},
	"check":   {0, 1, true, harnessCheck},
	"logs":    {1, 1, true, harnessLogs},
	"result":  {0, 1, true, harnessResult},
	"verdict": {2, 2, true, harnessVerdict},
	"down":    {0, 0, true, harnessDown},
}

// harnessMain runs a host subcommand against serve.
func harnessMain(args []string, h harnessIO) error {
	if len(args) == 0 {
		return harnessUsage(h.errOut)
	}
	sub, ok := harnessSubs[args[0]]
	if !ok {
		return harnessUsage(h.errOut)
	}
	a, err := parseHarnessArgs(args[1:])
	if err != nil || a.remote != "" || len(a.pos) < sub.minPos || len(a.pos) > sub.maxPos {
		if err != nil {
			_, _ = fmt.Fprintln(h.errOut, err)
		}
		return harnessUsage(h.errOut)
	}
	if args[0] == "result" && len(a.pos) == 1 {
		a.run = a.pos[0]
	}
	if a.session == "" {
		a.session = strings.TrimSpace(h.getenv("AIMAN_ID"))
	}
	if a.session == "" && (!sub.needsRun || a.run == "") {
		_, _ = fmt.Fprintln(h.errOut, "name the session with --session NAME (or set AIMAN_ID), or the run with --run ID")
		return harnessUsage(h.errOut)
	}
	return sub.run(h, a)
}

// harnessCallOut calls serve and turns a refusal into printed output and a
// non-zero exit.
func harnessCallOut(h harnessIO, a harnessArgs, method string, params map[string]any) (json.RawMessage, error) {
	raw, err := h.call(method, params)
	if err == nil {
		return raw, nil
	}
	var re *harnessRPCError
	if !errors.As(err, &re) {
		return nil, err
	}
	if a.json {
		e := map[string]any{"code": re.Code, "message": re.Message}
		if len(re.Data) > 0 {
			e["data"] = re.Data
		}
		_ = json.NewEncoder(h.errOut).Encode(map[string]any{"error": e})
	} else {
		_, _ = fmt.Fprintf(h.errOut, "harness: %s: %s\n", re.Code, re.Message)
		printRefusalData(h.errOut, re.Data)
	}
	return nil, cliExit{code: 1}
}

// printRefusalData prints the lists a refusal carries (dirty paths, file
// errors) and any other field as key: value.
func printRefusalData(w io.Writer, data json.RawMessage) {
	if len(data) == 0 {
		return
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return
	}
	for k, v := range m {
		var list []string
		if json.Unmarshal(v, &list) == nil {
			_, _ = fmt.Fprintf(w, "  %s:\n", k)
			for _, s := range list {
				_, _ = fmt.Fprintf(w, "    %s\n", s)
			}
			continue
		}
		_, _ = fmt.Fprintf(w, "  %s: %s\n", k, strings.Trim(string(v), `"`))
	}
}

func printHarnessJSON(w io.Writer, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

type harnessRunReply struct {
	Run harness.Run `json:"run"`
}

func decodeRun(raw json.RawMessage) (harness.Run, error) {
	var r harnessRunReply
	err := json.Unmarshal(raw, &r)
	return r.Run, err
}

// errHarnessRunFailed makes the CLI exit non-zero after printing a failed run.
var errHarnessRunFailed = errors.New("the harness run failed")

func harnessUp(h harnessIO, a harnessArgs) error {
	params := map[string]any{"session": a.session, "scenario": a.pos[0], "dirty": a.dirty, "force": a.force, "wait": a.wait}
	raw, err := harnessCallOut(h, a, "harness.up", params)
	if err != nil {
		return err
	}
	if a.wait {
		if raw, err = harnessWaitStarting(h, a, raw); err != nil {
			return err
		}
	}
	return harnessPrintRun(h, a, raw)
}

// harnessWaitStarting polls harness.status while the run is starting.
func harnessWaitStarting(h harnessIO, a harnessArgs, raw json.RawMessage) (json.RawMessage, error) {
	run, err := decodeRun(raw)
	if err != nil {
		return nil, err
	}
	for waited := time.Duration(0); run.State == harness.StateStarting && waited < harnessUpWaitLimit; waited += harnessPollEvery {
		h.sleep(harnessPollEvery)
		if raw, err = harnessCallOut(h, a, "harness.status", map[string]any{"run": run.ID}); err != nil {
			return nil, err
		}
		if run, err = decodeRun(raw); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func harnessPrintRun(h harnessIO, a harnessArgs, raw json.RawMessage) error {
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	run, err := decodeRun(raw)
	if err != nil {
		return err
	}
	writeHarnessRun(h.out, run)
	if run.State == harness.StateFailed {
		return errHarnessRunFailed
	}
	return nil
}

func harnessStatus(h harnessIO, a harnessArgs) error {
	raw, err := harnessCallOut(h, a, "harness.status", a.target())
	if err != nil {
		return err
	}
	return harnessPrintRun(h, a, raw)
}

func harnessDown(h harnessIO, a harnessArgs) error {
	raw, err := harnessCallOut(h, a, "harness.down", a.target())
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	run, err := decodeRun(raw)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(h.out, "run %s %s\n", run.ID, run.State)
	return nil
}

func harnessResult(h harnessIO, a harnessArgs) error {
	raw, err := harnessCallOut(h, a, "harness.result", a.target())
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	run, err := decodeRun(raw)
	if err != nil {
		return err
	}
	writeHarnessRun(h.out, run)
	writeHarnessVerdicts(h.out, run.Verdicts)
	return nil
}

// errHarnessCheckFailed makes the CLI exit non-zero after a failed check.
var errHarnessCheckFailed = errors.New("a harness check failed")

func harnessCheck(h harnessIO, a harnessArgs) error {
	params := a.target()
	if len(a.pos) == 1 {
		params["name"] = a.pos[0]
	}
	raw, err := harnessCallOut(h, a, "harness.check", params)
	if err != nil {
		return err
	}
	var res struct {
		Checks []harness.CheckRun `json:"checks"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	if a.json {
		if err := printHarnessJSON(h.out, raw); err != nil {
			return err
		}
	} else {
		writeHarnessChecks(h.out, res.Checks)
	}
	for _, c := range res.Checks {
		if c.Exit != nil && *c.Exit != 0 {
			return errHarnessCheckFailed
		}
	}
	return nil
}

func harnessLogs(h harnessIO, a harnessArgs) error {
	params := a.target()
	params["service"] = a.pos[0]
	if a.lines > 0 {
		params["lines"] = a.lines
	}
	raw, err := harnessCallOut(h, a, "harness.logs", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	for _, l := range res.Lines {
		_, _ = fmt.Fprintln(h.out, l)
	}
	return nil
}

func harnessList(h harnessIO, a harnessArgs) error {
	raw, err := harnessCallOut(h, a, "harness.list", map[string]any{"session": a.session})
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	var res struct {
		Source    string             `json:"source"`
		SHA       string             `json:"sha"`
		Scenarios []harness.Scenario `json:"scenarios"`
		Runs      []harness.Run      `json:"runs"`
		FileError *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"file_error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	if res.FileError != nil {
		_, _ = fmt.Fprintf(h.out, "harness file: %s: %s\n", res.FileError.Code, res.FileError.Message)
	} else {
		_, _ = fmt.Fprintf(h.out, "scenarios (%s at %s):\n", res.Source, short(res.SHA))
	}
	for _, sc := range res.Scenarios {
		_, _ = fmt.Fprintf(h.out, "  %-24s %s  (%d steps, %d checks)\n", sc.ID, sc.Title, len(sc.Steps), len(sc.Checks))
	}
	if len(res.Runs) > 0 {
		_, _ = fmt.Fprintln(h.out, "runs:")
	}
	for _, r := range res.Runs {
		_, _ = fmt.Fprintf(h.out, "  %s  %-24s %-8s %s  %s\n", r.ID, r.Scenario, r.State, short(r.SHA), r.StartedAt.Local().Format(time.DateTime))
	}
	return nil
}

// parseVerdict reads `<step|all> pass|fail|skip`; all is step 0.
func parseVerdict(stepArg, result string) (int, string, error) {
	step := 0
	if stepArg != "all" {
		n, err := strconv.Atoi(stepArg)
		if err != nil || n < 1 {
			return 0, "", fmt.Errorf("step must be a number from 1, or all: %q", stepArg)
		}
		step = n
	}
	switch result {
	case harness.ResultPass, harness.ResultFail, harness.ResultSkip:
		return step, result, nil
	}
	return 0, "", fmt.Errorf("result must be pass, fail or skip: %q", result)
}

func harnessVerdict(h harnessIO, a harnessArgs) error {
	step, result, err := parseVerdict(a.pos[0], a.pos[1])
	if err != nil {
		_, _ = fmt.Fprintln(h.errOut, err)
		return harnessUsage(h.errOut)
	}
	params := a.target()
	params["result"] = result
	if a.note != "" {
		params["note"] = a.note
	}
	if step > 0 {
		params["step"] = step
	}
	raw, err := harnessCallOut(h, a, "harness.verdict", params)
	if err != nil {
		return err
	}
	if a.json {
		return printHarnessJSON(h.out, raw)
	}
	return writeVerdictReply(h.out, raw)
}

func writeVerdictReply(w io.Writer, raw json.RawMessage) error {
	var res struct {
		Verdict harness.Verdict `json:"verdict"`
		Todo    string          `json:"todo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "recorded %s %s on run %s", verdictStep(res.Verdict.Step), res.Verdict.Result, res.Verdict.RunID)
	if res.Todo != "" {
		_, _ = fmt.Fprintf(w, "; to-do %s", res.Todo)
	}
	_, _ = fmt.Fprintln(w)
	return nil
}

func verdictStep(step int) string {
	if step == 0 {
		return "scenario"
	}
	return "step " + strconv.Itoa(step)
}

func writeHarnessRun(w io.Writer, r harness.Run) {
	tree := short(r.SHA)
	if r.Dirty {
		tree += " (dirty, diff " + short(r.DiffSHA256) + ")"
	}
	_, _ = fmt.Fprintf(w, "run %s  %s  %s  sha %s  source %s\n", r.ID, r.Scenario, r.State, tree, r.Source)
	if r.State == harness.StateFailed {
		_, _ = fmt.Fprintf(w, "  failed at %s %s: %s\n", r.FailedStep, r.FailedService, r.Error)
	}
	if r.Result != "" {
		_, _ = fmt.Fprintf(w, "  verdict: %s\n", r.Result)
	}
	for _, p := range r.Ports {
		_, _ = fmt.Fprintf(w, "  port %-12s host 127.0.0.1:%d (%s)\n", p.Name, p.HostPort, p.Service)
	}
	writeHarnessSteps(w, r.Steps, nil)
	writeHarnessChecks(w, r.Checks)
}

// writeHarnessSteps prints the human steps; urls replaces {{name}} with a
// laptop URL when set.
func writeHarnessSteps(w io.Writer, steps []harness.StepView, urls map[string]string) {
	for _, s := range steps {
		mark := ""
		if s.Result != "" {
			mark = " [" + s.Result + "]"
		}
		_, _ = fmt.Fprintf(w, "  %d.%s %s\n", s.N, mark, expandStepURLs(s.Do, urls))
		if s.Expect != "" {
			_, _ = fmt.Fprintf(w, "     expect: %s\n", expandStepURLs(s.Expect, urls))
		}
		if s.Note != "" {
			_, _ = fmt.Fprintf(w, "     note: %s\n", s.Note)
		}
	}
}

func expandStepURLs(s string, urls map[string]string) string {
	for name, u := range urls {
		s = strings.ReplaceAll(s, "{{"+name+"}}", u)
	}
	return s
}

func writeHarnessChecks(w io.Writer, checks []harness.CheckRun) {
	for _, c := range checks {
		status := "not run"
		if c.Exit != nil {
			status = "exit " + strconv.Itoa(*c.Exit)
		}
		_, _ = fmt.Fprintf(w, "  check %-20s %s\n", c.Name, status)
		if c.Output != "" && c.Exit != nil && *c.Exit != 0 {
			for _, l := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
				_, _ = fmt.Fprintf(w, "    %s\n", l)
			}
		}
	}
}

func writeHarnessVerdicts(w io.Writer, vs []harness.Verdict) {
	for _, v := range vs {
		_, _ = fmt.Fprintf(w, "  verdict %s %s", verdictStep(v.Step), v.Result)
		if v.Note != "" {
			_, _ = fmt.Fprintf(w, ": %s", v.Note)
		}
		_, _ = fmt.Fprintln(w)
	}
}
