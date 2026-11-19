package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bouwerp/aiman/internal/console"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// harnessConsoleCallTimeout bounds one RPC the console sends to serve.
const harnessConsoleCallTimeout = 45 * time.Second

// consoleRPC is what the console asks the host's serve. Calls go over the
// laptop's RPC path to the remote, so serve sees the operator.
type consoleRPC interface {
	Status(ctx context.Context, params map[string]any) (json.RawMessage, error)
	Verdict(ctx context.Context, params map[string]any) (json.RawMessage, error)
}

// remoteHarnessRPC calls serve on a remote through usecase.ServeRPC.
type remoteHarnessRPC struct {
	remote usecase.TerminalExecutor
}

func (r remoteHarnessRPC) Status(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, harnessConsoleCallTimeout)
	defer cancel()
	return usecase.ServeRPC(ctx, r.remote, "harness.status", params)
}

// Call sends any harness method, waiting as long as serve's own limit for it.
func (r remoteHarnessRPC) Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, harnessConsoleTimeout(method))
	defer cancel()
	return usecase.ServeRPC(ctx, r.remote, method, params)
}

// harnessConsoleTimeout is serve's limit for the long harness calls plus a
// margin, else harnessConsoleCallTimeout.
func harnessConsoleTimeout(method string) time.Duration {
	switch method {
	case "harness.up":
		return server.HarnessUpTimeout + 30*time.Second
	case "harness.check":
		return server.HarnessCheckTimeout + 30*time.Second
	case "harness.down":
		return server.HarnessDownTimeout + 30*time.Second
	case "harness.debug":
		return server.HarnessDebugTimeout + 15*time.Second
	case "harness.aws_write":
		return server.HarnessAWSResetTimeout + 30*time.Second
	}
	return harnessConsoleCallTimeout
}

func (r remoteHarnessRPC) Verdict(ctx context.Context, params map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, harnessConsoleCallTimeout)
	defer cancel()
	return usecase.ServeRPC(ctx, r.remote, "harness.verdict", params)
}

type consoleDeps struct {
	rpc       consoleRPC
	forwarder usecase.HarnessForwarder
	in        io.Reader
	out       io.Writer
	sleep     func(time.Duration)
}

const harnessConsoleUsage = "Usage: aiman harness console [--remote NAME] [--session NAME] [--run ID] [--text] [--no-browser]\n" +
	"Runs on the laptop: forwards the run's ports over ssh and calls serve as the operator.\n" +
	"By default it serves a web console on 127.0.0.1 and opens it in the browser; --text asks for verdicts in the terminal.\n"

// runHarnessConsole is `aiman harness console` on the laptop.
func runHarnessConsole(args []string) error {
	a, err := parseHarnessArgs(args)
	if err != nil || len(a.pos) != 0 || a.json || a.dirty || a.force || a.wait || a.note != "" || a.lines != 0 ||
		(a.session == "" && a.run == "") {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Fprint(os.Stderr, harnessConsoleUsage)
		return errUsage
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("read the laptop config: %w", err)
	}
	remote, err := pickConsoleRemote(cfg.Remotes, cfg.ActiveRemote, a.remote)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	forwarder := usecase.HarnessForwarder{Target: ssh.Target(remote.Host, remote.User)}
	if !a.text {
		return harnessWebConsole(ctx, a, webConsoleDeps{
			rpc:       remoteHarnessRPC{remote: mgr},
			forwarder: console.SSHForwarder{F: forwarder},
			listen:    func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
			open:      openBrowser,
			out:       os.Stdout,
			log:       os.Stderr,
		})
	}
	return harnessConsole(ctx, a, consoleDeps{
		rpc:       remoteHarnessRPC{remote: mgr},
		forwarder: forwarder,
		in:        os.Stdin,
		out:       os.Stdout,
		sleep:     time.Sleep,
	})
}

// pickConsoleRemote takes --remote (a name ignoring case, else a unique
// user@host; see config.FindRemote), else the active remote, else the only
// one.
func pickConsoleRemote(remotes []config.Remote, active, name string) (config.Remote, error) {
	if name == "" {
		name = active
	}
	if name == "" {
		if len(remotes) == 1 {
			return remotes[0], nil
		}
		return config.Remote{}, fmt.Errorf("name the remote with --remote NAME; remotes: %s (aiman remotes lists them)", config.RemoteNames(remotes))
	}
	return config.FindRemote(remotes, name)
}

// errConsoleStopped ends the console after a forward died.
var errConsoleStopped = errors.New("a forward ended; the console stopped")

// harnessConsole finds the run, forwards its ports, prints URLs and steps,
// and takes verdicts until q, end of input or ctrl+c. The forwards end with
// it on every path.
func harnessConsole(ctx context.Context, a harnessArgs, d consoleDeps) error {
	run, err := consoleReadyRun(ctx, a, d)
	if err != nil {
		return err
	}
	ports := make([]usecase.HarnessPort, 0, len(run.Ports))
	for _, p := range run.Ports {
		ports = append(ports, usecase.HarnessPort{Name: p.Name, HostPort: p.HostPort})
	}
	fw, err := d.forwarder.Open(ctx, ports)
	if err != nil {
		return err
	}
	defer fw.Close()
	urls := map[string]string{}
	_, _ = fmt.Fprintf(d.out, "run %s  %s  sha %s\n", run.ID, run.Scenario, short(run.SHA))
	for _, f := range fw.Forwards() {
		urls[f.Name] = f.URL()
		_, _ = fmt.Fprintf(d.out, "  %-12s %s  (host 127.0.0.1:%d)\n", f.Name, f.URL(), f.HostPort)
	}
	_, _ = fmt.Fprintln(d.out, "steps:")
	writeHarnessSteps(d.out, run.Steps, urls)
	c := &consoleSession{ctx: ctx, d: d, run: run, lines: readLines(d.in), died: fw.Died()}
	return c.loop()
}

