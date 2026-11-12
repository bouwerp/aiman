package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/slack"
	"golang.org/x/term"
)

// slackCommand is one aiman slack subcommand. write reports whether a call
// with these args (the command name excluded) changes Slack; slack.writes
// off refuses such a call before any API call. A nil write is a read.
type slackCommand struct {
	run   func(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error
	write func(args []string) bool
}

// slackAlwaysWrite marks a command that changes Slack whatever its args.
func slackAlwaysWrite([]string) bool { return true }

// slackNeverWrite marks a read-only command; same as a nil write.
func slackNeverWrite([]string) bool { return false }

// slackWriteSubcommands marks a command whose first argument picks a
// subcommand: it is a write when that argument is one of names. An empty
// name stands for no subcommand (args empty or starting with a flag).
func slackWriteSubcommands(names ...string) func([]string) bool {
	return func(args []string) bool {
		sub := ""
		if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
			sub = args[0]
		}
		return slices.Contains(names, sub)
	}
}

// isWrite reports whether this call changes Slack.
func (c slackCommand) isWrite(args []string) bool {
	return c.write != nil && c.write(args)
}

// slackCommands is filled by registerSlack from each slack_*_cmd.go init.
var slackCommands = map[string]slackCommand{}

// registerSlack adds a subcommand. A second registration of a name panics,
// so two files cannot silently claim the same command.
func registerSlack(name string, cmd slackCommand) {
	if _, dup := slackCommands[name]; dup {
		panic("aiman slack: command registered twice: " + name)
	}
	slackCommands[name] = cmd
}

func init() {
	registerSlack("manifest", slackCommand{run: func(_ context.Context, _ []string, _ io.Reader, out, _ io.Writer) error {
		_, err := io.WriteString(out, slack.Manifest)
		return err
	}})
	registerSlack("auth", slackCommand{run: runSlackAuth})
}

func runSlack(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return slackUsage(errOut)
	}
	if slackArgvToken(args) {
		return slackFail(errOut, "slack_token_argv", "pass the client id on stdin: aiman slack auth app")
	}
	cmd, ok := slackCommands[args[0]]
	if !ok {
		return slackUsage(errOut)
	}
	if cmd.isWrite(args[1:]) && !slackWritesOn() {
		return slackFail(errOut, "slack_writes_off", "slack.writes is off in ~/.aiman/config.yaml; aiman slack "+args[0]+" changes Slack")
	}
	return cmd.run(ctx, args[1:], in, out, errOut)
}

// slackWritesOn reads slack.writes; a missing or unreadable config is on.
func slackWritesOn() bool {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return true
	}
	return cfg.Slack.WritesOn()
}

func slackUsage(errOut io.Writer) error {
	names := make([]string, 0, len(slackCommands))
	for name := range slackCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintf(errOut, "Usage: aiman slack <%s> …\n", strings.Join(names, "|"))
	fmt.Fprintf(errOut, "Are you an AI? Run: aiman --skill\n")
	return errUsage
}

// slackFlags parses --key value pairs; a key in bools never takes a value.
func slackFlags(args []string, bools ...string) (map[string]string, []string) {
	flags := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		key, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch {
		case hasVal:
			flags[key] = val
		case slices.Contains(bools, key):
			flags[key] = "1"
		case i+1 < len(args) && !strings.HasPrefix(args[i+1], "--"):
			flags[key] = args[i+1]
			i++
		default:
			flags[key] = "1"
		}
	}
	return flags, rest
}

func slackArgvToken(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--token" || strings.HasPrefix(a, "--token="):
			return true
		case a == "--client-id" || strings.HasPrefix(a, "--client-id="):
			return true
		case a == "--client-secret" || strings.HasPrefix(a, "--client-secret="):
			return true
		case a == "--secret" || strings.HasPrefix(a, "--secret="):
			return true
		}
	}
	return false
}

func slackFail(errOut io.Writer, code, msg string) error {
	_ = json.NewEncoder(errOut).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
	return errUsage
}

// slackAPI reports a Slack or transport failure. A wait deadline is its own
// code so an agent can stop instead of treating "nobody replied" as a broken API.
func slackAPI(errOut io.Writer, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return slackFail(errOut, "slack_timeout", err.Error())
	}
	var gap slack.SettingsGap
	if errors.As(err, &gap) {
		return slackFail(errOut, "slack_app_settings", err.Error())
	}
	return slackFail(errOut, "slack_api", err.Error())
}

func slackTokenPath() (string, error) {
	dir, err := config.GetDir()
	if err != nil {
		return "", err
	}
	return slack.TokenPath(dir), nil
}

