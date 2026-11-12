package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func init() {
	registerSlack("send", slackCommand{run: runSlackSend, write: slackAlwaysWrite})
	registerSlack("react", slackCommand{run: runSlackReact, write: slackAlwaysWrite})
	registerSlack("wait", slackCommand{run: runSlackWait})
}

func runSlackSend(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
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

func runSlackReact(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
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

func runSlackWait(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
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

// slackText reads the message from --text-file, --text, or stdin for --text -.
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
