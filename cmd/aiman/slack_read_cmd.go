package main

import (
	"context"
	"io"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func init() {
	registerSlack("history", slackCommand{run: runSlackHistory, usage: `--channel <name|C…|U…|link> [--limit n] [--oldest ts] [--latest ts] [--cursor c]

  A user id reads the DM with that member.` + slackLinkHelp})
	registerSlack("thread", slackCommand{run: runSlackThread, usage: `--channel <name|C…> --ts <ts|link> [--limit n] [--oldest ts] [--latest ts] [--cursor c]

  A reply's link reads its parent's thread.` + slackLinkHelp})
	registerSlack("channels", slackCommand{run: runSlackChannels, usage: "[--mine] [--prefix p] [--types public_channel,…] [--exclude-archived] [--limit n] [--cursor c]"})
	registerSlack("members", slackCommand{run: runSlackMembers, usage: "--channel <name|C…|link> [--limit n] [--cursor c]"})
	registerSlack("user", slackCommand{run: runSlackUser, usage: "[U…]\n\n  Without an id, the member who owns the token."})
	registerSlack("reactions", slackCommand{run: runSlackReactions, usage: "--channel <name|C…> --ts <ts|link>" + slackLinkHelp})
}

// slackMessages is the output of history and thread.
type slackMessages struct {
	Messages   []slack.Message `json:"messages"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func runSlackHistory(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	page, err := c.HistoryPage(ctx, slack.HistoryQuery{
		Channel: channel, Oldest: flags["oldest"], Latest: flags["latest"],
		Cursor: flags["cursor"], Limit: atoi(flags["limit"]),
	})
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, slackMessages{Messages: nonNil(page.Items), NextCursor: page.NextCursor})
}

func runSlackThread(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	if flags["ts"] == "" {
		return slackFail(errOut, "slack_params", "thread requires --ts")
	}
	page, err := c.RepliesPage(ctx, slack.RepliesQuery{
		Channel: channel, TS: flags["ts"], Oldest: flags["oldest"], Latest: flags["latest"],
		Cursor: flags["cursor"], Limit: atoi(flags["limit"]),
	})
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, slackMessages{Messages: nonNil(page.Items), NextCursor: page.NextCursor})
}

func runSlackChannels(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args, "mine", "exclude-archived")
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	q := slack.ChannelQuery{
		Mine: flags["mine"] != "", Prefix: flags["prefix"], Cursor: flags["cursor"],
		Limit: atoi(flags["limit"]), ExcludeArchived: flags["exclude-archived"] != "",
	}
	if t := flags["types"]; t != "" {
		q.Types = strings.Split(t, ",")
	}
	page, err := c.ChannelsPage(ctx, q)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, struct {
		Channels   []slack.Channel `json:"channels"`
		NextCursor string          `json:"next_cursor,omitempty"`
	}{nonNil(page.Items), page.NextCursor})
}

func runSlackMembers(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	page, err := c.Members(ctx, channel, flags["cursor"], atoi(flags["limit"]))
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, struct {
		Members    []slack.User `json:"members"`
		NextCursor string       `json:"next_cursor,omitempty"`
	}{nonNil(page.Items), page.NextCursor})
}

// runSlackUser prints a member's profile; with no id, the token's member.
func runSlackUser(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	_, rest := slackFlags(args)
	if len(rest) > 1 {
		return slackFail(errOut, "slack_params", "user takes at most one user id")
	}
	c, err := slackClientOrFail(ctx, errOut)
	if err != nil {
		return err
	}
	id := ""
	if len(rest) == 1 {
		id = rest[0]
	}
	p, err := c.Profile(ctx, id)
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, p)
}

func runSlackReactions(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, _ := slackFlags(args)
	c, channel, err := slackChannel(ctx, flags, errOut)
	if err != nil {
		return err
	}
	if flags["ts"] == "" {
		return slackFail(errOut, "slack_params", "reactions requires --ts")
	}
	rs, err := c.Reactions(ctx, channel, flags["ts"])
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"reactions": rs})
}
