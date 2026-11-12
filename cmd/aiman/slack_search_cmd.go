package main

import (
	"context"
	"io"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func init() {
	registerSlack("search", slackCommand{run: runSlackSearch})
	registerSlack("emoji", slackCommand{run: runSlackEmoji})
}

// slackSearchKinds are the content-type flags; none means messages.
var slackSearchKinds = []string{"files", "channels", "users"}

// runSlackSearch prints one page of Real-time Search; the caller follows
// next_cursor with --cursor.
func runSlackSearch(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	flags, rest := slackFlags(args, append([]string{"private"}, slackSearchKinds...)...)
	query := strings.TrimSpace(strings.Join(rest, " "))
	if query == "" {
		return slackFail(errOut, "slack_params", "search requires a query")
	}
	c, err := slackClientOrFail(errOut)
	if err != nil {
		return err
	}
	q := slack.SearchQuery{
		Query: query, Private: flags["private"] != "", From: flags["from"],
		After: flags["after"], Before: flags["before"],
		Limit: atoi(flags["limit"]), Cursor: flags["cursor"],
	}
	if in := flags["in"]; in != "" {
		if q.In, err = c.ResolveChannel(ctx, in); err != nil {
			return slackAPI(errOut, err)
		}
	}
	for _, k := range slackSearchKinds {
		if flags[k] != "" {
			q.Kinds = append(q.Kinds, k)
		}
	}
	res, err := c.Search(ctx, q)
	if err != nil {
		return slackAPI(errOut, err)
	}
	res.Hits = nonNil(res.Hits)
	return slackWrite(out, res)
}

// runSlackEmoji prints the workspace's custom emoji, filtered by an optional
// comma-separated list of name substrings.
func runSlackEmoji(ctx context.Context, args []string, _ io.Reader, out, errOut io.Writer) error {
	_, rest := slackFlags(args)
	c, err := slackClientOrFail(errOut)
	if err != nil {
		return err
	}
	list, err := c.Emoji(ctx, strings.Join(rest, ","))
	if err != nil {
		return slackAPI(errOut, err)
	}
	return slackWrite(out, map[string]any{"emoji": nonNil(list)})
}
