package main

import (
	"context"
	"errors"
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
	registerSlack("schedule", slackCommand{run: runSlackSchedule, write: slackWriteSubcommands("", "cancel")})
	registerSlack("create-channel", slackCommand{run: runSlackCreateChannel, write: slackAlwaysWrite})
	registerSlack("dm", slackCommand{run: runSlackDM, write: slackAlwaysWrite})
}

func runSlackSend(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args, "broadcast", "no-unfurl")
	if flags["broadcast"] != "" && flags["thread"] == "" {
		return slackFail(errOut, "slack_params", "--broadcast needs --thread")
	}
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	text, err := slackText(flags, in)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	posted, err := c.Post(ctx, slack.Post{
		Channel:   channel,
		Text:      text,
		ThreadTS:  flags["thread"],
		Broadcast: flags["broadcast"] != "",
		NoUnfurl:  flags["no-unfurl"] != "",
	})
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

func runSlackSchedule(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runSlackScheduleList(ctx, args[1:], out, errOut)
		case "cancel":
			return runSlackScheduleCancel(ctx, args[1:], out, errOut)
		}
	}
	flags, rest := slackFlags(args)
	if len(rest) > 0 {
		return slackFail(errOut, "slack_params", "schedule takes list, cancel or --channel C --at T --text t")
	}
	at, err := parseSlackAt(flags["at"], time.Now())
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	text, err := slackText(flags, in)
	if err != nil {
		return slackFail(errOut, "slack_params", err.Error())
	}
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	s, err := c.Schedule(ctx, slack.ScheduleRequest{Channel: channel, Text: text, At: at, Thread: flags["thread"]})
	if errors.Is(err, slack.ErrScheduleTime) {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, s)
}

func runSlackScheduleList(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	var (
		c       *slack.Client
		channel string
		err     error
	)
	if flags["channel"] != "" {
		c, channel, err = slackChannel(ctx, flags, errOut)
	} else {
		c, err = slackClientOrFail(errOut)
	}
	if err != nil {
		return err
	}
	items, err := c.ScheduledList(ctx, channel)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"scheduled": nonNil(items)})
}

func runSlackScheduleCancel(ctx context.Context, args []string, out, errOut io.Writer) error {
	flags, rest := slackFlags(args)
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "schedule cancel takes one id: schedule cancel <id> --channel C")
	}
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	if err := c.ScheduleCancel(ctx, channel, rest[0]); err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"ok": true})
}

// parseSlackAt reads --at as RFC3339 or +duration from now.
func parseSlackAt(v string, now time.Time) (time.Time, error) {
	if v == "" {
		return time.Time{}, errors.New("schedule requires --at <RFC3339|+duration>")
	}
	if d, ok := strings.CutPrefix(v, "+"); ok {
		dur, err := time.ParseDuration(d)
		if err != nil || dur <= 0 {
			return time.Time{}, fmt.Errorf("--at %q: want a positive duration like +2h", v)
		}
		return now.Add(dur), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("--at %q: want RFC3339 like 2026-11-13T09:00:00Z or +2h", v)
	}
	return t, nil
}

func runSlackCreateChannel(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, rest := slackFlags(args, "private")
	if len(rest) != 1 {
		return slackFail(errOut, "slack_params", "create-channel takes one name: create-channel <name> [--private] [--invite U1,U2]")
	}
	c, err := slackClientOrFail(errOut)
	if err != nil {
		return err
	}
	ch, err := c.CreateChannel(ctx, rest[0], flags["private"] != "", splitSlackIDs(flags["invite"]))
	if errors.Is(err, slack.ErrChannelName) {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, ch)
}

func runSlackDM(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	_, rest := slackFlags(args)
	var users []string
	for _, r := range rest {
		users = append(users, splitSlackIDs(r)...)
	}
	if len(users) == 0 {
		return slackFail(errOut, "slack_params", "dm takes member ids: dm U1[,U2...]")
	}
	c, err := slackClientOrFail(errOut)
	if err != nil {
		return err
	}
	id, err := c.OpenDM(ctx, users)
	if errors.Is(err, slack.ErrDMUsers) {
		return slackFail(errOut, "slack_params", err.Error())
	}
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]string{"channel": id})
}

// splitSlackIDs splits a comma list, dropping blanks.
func splitSlackIDs(v string) []string {
	var ids []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			ids = append(ids, s)
		}
	}
	return ids
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
		return "", fmt.Errorf("requires --text or --text-file")
	}
	return text, nil
}