// consoleReadyRun reads the run and waits while it starts. A run that is not
// ready cannot be forwarded.
func consoleReadyRun(ctx context.Context, a harnessArgs, d consoleDeps) (harness.Run, error) {
	run, err := consoleStatus(ctx, d, a.target())
	if err != nil {
		return run, err
	}
	for waited := time.Duration(0); run.State == harness.StateStarting && waited < harnessUpWaitLimit; waited += harnessPollEvery {
		if waited == 0 {
			_, _ = fmt.Fprintf(d.out, "run %s is starting; waiting\n", run.ID)
		}
		d.sleep(harnessPollEvery)
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		if run, err = consoleStatus(ctx, d, map[string]any{"run": run.ID}); err != nil {
			return run, err
		}
	}
	if run.State != harness.StateReady {
		writeHarnessRun(d.out, run)
		return run, fmt.Errorf("run %s is %s; start one with aiman harness up <scenario> on the host", run.ID, run.State)
	}
	return run, nil
}

func consoleStatus(ctx context.Context, d consoleDeps, params map[string]any) (harness.Run, error) {
	raw, err := d.rpc.Status(ctx, params)
	if err != nil {
		return harness.Run{}, fmt.Errorf("harness.status: %w", err)
	}
	return decodeRun(raw)
}

// readLines feeds the input's lines to a channel and closes it at the end.
// The reader goroutine lives as long as the input does.
func readLines(in io.Reader) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			ch <- sc.Text()
		}
	}()
	return ch
}

type consoleSession struct {
	ctx   context.Context
	d     consoleDeps
	run   harness.Run
	lines <-chan string
	died  <-chan usecase.HarnessForward
}

// errConsoleEOF is the end of the tester's input.
var errConsoleEOF = errors.New("end of input")

// ask prints prompt and returns the next line.
func (c *consoleSession) ask(prompt string) (string, error) {
	_, _ = fmt.Fprint(c.d.out, prompt)
	select {
	case <-c.ctx.Done():
		_, _ = fmt.Fprintln(c.d.out)
		return "", c.ctx.Err()
	case f := <-c.died:
		if c.ctx.Err() != nil { // ctrl+c reaches the ssh children too
			return "", c.ctx.Err()
		}
		_, _ = fmt.Fprintf(c.d.out, "\nforward %s (laptop port %d) ended\n", f.Name, f.LocalPort)
		return "", errConsoleStopped
	case l, ok := <-c.lines:
		if !ok {
			return "", errConsoleEOF
		}
		return strings.TrimSpace(l), nil
	}
}

// loop asks for a verdict on each step, then on the scenario, then holds the
// forwards until q. ctrl+c and end of input stop it without an error.
func (c *consoleSession) loop() error {
	err := c.verdicts()
	if err == nil {
		_, _ = fmt.Fprintln(c.d.out, "Forwards stay open.")
		err = c.hold()
	}
	_, _ = fmt.Fprintln(c.d.out, "stopping forwards")
	if errors.Is(err, errConsoleEOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (c *consoleSession) hold() error {
	for {
		l, err := c.ask("q to stop > ")
		if err != nil {
			return err
		}
		if l == "q" {
			return nil
		}
	}
}

func (c *consoleSession) verdicts() error {
	for _, s := range c.run.Steps {
		quit, err := c.verdictFor(s.N, fmt.Sprintf("step %d [p]ass [f]ail [s]kip, enter to leave, q to stop > ", s.N))
		if err != nil || quit {
			return err
		}
	}
	quit, err := c.verdictFor(0, "scenario [p]ass [f]ail [s]kip, enter to leave, q to stop > ")
	if quit {
		return errConsoleEOF
	}
	return err
}

// verdictFor asks for one verdict and sends it; step 0 is the scenario. It
// reports quit when the tester typed q.
func (c *consoleSession) verdictFor(step int, prompt string) (bool, error) {
	for {
		l, err := c.ask(prompt)
		if err != nil {
			return false, err
		}
		result, ok := consoleResults[strings.ToLower(l)]
		switch {
		case l == "":
			return false, nil
		case l == "q":
			return true, nil
		case !ok:
			_, _ = fmt.Fprintln(c.d.out, "answer p, f, s, enter or q")
			continue
		}
		note, err := c.ask("note (enter for none) > ")
		if err != nil {
			return false, err
		}
		c.send(step, result, note)
		return false, nil
	}
}

var consoleResults = map[string]string{
	"p": harness.ResultPass, "pass": harness.ResultPass,
	"f": harness.ResultFail, "fail": harness.ResultFail,
	"s": harness.ResultSkip, "skip": harness.ResultSkip,
}

// send records a verdict; a refusal is printed and the console goes on.
func (c *consoleSession) send(step int, result, note string) {
	params := map[string]any{"run": c.run.ID, "result": result}
	if step > 0 {
		params["step"] = step
	}
	if note != "" {
		params["note"] = note
	}
	raw, err := c.d.rpc.Verdict(c.ctx, params)
	if err != nil {
		_, _ = fmt.Fprintf(c.d.out, "verdict not recorded: %v\n", err)
		return
	}
	if err := writeVerdictReply(c.d.out, raw); err != nil {
		_, _ = fmt.Fprintf(c.d.out, "verdict recorded; unreadable answer: %v\n", err)
	}
}
