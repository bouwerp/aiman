package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// APIBase is the Slack Web API prefix. Tests point it at an httptest server.
var APIBase = "https://slack.com/api/"

// Client calls the Slack Web API as the Aiman bot.
type Client struct {
	Token string
	Base  string
	HTTP  *http.Client
	// Poll is how often Wait re-reads a thread. Zero means 2s.
	Poll time.Duration
}

// Message is one Slack message the agent can read or wait for.
type Message struct {
	Type   string `json:"type"`
	User   string `json:"user"`
	BotID  string `json:"bot_id,omitempty"`
	Text   string `json:"text"`
	TS     string `json:"ts"`
	Thread string `json:"thread_ts,omitempty"`
}

// Channel is a conversation the bot can see.
type Channel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsMember bool   `json:"is_member"`
	IsIM     bool   `json:"is_im"`
}

// Post is a chat.postMessage request.
type Post struct {
	Channel  string
	Text     string
	ThreadTS string
}

// Posted is the identity of a message the bot just sent.
type Posted struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// Auth is the bot identity from auth.test, without the token.
type Auth struct {
	Team   string `json:"team"`
	User   string `json:"user"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

func (c *Client) base() string {
	if c.Base != "" {
		return c.Base
	}
	return APIBase
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) poll() time.Duration {
	if c.Poll > 0 {
		return c.Poll
	}
	return 2 * time.Second
}

type apiFailure struct {
	Method string
	Code   string
}

func (e *apiFailure) Error() string {
	return e.Method + ": " + e.Code
}

func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("slack %s: bad response", method)
	}
	if !env.OK {
		code := env.Error
		if code == "" {
			code = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return &apiFailure{Method: method, Code: code}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	return nil
}

// AuthTest checks the user token and returns who it is.
func (c *Client) AuthTest(ctx context.Context) (Auth, error) {
	var out struct {
		Auth
	}
	err := c.call(ctx, "auth.test", map[string]string{}, &out)
	return out.Auth, err
}

// Post sends a message as the member who owns the user token.
func (c *Client) Post(ctx context.Context, p Post) (Posted, error) {
	req := map[string]string{"channel": p.Channel, "text": p.Text}
	if p.ThreadTS != "" {
		req["thread_ts"] = p.ThreadTS
	}
	var out Posted
	err := c.call(ctx, "chat.postMessage", req, &out)
	return out, err
}

// History returns the latest messages in a channel. Slack lists them newest first.
func (c *Client) History(ctx context.Context, channel string, limit int) ([]Message, error) {
	return c.messages(ctx, "conversations.history", channel, "", limit)
}

// Replies returns messages in a thread, oldest first as Slack returns them.
func (c *Client) Replies(ctx context.Context, channel, thread string, limit int) ([]Message, error) {
	return c.messages(ctx, "conversations.replies", channel, thread, limit)
}

func (c *Client) messages(ctx context.Context, method, channel, thread string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 20
	}
	req := map[string]any{"channel": channel, "limit": limit}
	if thread != "" {
		req["ts"] = thread
	}
	var out struct {
		Messages []Message `json:"messages"`
	}
	if err := c.call(ctx, method, req, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// Channels lists conversations the bot can see.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var all []Channel
	cursor := ""
	for page := 0; page < 10; page++ {
		req := map[string]any{
			"types": "public_channel,private_channel,mpim,im",
			"limit": 200,
		}
		if cursor != "" {
			req["cursor"] = cursor
		}
		var out struct {
			Channels []Channel `json:"channels"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := c.call(ctx, "conversations.list", req, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Channels...)
		cursor = out.Meta.NextCursor
		if cursor == "" {
			break
		}
	}
	return all, nil
}

// ResolveChannel accepts a Slack id or a #name / name of a channel the bot is in.
func (c *Client) ResolveChannel(ctx context.Context, nameOrID string) (string, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if isChannelID(nameOrID) {
		return nameOrID, nil
	}
	want := strings.TrimPrefix(nameOrID, "#")
	chs, err := c.Channels(ctx)
	if err != nil {
		return "", err
	}
	for _, ch := range chs {
		if strings.EqualFold(ch.Name, want) {
			return ch.ID, nil
		}
	}
	return "", fmt.Errorf("slack channel %q not found", nameOrID)
}

func isChannelID(s string) bool {
	if len(s) < 2 {
		return false
	}
	switch s[0] {
	case 'C', 'G', 'D':
	default:
		return false
	}
	for _, r := range s[1:] {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// User is a Slack member the bot is allowed to see.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
}

// User looks up one member by id.
func (c *Client) User(ctx context.Context, id string) (User, error) {
	var out struct {
		User User `json:"user"`
	}
	err := c.call(ctx, "users.info", map[string]string{"user": id}, &out)
	return out.User, err
}

// React adds an emoji reaction (without colons) to a message.
func (c *Client) React(ctx context.Context, channel, ts, emoji string) error {
	emoji = strings.Trim(emoji, ":")
	return c.call(ctx, "reactions.add", map[string]string{
		"channel": channel, "timestamp": ts, "name": emoji,
	}, nil)
}

// Wait polls a thread until a message newer than after arrives from someone
// other than botUser. botUser empty means any non-bot message counts.
func (c *Client) Wait(ctx context.Context, channel, thread, after, botUser string) (Message, error) {
	if after == "" {
		after = fmt.Sprintf("%d.000000", time.Now().Unix())
	}
	for {
		msgs, err := c.Replies(ctx, channel, thread, 100)
		if err != nil {
			return Message{}, err
		}
		if msg, ok := newerHuman(msgs, after, botUser); ok {
			return msg, nil
		}
		timer := time.NewTimer(c.poll())
		select {
		case <-ctx.Done():
			timer.Stop()
			return Message{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func newerHuman(msgs []Message, after, botUser string) (Message, bool) {
	var found Message
	ok := false
	for _, m := range msgs {
		if m.TS <= after || m.BotID != "" {
			continue
		}
		if botUser != "" && m.User == botUser {
			continue
		}
		if !ok || m.TS < found.TS {
			found = m
			ok = true
		}
	}
	return found, ok
}
