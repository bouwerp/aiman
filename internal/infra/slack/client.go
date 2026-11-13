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
	// Raw, when set, receives every Slack response body that passed the size
	// cap, ok or not, before it is decoded. A rate-limited answer that is
	// retried is not passed on.
	Raw func(method string, body []byte)
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

// Message is one Slack message the agent can read or wait for. Attachments
// and blocks are kept as Slack sent them.
type Message struct {
	Type        string            `json:"type"`
	Subtype     string            `json:"subtype,omitempty"`
	User        string            `json:"user"`
	BotID       string            `json:"bot_id,omitempty"`
	Text        string            `json:"text"`
	TS          string            `json:"ts"`
	Thread      string            `json:"thread_ts,omitempty"`
	ReplyCount  int               `json:"reply_count,omitempty"`
	Reactions   []Reaction        `json:"reactions,omitempty"`
	Files       []MessageFile     `json:"files,omitempty"`
	Attachments []json.RawMessage `json:"attachments,omitempty"`
	Blocks      []json.RawMessage `json:"blocks,omitempty"`
}

// MessageFile is a file shared in a message. Its id is what file read takes;
// url_private needs the user token and is never downloaded on a read.
type MessageFile struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Title      string `json:"title,omitempty"`
	Mimetype   string `json:"mimetype,omitempty"`
	Filetype   string `json:"filetype,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Permalink  string `json:"permalink,omitempty"`
	URLPrivate string `json:"url_private,omitempty"`
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
	_, err := c.callHeader(ctx, method, payload, out)
	return err
}

// callHeader is call that also returns the headers of the answer it decoded.
func (c *Client) callHeader(ctx context.Context, method string, payload any, out any) (http.Header, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("slack %s: %w", method, err)
	}
	var hdr http.Header
	err = c.do(ctx, method, out, &hdr, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+method, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		return req, nil
	})
	return hdr, err
}

// callForm POSTs form as application/x-www-form-urlencoded, for methods
// that do not accept JSON.
func (c *Client) callForm(ctx context.Context, method string, form url.Values, out any) error {
	body := form.Encode()
	return c.do(ctx, method, out, nil, func() (*http.Request, error) {
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
	return c.do(ctx, method, out, nil, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	})
}

// do sends the request build makes, repeating it after HTTP 429 as Slack's
// Retry-After asks, at most maxRetries times. A non-nil hdr receives the
// headers of the last answer that was not rate limited.
func (c *Client) do(ctx context.Context, method string, out any, hdr *http.Header, build func() (*http.Request, error)) error {
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return fmt.Errorf("slack %s: %w", method, err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		wait, err := c.send(req, method, out, hdr)
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
func (c *Client) send(req *http.Request, method string, out any, hdr *http.Header) (time.Duration, error) {
	resp, err := c.http().Do(req)
	if err != nil {
		return -1, fmt.Errorf("slack %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return retryAfter(resp.Header.Get("Retry-After")), &apiFailure{Method: method, Code: "ratelimited"}
	}
	if hdr != nil {
		*hdr = resp.Header
	}
	limit := c.maxBody()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return -1, fmt.Errorf("slack %s: %w", method, err)
	}
	if int64(len(raw)) > limit {
		return -1, fmt.Errorf("slack %s: response larger than %d bytes", method, limit)
	}
	if c.Raw != nil {
		c.Raw(method, raw)
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

// AuthCheck runs auth.test once and returns who the token is and the
// UserScopes entries Slack did not grant, read from the X-OAuth-Scopes header.
// checked is false when Slack sent no such header.
func (c *Client) AuthCheck(ctx context.Context) (auth Auth, missing []string, checked bool, err error) {
	var out struct {
		Auth
	}
	hdr, err := c.callHeader(ctx, "auth.test", map[string]string{}, &out)
	if err != nil {
		return Auth{}, nil, false, err
	}
	vals := hdr.Values("X-OAuth-Scopes")
	if len(vals) == 0 {
		return out.Auth, nil, false, nil
	}
	return out.Auth, missingScopes(splitScopes(strings.Join(vals, ","))), true, nil
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
