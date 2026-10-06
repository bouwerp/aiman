package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/slack"
)

func runSlack(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return slackUsage(errOut)
	}
	if slackArgvToken(args) {
		return slackFail(errOut, "slack_token_argv", "pass the client id on stdin: aiman slack auth app")
	}
	switch args[0] {
	case "manifest":
		_, err := io.WriteString(out, slack.Manifest)
		return err
	case "auth":
		return runSlackAuth(ctx, args[1:], in, out, errOut)
	case "channels":
		return runSlackChannels(ctx, out, errOut)
	case "history":
		return runSlackHistory(ctx, args[1:], out, errOut)
	case "thread":
		return runSlackThread(ctx, args[1:], out, errOut)
	case "send":
		return runSlackSend(ctx, args[1:], in, out, errOut)
	case "react":
		return runSlackReact(ctx, args[1:], out, errOut)
	case "user":
		return runSlackUser(ctx, args[1:], out, errOut)
	case "wait":
		return runSlackWait(ctx, args[1:], out, errOut)
	default:
		return slackUsage(errOut)
	}
}

func slackUsage(errOut io.Writer) error {
	fmt.Fprintf(errOut, "Usage: aiman slack <manifest|auth|channels|history|thread|send|react|user|wait> …\n")
	fmt.Fprintf(errOut, "Are you an AI? Run: aiman --skill\n")
	return errUsage
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
	return slackFail(errOut, "slack_api", err.Error())
}

func slackTokenPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("AIMAN_SLACK_TOKEN_FILE")); p != "" {
		return p, nil
	}
	dir, err := config.GetDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "slack-oauth.json"), nil
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

func runSlackAuthApp(in io.Reader, out, errOut io.Writer) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	path, err := slackTokenPath()
	if err != nil {
		return err
	}
	if err := slack.SaveClientID(path, string(body)); err != nil {
		return slackFail(errOut, "slack_token", err.Error())
	}
	return slackWrite(out, map[string]any{"ok": true})
}

func runSlackAuthLogin(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := takeFlags(args)
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

func runSlackChannels(ctx context.Context, out, errOut io.Writer) error {
	c, err := slackClient()
	if err != nil {
		return slackFail(errOut, "slack_unconfigured", err.Error())
	}
	chs, err := c.Channels(ctx)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, chs)
}

func runSlackHistory(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := takeFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	msgs, err := c.History(ctx, channel, atoi(flags["limit"]))
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, msgs)
}

func runSlackThread(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := takeFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	ts := flags["ts"]
	if ts == "" {
		return slackFail(errOut, "slack_params", "thread requires --ts")
	}
	msgs, err := c.Replies(ctx, channel, ts, atoi(flags["limit"]))
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, msgs)
}

func runSlackSend(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags, _ := takeFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	text, err := slackText(flags, in)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	posted, err := c.Post(ctx, slack.Post{Channel: channel, Text: text, ThreadTS: flags["thread"]})
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, posted)
}

func runSlackReact(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := takeFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	if flags["ts"] == "" || flags["emoji"] == "" {
		return slackFail(errOut, "slack_params", "react requires --ts and --emoji")
	}
	if err := c.React(ctx, channel, flags["ts"], flags["emoji"]); err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"ok": true})
}

func runSlackUser(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "--") {
		return slackFail(errOut, "slack_params", "user requires a user id")
	}
	c, err := slackClient()
	if err != nil {
		return slackFail(errOut, "slack_unconfigured", err.Error())
	}
	user, err := c.User(ctx, args[0])
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, user)
}

func runSlackWait(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := takeFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	thread := flags["thread"]
	if thread == "" {
		return slackFail(errOut, "slack_params", "wait requires --thread")
	}
	if flags["timeout"] != "" {
		d, err := time.ParseDuration(flags["timeout"])
		if err != nil {
			return slackFail(errOut, "slack_params", "timeout must be a duration like 2m")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	auth, err := c.AuthTest(ctx)
	if err != nil {
		return slackAPI(errOut, err)
	}
	msg, err := c.Wait(ctx, channel, thread, flags["after"], auth.UserID)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, msg)
}

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

func slackText(flags map[string]string, in io.Reader) (string, error) {
	if path := flags["text-file"]; path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\n"), nil
	}
	text := flags["text"]
	if text == "-" {
		b, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		text = string(b)
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("send requires --text or --text-file")
	}
	return text, nil
}
