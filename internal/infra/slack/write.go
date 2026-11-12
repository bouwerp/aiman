package slack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Writes send JSON bodies. Message text goes in markdown_text, which Slack
// renders as standard markdown; it refuses a call carrying text as well
// (markdown_text_conflict).

// Post is a chat.postMessage request. Broadcast also shows a thread reply in
// the channel; NoUnfurl turns off link and media previews.
type Post struct {
	Channel   string
	Text      string
	ThreadTS  string
	Broadcast bool
	NoUnfurl  bool
	// Plain sends Text as mrkdwn `text` instead of `markdown_text`, for a
	// caller that escapes &, < and > itself (Slack decodes those entities
	// in `text`), such as the notification sink.
	Plain bool
}

// Post sends a message as the member who owns the user token.
func (c *Client) Post(ctx context.Context, p Post) (Posted, error) {
	req := map[string]any{"channel": p.Channel, "markdown_text": p.Text}
	if p.Plain {
		req = map[string]any{"channel": p.Channel, "text": p.Text}
	}
	if p.ThreadTS != "" {
		req["thread_ts"] = p.ThreadTS
	}
	if p.Broadcast {
		req["reply_broadcast"] = true
	}
	if p.NoUnfurl {
		req["unfurl_links"] = false
		req["unfurl_media"] = false
	}
	var out Posted
	err := c.call(ctx, "chat.postMessage", req, &out)
	return out, err
}

// ScheduleMaxAhead is how far ahead chat.scheduleMessage accepts a post_at.
const ScheduleMaxAhead = 120 * 24 * time.Hour

// ErrScheduleTime is a schedule time Slack would refuse.
var ErrScheduleTime = errors.New("schedule time must be in the future and at most 120 days ahead")

// ErrChannelName is a channel name conversations.create would refuse.
var ErrChannelName = errors.New("channel name must be 1 to 80 lowercase letters, digits, - or _")

// ErrDMUsers is a member count conversations.open would refuse.
var ErrDMUsers = errors.New("a DM takes 1 to 8 members")

// scheduledListMaxPages bounds ScheduledList so a cursor loop cannot run away.
const scheduledListMaxPages = 20

// ScheduleRequest is a chat.scheduleMessage request.
type ScheduleRequest struct {
	Channel string
	Text    string
	At      time.Time
	Thread  string
}

// Scheduled is a message waiting to be sent.
type Scheduled struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Text    string `json:"text,omitempty"`
	PostAt  int64  `json:"post_at"`
}

// unixTime decodes a post_at that Slack sends as a number or a string.
type unixTime int64

func (u *unixTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*u = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("post_at %s: %w", b, err)
	}
	*u = unixTime(n)
	return nil
}

// Schedule queues a message for At. A time in the past or more than
// ScheduleMaxAhead away is refused before any call.
func (c *Client) Schedule(ctx context.Context, r ScheduleRequest) (Scheduled, error) {
	if now := time.Now(); !r.At.After(now) || r.At.Sub(now) > ScheduleMaxAhead {
		return Scheduled{}, fmt.Errorf("%w: got %s", ErrScheduleTime, r.At.Format(time.RFC3339))
	}
	req := map[string]any{"channel": r.Channel, "markdown_text": r.Text, "post_at": r.At.Unix()}
	if r.Thread != "" {
		req["thread_ts"] = r.Thread
	}
	var out struct {
		Channel string   `json:"channel"`
		ID      string   `json:"scheduled_message_id"`
		PostAt  unixTime `json:"post_at"`
	}
	if err := c.call(ctx, "chat.scheduleMessage", req, &out); err != nil {
		return Scheduled{}, err
	}
	return Scheduled{ID: out.ID, Channel: out.Channel, Text: r.Text, PostAt: int64(out.PostAt)}, nil
}

// ScheduledList returns the token member's scheduled messages, in channel
// when it is set.
func (c *Client) ScheduledList(ctx context.Context, channel string) ([]Scheduled, error) {
	items := []Scheduled{}
	cursor := ""
	for range scheduledListMaxPages {
		v := url.Values{}
		setIf(v, "channel", channel)
		setIf(v, "cursor", cursor)
		var out struct {
			Messages []struct {
				ID      string   `json:"id"`
				Channel string   `json:"channel_id"`
				PostAt  unixTime `json:"post_at"`
				Text    string   `json:"text"`
			} `json:"scheduled_messages"`
			cursorMeta
		}
		if err := c.callForm(ctx, "chat.scheduledMessages.list", v, &out); err != nil {
			return nil, err
		}
		for _, m := range out.Messages {
			items = append(items, Scheduled{ID: m.ID, Channel: m.Channel, Text: m.Text, PostAt: int64(m.PostAt)})
		}
		cursor = out.Meta.NextCursor
		if cursor == "" {
			break
		}
	}
	return items, nil
}

// ScheduleCancel deletes a scheduled message before it is sent.
func (c *Client) ScheduleCancel(ctx context.Context, channel, id string) error {
	return c.call(ctx, "chat.deleteScheduledMessage", map[string]string{
		"channel": channel, "scheduled_message_id": id,
	}, nil)
}

// channelNameRe is what conversations.create accepts as a name.
var channelNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)

// CreateChannel creates a channel and invites users to it when there are
// any. A name Slack would refuse is refused before any call.
func (c *Client) CreateChannel(ctx context.Context, name string, private bool, invite []string) (Channel, error) {
	if !channelNameRe.MatchString(name) {
		return Channel{}, fmt.Errorf("%w: got %q", ErrChannelName, name)
	}
	var out struct {
		Channel Channel `json:"channel"`
	}
	if err := c.call(ctx, "conversations.create", map[string]any{"name": name, "is_private": private}, &out); err != nil {
		return Channel{}, err
	}
	if len(invite) > 0 {
		err := c.call(ctx, "conversations.invite", map[string]string{
			"channel": out.Channel.ID, "users": strings.Join(invite, ","),
		}, nil)
		if err != nil {
			return out.Channel, fmt.Errorf("channel %s created; %w", out.Channel.ID, err)
		}
	}
	return out.Channel, nil
}

// OpenDM opens (or finds) the direct message with 1 to 8 members and
// returns its id; more than one member is a group DM.
func (c *Client) OpenDM(ctx context.Context, users []string) (string, error) {
	if len(users) < 1 || len(users) > 8 {
		return "", fmt.Errorf("%w, got %d", ErrDMUsers, len(users))
	}
	var out struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := c.call(ctx, "conversations.open", map[string]string{"users": strings.Join(users, ",")}, &out); err != nil {
		return "", err
	}
	return out.Channel.ID, nil
}
