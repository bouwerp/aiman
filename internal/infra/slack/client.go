package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	// Sleep waits out a 429 Retry-After. Nil is a timer that honours ctx.
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxBody caps a response in bytes. Zero means 8 MiB.
	MaxBody int64
}

// Page is one page of a cursor-paged Slack listing.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// cursorMeta is Slack's response_metadata for paged methods.
type cursorMeta struct {
	Meta struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
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

// defaultMaxBody caps a Slack response when Client.MaxBody is zero.
const defaultMaxBody = 8 << 20

// maxRetries is how many times a call is repeated after HTTP 429.
const maxRetries = 2

// call POSTs payload as JSON.
func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	return c.do(ctx, method, out, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+method, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		return req, nil
	})
}

// callForm POSTs form as application/x-www-form-urlencoded, for methods
// that do not accept JSON.
func (c *Client) callForm(ctx context.Context, method string, form url.Values, out any) error {
	body := form.Encode()
	return c.do(ctx, method, out, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+method, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	})
}

// get calls method with GET and query as the query string, for methods
// that take no request body.
func (c *Client) get(ctx context.Context, method string, query url.Values, out any) error {
	target := c.base() + method
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return c.do(ctx, method, out, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	})
}

// do sends the request build makes, repeating it after HTTP 429 as Slack's
// Retry-After asks, at most maxRetries times.
func (c *Client) do(ctx context.Context, method string, out any, build func() (*http.Request, error)) error {
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return fmt.Errorf("slack %s: %w", method, err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		wait, err := c.send(req, method, out)
		if wait < 0 || attempt >= maxRetries {
			return err
		}
		if err := c.sleep(ctx, wait); err != nil {
			return fmt.Errorf("slack %s: %w", method, err)
		}
	}
}

// send runs one request. A non-negative wait means Slack rate limited it and
// asked for that pause; err is then the ratelimited failure.
func (c *Client) send(req *http.Request, method string, out any) (time.Duration, error) {
	resp, err := c.http().Do(req)
	if err != nil {
		return -1, fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return retryAfter(resp.Header.Get("Retry-After")), &apiFailure{Method: method, Code: "ratelimited"}
	}
	limit := c.maxBody()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return -1, fmt.Errorf("slack %s: %w", method, err)
	}
	if int64(len(raw)) > limit {
		return -1, fmt.Errorf("slack %s: response larger than %d bytes", method, limit)
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return -1, fmt.Errorf("slack %s: bad response", method)
	}
	if !env.OK {
		code := env.Error
		if code == "" {
			code = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return -1, &apiFailure{Method: method, Code: code}
	}
	if out == nil {
		return -1, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return -1, fmt.Errorf("slack %s: %w", method, err)
	}
	return -1, nil
}

// retryAfter reads Slack's Retry-After seconds; missing or bad means 1s.
func retryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n < 0 {
		return time.Second
	}
	return time.Duration(n) * time.Second
}

func (c *Client) maxBody() int64 {
	if c.MaxBody > 0 {
		return c.MaxBody
	}
	return defaultMaxBody
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// AuthTest checks the user token and returns who it is.
func (c *Client) AuthTest(ctx context.Context) (Auth, error) {
	var out struct {
		Auth
	}
	err := c.call(ctx, "auth.test", map[string]string{}, &out)
	return out.Auth, err
}

// ResolveChannel accepts a channel or member id, or a #name / name of a
// channel the member can see.
func (c *Client) ResolveChannel(ctx context.Context, nameOrID string) (string, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if isChannelID(nameOrID) || isUserID(nameOrID) {
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
	return isUpperAlnum(s[1:])
}

func isUpperAlnum(s string) bool {
	for _, r := range s {
		if !unicode.IsUpper(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
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