func slackClient() (*slack.Client, error) {
	path, err := slackTokenPath()
	if err != nil {
		return nil, err
	}
	tok, err := slack.ReadToken(path)
	if err != nil {
		return nil, err
	}
	return &slack.Client{Token: tok}, nil
}

func slackWrite(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runSlackAuth(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return slackUsage(errOut)
	}
	switch args[0] {
	case "app":
		return runSlackAuthApp(in, out, errOut)
	case "login":
		return runSlackAuthLogin(ctx, args[1:], out, errOut)
	case "check":
		return runSlackAuthCheck(ctx, args[1:], in, out, errOut)
	case "status":
		c, err := slackClient()
		if err != nil {
			return slackFail(errOut, "slack_unconfigured", err.Error())
		}
		auth, err := c.AuthTest(ctx)
		if err != nil {
			return slackAPI(errOut, err)
		}
		return slackWrite(out, auth)
	default:
		return slackUsage(errOut)
	}
}

// runSlackAuthApp reads the client id from the first line of stdin, so a
// paste followed by Enter is enough; a terminal gets a prompt first.
func runSlackAuthApp(in io.Reader, out, errOut io.Writer) error {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		_, _ = fmt.Fprint(errOut, "Paste the Slack app's Client ID and press Enter: ")
	}
	body, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	path, err := slackTokenPath()
	if err != nil {
		return err
	}
	if err := slack.SaveClientID(path, body); err != nil {
		return slackFail(errOut, "slack_token", err.Error())
	}
	return slackWrite(out, map[string]any{"ok": true})
}

func runSlackAuthCheck(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	if flags["app"] == "" {
		return slackFail(errOut, "slack_params", "check requires --app A012")
	}
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	err = slack.ExportApp(ctx, string(body), flags["app"])
	if err == nil {
		return slackWrite(out, map[string]any{"ok": true})
	}
	var gap slack.SettingsGap
	if errors.As(err, &gap) {
		return slackFail(errOut, "slack_app_settings", err.Error())
	}
	if errors.Is(err, slack.ErrConfigToken) || errors.Is(err, slack.ErrAppID) {
		return slackFail(errOut, "slack_params", err.Error())
	}
	return slackAPI(errOut, err)
}

func runSlackAuthLogin(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	timeout := 5 * time.Minute
	if flags["timeout"] != "" {
		d, err := time.ParseDuration(flags["timeout"])
		if err != nil {
			return slackFail(errOut, "slack_params", "timeout must be a duration like 5m")
		}
		timeout = d
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	path, err := slackTokenPath()
	if err != nil {
		return err
	}
	stored, err := slack.Load(path)
	if err != nil && !os.IsNotExist(err) {
		return slackFail(errOut, "slack_unconfigured", err.Error())
	}
	if strings.TrimSpace(stored.ClientID) == "" {
		return slackFail(errOut, "slack_unconfigured", "slack client id is not set")
	}
	creds, err := slack.CompleteLogin(ctx, stored.ClientID, func(rawURL string) error {
		fmt.Fprintf(errOut, "Open this URL and approve Aiman:\n%s\n", rawURL)
		if err := slack.BrowserOpen(rawURL); err != nil {
			fmt.Fprintln(errOut, "Could not open a browser. Open the URL yourself.")
		}
		return nil
	})
	if err != nil {
		return slackAPI(errOut, err)
	}
	if err := slack.Store(path, creds); err != nil {
		return slackFail(errOut, "slack_token", err.Error())
	}
	return slackWrite(out, map[string]any{"ok": true, "user_id": creds.UserID, "team": creds.Team})
}

// slackChannel builds a client and resolves --channel to an id.
func slackChannel(ctx context.Context, flags map[string]string, errOut io.Writer) (*slack.Client, string, error) {
	if flags["channel"] == "" {
		return nil, "", slackFail(errOut, "slack_params", "requires --channel")
	}
	c, err := slackClient()
	if err != nil {
		return nil, "", slackFail(errOut, "slack_unconfigured", err.Error())
	}
	id, err := c.ResolveChannel(ctx, flags["channel"])
	if err != nil {
		return nil, "", slackAPI(errOut, err)
	}
	return c, id, nil
}

// slackClientOrFail builds a client or prints slack_unconfigured.
func slackClientOrFail(errOut io.Writer) (*slack.Client, error) {
	c, err := slackClient()
	if err != nil {
		return nil, slackFail(errOut, "slack_unconfigured", err.Error())
	}
	return c, nil
}
