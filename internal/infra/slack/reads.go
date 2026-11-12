package slack

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Read methods take form-encoded bodies: Slack accepts that encoding for every
// method, while several reads ignore a JSON body.

// HistoryQuery pages conversations.history. Channel may be a user id (U… or
// W…), which opens the direct message with that member first.
type HistoryQuery struct {
	Channel string
	Oldest  string
	Latest  string
	Cursor  string
	Limit   int
}

// RepliesQuery pages conversations.replies for the thread rooted at TS.
type RepliesQuery struct {
	Channel string
	TS      string
	Oldest  string
	Latest  string
	Cursor  string
	Limit   int
}

// ChannelQuery pages the conversations a member can see. Mine lists only the
// ones the token's member belongs to. Prefix keeps names starting with it,
// ignoring case; it filters the page Slack returned, so a page can be short.
type ChannelQuery struct {
	Mine            bool
	Types           []string
	Prefix          string
	Cursor          string
	Limit           int
	ExcludeArchived bool
}

// User is a Slack member the token is allowed to see.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
}

// Profile is a member with the fields from users.profile.get.
type Profile struct {
	User
	Email       string `json:"email,omitempty"`
	Title       string `json:"title,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	StatusText  string `json:"status_text,omitempty"`
	TZ          string `json:"tz,omitempty"`
}

// Reaction is one emoji on a message and who added it.
type Reaction struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Users []string `json:"users"`
}

// defaultChannelTypes is every conversation type a member can see.
var defaultChannelTypes = []string{"public_channel", "private_channel", "mpim", "im"}

func setIf(v url.Values, key, val string) {
	if val != "" {
		v.Set(key, val)
	}
}

func setLimit(v url.Values, limit, def int) {
	if limit <= 0 {
		limit = def
	}
	v.Set("limit", strconv.Itoa(limit))
}

// HistoryPage returns one page of a channel's messages, newest first.
func (c *Client) HistoryPage(ctx context.Context, q HistoryQuery) (Page[Message], error) {
	channel, err := c.dmFor(ctx, q.Channel)
	if err != nil {
		return Page[Message]{}, err
	}
	v := url.Values{"channel": {channel}}
	setIf(v, "oldest", q.Oldest)
	setIf(v, "latest", q.Latest)
	setIf(v, "cursor", q.Cursor)
	setLimit(v, q.Limit, 20)
	return c.messagePage(ctx, "conversations.history", v)
}

// RepliesPage returns one page of a thread, oldest first.
func (c *Client) RepliesPage(ctx context.Context, q RepliesQuery) (Page[Message], error) {
	v := url.Values{"channel": {q.Channel}, "ts": {q.TS}}
	setIf(v, "oldest", q.Oldest)
	setIf(v, "latest", q.Latest)
	setIf(v, "cursor", q.Cursor)
	setLimit(v, q.Limit, 20)
	return c.messagePage(ctx, "conversations.replies", v)
}

func (c *Client) messagePage(ctx context.Context, method string, v url.Values) (Page[Message], error) {
	var out struct {
		Messages []Message `json:"messages"`
		cursorMeta
	}
	if err := c.callForm(ctx, method, v, &out); err != nil {
		return Page[Message]{}, err
	}
	return Page[Message]{Items: out.Messages, NextCursor: out.Meta.NextCursor}, nil
}

// dmFor turns a member id into the id of the direct message with them and
// returns any other id unchanged.
func (c *Client) dmFor(ctx context.Context, id string) (string, error) {
	if !isUserID(id) {
		return id, nil
	}
	var out struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	if err := c.callForm(ctx, "conversations.open", url.Values{"users": {id}}, &out); err != nil {
		return "", err
	}
	if out.Channel.ID == "" {
		return "", fmt.Errorf("slack conversations.open: no channel for %s", id)
	}
	return out.Channel.ID, nil
}

func isUserID(s string) bool {
	return len(s) > 1 && (s[0] == 'U' || s[0] == 'W') && isUpperAlnum(s[1:])
}

// History returns the latest messages in a channel. Slack lists them newest first.
func (c *Client) History(ctx context.Context, channel string, limit int) ([]Message, error) {
	page, err := c.HistoryPage(ctx, HistoryQuery{Channel: channel, Limit: limit})
	return page.Items, err
}

// Replies returns messages in a thread, oldest first as Slack returns them.
func (c *Client) Replies(ctx context.Context, channel, thread string, limit int) ([]Message, error) {
	page, err := c.RepliesPage(ctx, RepliesQuery{Channel: channel, TS: thread, Limit: limit})
	return page.Items, err
}

// ChannelsPage returns one page of conversations: users.conversations for
// Mine, else conversations.list.
func (c *Client) ChannelsPage(ctx context.Context, q ChannelQuery) (Page[Channel], error) {
	types := q.Types
	if len(types) == 0 {
		types = defaultChannelTypes
	}
	v := url.Values{"types": {strings.Join(types, ",")}}
	setIf(v, "cursor", q.Cursor)
	setLimit(v, q.Limit, 200)
	if q.ExcludeArchived {
		v.Set("exclude_archived", "true")
	}
	method := "conversations.list"
	if q.Mine {
		method = "users.conversations"
	}
	var out struct {
		Channels []Channel `json:"channels"`
		cursorMeta
	}
	if err := c.callForm(ctx, method, v, &out); err != nil {
		return Page[Channel]{}, err
	}
	items := out.Channels
	if q.Prefix != "" {
		prefix := strings.ToLower(strings.TrimPrefix(q.Prefix, "#"))
		items = items[:0:0]
		for _, ch := range out.Channels {
			if strings.HasPrefix(strings.ToLower(ch.Name), prefix) {
				items = append(items, ch)
			}
		}
	}
	return Page[Channel]{Items: items, NextCursor: out.Meta.NextCursor}, nil
}

// Channels lists conversations the member can see, up to ten pages.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var all []Channel
	cursor := ""
	for range 10 {
		page, err := c.ChannelsPage(ctx, ChannelQuery{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	return all, nil
}

// Members returns one page of a channel's members, each looked up with
// users.info.
func (c *Client) Members(ctx context.Context, channel, cursor string, limit int) (Page[User], error) {
	v := url.Values{"channel": {channel}}
	setIf(v, "cursor", cursor)
	setLimit(v, limit, 100)
	var out struct {
		Members []string `json:"members"`
		cursorMeta
	}
	if err := c.callForm(ctx, "conversations.members", v, &out); err != nil {
		return Page[User]{}, err
	}
	users := make([]User, 0, len(out.Members))
	for _, id := range out.Members {
		u, err := c.User(ctx, id)
		if err != nil {
			return Page[User]{}, err
		}
		users = append(users, u)
	}
	return Page[User]{Items: users, NextCursor: out.Meta.NextCursor}, nil
}

// User looks up one member by id.
func (c *Client) User(ctx context.Context, id string) (User, error) {
	u, _, err := c.userInfo(ctx, id)
	return u, err
}

func (c *Client) userInfo(ctx context.Context, id string) (User, string, error) {
	var out struct {
		User struct {
			User
			TZ string `json:"tz"`
		} `json:"user"`
	}
	err := c.callForm(ctx, "users.info", url.Values{"user": {id}}, &out)
	return out.User.User, out.User.TZ, err
}

// Profile returns a member with their profile fields. An empty id is the
// member who owns the token.
func (c *Client) Profile(ctx context.Context, id string) (Profile, error) {
	if id == "" {
		auth, err := c.AuthTest(ctx)
		if err != nil {
			return Profile{}, err
		}
		id = auth.UserID
	}
	u, tz, err := c.userInfo(ctx, id)
	if err != nil {
		return Profile{}, err
	}
	var out struct {
		Profile struct {
			Email       string `json:"email"`
			Title       string `json:"title"`
			DisplayName string `json:"display_name"`
			StatusText  string `json:"status_text"`
		} `json:"profile"`
	}
	if err := c.get(ctx, "users.profile.get", url.Values{"user": {id}}, &out); err != nil {
		return Profile{}, err
	}
	if u.ID == "" {
		u.ID = id
	}
	p := out.Profile
	return Profile{User: u, Email: p.Email, Title: p.Title, DisplayName: p.DisplayName, StatusText: p.StatusText, TZ: tz}, nil
}

// Reactions returns the emoji on one message.
func (c *Client) Reactions(ctx context.Context, channel, ts string) ([]Reaction, error) {
	var out struct {
		Message struct {
			Reactions []Reaction `json:"reactions"`
		} `json:"message"`
	}
	v := url.Values{"channel": {channel}, "timestamp": {ts}, "full": {"true"}}
	if err := c.callForm(ctx, "reactions.get", v, &out); err != nil {
		return nil, err
	}
	if out.Message.Reactions == nil {
		return []Reaction{}, nil
	}
	return out.Message.Reactions, nil
}
