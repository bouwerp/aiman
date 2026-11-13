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
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/slack"
	"golang.org/x/term"
)

// slackCommand is one aiman slack subcommand. write reports whether a call
// with these args (the command name excluded) changes Slack; slack.writes
// off refuses such a call before any API call. A nil write is a read.
// usage is the text after "Usage: aiman slack <name> " that --help prints.
// noRaw marks a call that makes no Slack read, so --raw has nothing to show.
type slackCommand struct {
	run   func(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error
	write func(args []string) bool
	usage string
	noRaw func(args []string) bool
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
	registerSlack("manifest", slackCommand{
		run: func(_ context.Context, _ []string, _ io.Reader, out, _ io.Writer) error {
			_, err := io.WriteString(out, slack.Manifest)
			return err
		},
		usage: "(no flags)\n\nPrints the Slack app manifest to paste into Slack's App Manifest JSON editor.",
		noRaw: func([]string) bool { return true },
	})
	registerSlack("auth", slackCommand{
		run: runSlackAuth,
		usage: `<app|login|check [--app A012]|status> …

  auth app                     read the app's client id from stdin
  auth login [--timeout 5m]    approve the app in a browser and store the user token
  auth check                   check the stored user token (auth.test) and its granted scopes
  auth check --app A012        compare the app (configuration token on stdin) with the manifest
  auth status [--raw]          show who the stored token is`,
		noRaw: func(args []string) bool { return len(args) == 0 || args[0] != "status" },
	})
}

// slackLinkHelp ends the usage of a command that takes archive links.
const slackLinkHelp = "\n\n  --channel, --ts and --thread also take a message link: https://<workspace>.slack.com/archives/C…/p…"

// slackRawHelp is the --raw line of a read command's usage.
const slackRawHelp = "\n  --raw                        print each Slack response body unchanged, one per line"

func runSlack(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return slackUsage(errOut)
	}
	if slackArgvToken(args) {
		return slackFail(errOut, "slack_token_argv", "pass the client id on stdin: aiman slack auth app")
	}
	name := args[0]
	cmd, ok := slackCommands[name]
	if !ok {
		return slackUsage(errOut)
	}
	args = args[1:]
	if slackWantsHelp(args) {
		return slackCommandHelp(out, name, cmd)
	}
	args, raw := slackTakeRaw(args)
	if raw && (cmd.isWrite(args) || (cmd.noRaw != nil && cmd.noRaw(args))) {
		return slackFail(errOut, "slack_params", "--raw is only for reads; this aiman slack "+name+" call is not one")
	}
	if cmd.isWrite(args) && !slackWritesOn() {
		return slackFail(errOut, "slack_writes_off", "slack.writes is off in ~/.aiman/config.yaml; aiman slack "+name+" changes Slack")
	}
	args, err := slackLinkArgs(name, args)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if !raw {
		return cmd.run(ctx, args, in, out, errOut)
	}
	rec := &slackRawRecorder{}
	err = cmd.run(context.WithValue(ctx, slackRawKey{}, rec), args, in, io.Discard, errOut)
	if werr := rec.writeTo(out); err == nil {
		err = werr
	}
	return err
}

// slackWantsHelp reports whether args ask for help: --help or -h anywhere,
// except as the value of a flag that takes one (--text -h stays text).
func slackWantsHelp(args []string) bool {
	prev := ""
	for _, a := range args {
		if (a == "--help" || a == "-h") && !slackTakesValue(prev) {
			return true
		}
		prev = a
	}
	return false
}

// slackTakesValue reports whether arg is a --flag that consumes the next
// argument as its value.
func slackTakesValue(arg string) bool {
	if !strings.HasPrefix(arg, "--") || strings.Contains(arg, "=") || arg == "--help" {
		return false
	}
	switch strings.TrimPrefix(arg, "--") {
	case "raw", "mine", "exclude-archived", "private", "files", "channels", "users",
		"broadcast", "no-unfurl", "all", "schema", "todo":
		return false
	}
	return true
}

// slackCommandHelp prints one command's usage to out. A plain read gets the
// --raw line added; a command with writes names --raw in its own usage.
func slackCommandHelp(out io.Writer, name string, cmd slackCommand) error {
	text := "Usage: aiman slack " + name + " " + cmd.usage
	if cmd.write == nil && cmd.noRaw == nil {
		text += "\n" + slackRawHelp
	}
	_, err := fmt.Fprintln(out, text)
	return err
}

// slackTakeRaw removes --raw from args.
func slackTakeRaw(args []string) ([]string, bool) {
	raw := false
	kept := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--raw" {
			raw = true
			continue
		}
		kept = append(kept, a)
	}
	return kept, raw
}

type slackRawKey struct{}

// slackRawRecorder keeps every Slack response body of one command.
type slackRawRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *slackRawRecorder) record(_ string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, append([]byte(nil), body...))
}

// writeTo prints each body as Slack sent it, followed by a newline when it
// has none.
func (r *slackRawRecorder) writeTo(out io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.bodies {
		if _, err := out.Write(b); err != nil {
			return err
		}
		if len(b) == 0 || b[len(b)-1] != '\n' {
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

// slackLinkFlags are the flags that take a Slack archive link.
var slackLinkFlags = []string{"channel", "ts", "thread"}

// slackLinkArgs turns archive links given to --channel, --ts or --thread into
// ids. The ts of --thread, and of --ts on thread, is the link's parent
// (thread_ts wins); any other --ts is the linked message. A link fills
// --channel when it is absent; a different --channel is refused. Any other
// URL in those flags is refused.
func slackLinkArgs(name string, args []string) ([]string, error) {
	out := make([]string, 0, len(args)+2)
	channel, linkChannel := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, inline := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if !strings.HasPrefix(a, "--") || !slices.Contains(slackLinkFlags, key) {
			out = append(out, a)
			continue
		}
		if !inline {
			next := ""
			if i+1 < len(args) {
				next = args[i+1]
			}
			if next == "" || strings.HasPrefix(next, "--") {
				out = append(out, a)
				continue
			}
			val = next
			i++
		}
		if !slack.LooksLikeURL(val) {
			if key == "channel" {
				channel = val
			}
			out = append(out, "--"+key, val)
			continue
		}
		link, err := slack.ParseArchiveLink(val)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", key, err)
		}
		if linkChannel != "" && linkChannel != link.Channel {
			return nil, fmt.Errorf("--%s: link is in %s, another link is in %s", key, link.Channel, linkChannel)
		}
		linkChannel = link.Channel
		switch {
		case key == "channel":
			channel = link.Channel
			out = append(out, "--channel", link.Channel)
			continue
		case link.TS == "":
			return nil, fmt.Errorf("--%s: %s links a channel, not a message", key, val)
		case key == "thread" || name == "thread":
			out = append(out, "--"+key, link.Parent())
		default:
			out = append(out, "--"+key, link.TS)
		}
	}
	if linkChannel == "" {
		return out, nil
	}
	if channel == "" {
		return append(out, "--channel", linkChannel), nil
	}
	if channel != linkChannel {
		return nil, fmt.Errorf("--channel %s conflicts with the link's channel %s; drop --channel", channel, linkChannel)
	}
	return out, nil
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

// slackClient builds a client from the stored token. Under --raw it hands
// every response body to the command's recorder.
func slackClient(ctx context.Context) (*slack.Client, error) {
	path, err := slackTokenPath()
	if err != nil {
		return nil, err
	}
	tok, err := slack.ReadToken(path)
	if err != nil {
		return nil, err
	}
	c := &slack.Client{Token: tok}
	if rec, ok := ctx.Value(slackRawKey{}).(*slackRawRecorder); ok {
		c.Raw = rec.record
	}
	return c, nil
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
		c, err := slackClient(ctx)
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
		return runSlackAuthCheckToken(ctx, out, errOut)
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

// slackAuthCheck is what auth check without --app prints.
type slackAuthCheck struct {
	OK            bool   `json:"ok"`
	Team          string `json:"team"`
	User          string `json:"user"`
	UserID        string `json:"user_id"`
	ScopesChecked bool   `json:"scopes_checked"`
}

// runSlackAuthCheckToken checks the stored user token with one auth.test and
// compares the scopes Slack reports with slack.UserScopes. It never reads stdin.
func runSlackAuthCheckToken(ctx context.Context, out, errOut io.Writer) error {
	c, err := slackClient(ctx)
	if err != nil {
		return slackFail(errOut, "slack_unconfigured", err.Error())
	}
	auth, missing, checked, err := c.AuthCheck(ctx)
	if err != nil {
		return slackAPI(errOut, err)
	}
	if len(missing) > 0 {
		return slackFail(errOut, "slack_app_settings", "the stored token lacks user scope "+
			strings.Join(missing, ", ")+"; add them to the app (aiman slack manifest) and run aiman slack auth login again")
	}
	return slackWrite(out, slackAuthCheck{OK: true, Team: auth.Team, User: auth.User, UserID: auth.UserID, ScopesChecked: checked})
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
	c, err := slackClient(ctx)
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
func slackClientOrFail(ctx context.Context, errOut io.Writer) (*slack.Client, error) {
	c, err := slackClient(ctx)
	if err != nil {
		return nil, slackFail(errOut, "slack_unconfigured", err.Error())
	}
	return c, nil
}
