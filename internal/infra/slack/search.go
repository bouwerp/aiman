package slack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// SearchQuery is one page of Real-time Search. In, From, After and Before
// become query terms (in:<#C>, from:<@U>, after:YYYY-MM-DD, before:YYYY-MM-DD).
// Kinds are content types: messages (default), files, channels, users.
type SearchQuery struct {
	Query   string
	Private bool
	In      string
	From    string
	After   string
	Before  string
	Kinds   []string
	Limit   int
	Cursor  string
}

// SearchResult is one page of hits. Engine is realtime, or legacy when the
// workspace has no Real-time Search and search.messages answered instead.
type SearchResult struct {
	Engine     string      `json:"engine"`
	Hits       []SearchHit `json:"hits"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// SearchHit is a message, file, channel or user.
type SearchHit struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Channel   string `json:"channel,omitempty"`
	TS        string `json:"ts,omitempty"`
	User      string `json:"user,omitempty"`
	Text      string `json:"text,omitempty"`
	Permalink string `json:"permalink,omitempty"`
}

// Emoji is one custom emoji; AliasOf names the emoji an alias points to.
type Emoji struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	AliasOf string `json:"alias_of,omitempty"`
}

// searchMaxLimit is assistant.search.context's page size cap.
const searchMaxLimit = 20

var searchKinds = map[string]string{
	"messages": "message",
	"files":    "file",
	"channels": "channel",
	"users":    "user",
}

// Search returns one page from assistant.search.context. Real-time Search is
// rate limited per user and each page counts, so it never follows the
// cursor. When the workspace lacks the feature, a messages-only search falls
// back to search.messages.
func (c *Client) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	kinds := q.Kinds
	if len(kinds) == 0 {
		kinds = []string{"messages"}
	}
	for _, k := range kinds {
		if _, ok := searchKinds[k]; !ok {
			return SearchResult{}, fmt.Errorf("slack search: unknown content type %q", k)
		}
	}
	channelTypes := []string{"public_channel"}
	if q.Private {
		channelTypes = defaultChannelTypes
	}
	payload := map[string]any{
		"query":         searchText(q),
		"channel_types": channelTypes,
		"content_types": kinds,
	}
	if q.Limit > 0 {
		payload["limit"] = min(q.Limit, searchMaxLimit)
	}
	if q.Cursor != "" {
		payload["cursor"] = q.Cursor
	}
	var out struct {
		Results struct {
			Messages []struct {
				AuthorUserID string `json:"author_user_id"`
				ChannelID    string `json:"channel_id"`
				ChannelName  string `json:"channel_name"`
				MessageTS    string `json:"message_ts"`
				Content      string `json:"content"`
				Permalink    string `json:"permalink"`
			} `json:"messages"`
			Files []struct {
				FileID         string `json:"file_id"`
				Title          string `json:"title"`
				UploaderUserID string `json:"uploader_user_id"`
				AuthorUserID   string `json:"author_user_id"`
				Content        string `json:"content"`
				Permalink      string `json:"permalink"`
			} `json:"files"`
			Channels []struct {
				ID        string `json:"id"`
				ChannelID string `json:"channel_id"`
				Name      string `json:"name"`
				Purpose   string `json:"purpose"`
				Topic     string `json:"topic"`
				Permalink string `json:"permalink"`
			} `json:"channels"`
			Users []struct {
				UserID    string `json:"user_id"`
				ID        string `json:"id"`
				FullName  string `json:"full_name"`
				RealName  string `json:"real_name"`
				Name      string `json:"name"`
				Title     string `json:"title"`
				Permalink string `json:"permalink"`
			} `json:"users"`
		} `json:"results"`
		cursorMeta
	}
	err := c.call(ctx, "assistant.search.context", payload, &out)
	if searchUnavailable(err) && len(kinds) == 1 && kinds[0] == "messages" {
		return c.searchLegacy(ctx, q)
	}
	if err != nil {
		return SearchResult{}, err
	}
	hits := []SearchHit{}
	r := out.Results
	for _, m := range r.Messages {
		hits = append(hits, SearchHit{Kind: "message", Channel: m.ChannelID, Name: m.ChannelName, TS: m.MessageTS, User: m.AuthorUserID, Text: m.Content, Permalink: m.Permalink})
	}
	for _, f := range r.Files {
		user := f.UploaderUserID
		if user == "" {
			user = f.AuthorUserID
		}
		hits = append(hits, SearchHit{Kind: "file", ID: f.FileID, Name: f.Title, User: user, Text: f.Content, Permalink: f.Permalink})
	}
	for _, ch := range r.Channels {
		text := ch.Purpose
		if text == "" {
			text = ch.Topic
		}
		hits = append(hits, SearchHit{Kind: "channel", ID: searchFirst(ch.ID, ch.ChannelID), Name: ch.Name, Text: text, Permalink: ch.Permalink})
	}
	for _, u := range r.Users {
		id := searchFirst(u.UserID, u.ID)
		hits = append(hits, SearchHit{Kind: "user", ID: id, User: id, Name: searchFirst(u.FullName, u.RealName, u.Name), Text: u.Title, Permalink: u.Permalink})
	}
	return SearchResult{Engine: "realtime", Hits: hits, NextCursor: out.Meta.NextCursor}, nil
}

// searchLegacy returns one page from search.messages.
func (c *Client) searchLegacy(ctx context.Context, q SearchQuery) (SearchResult, error) {
	v := url.Values{"query": {searchText(q)}}
	if q.Limit > 0 {
		v.Set("count", strconv.Itoa(q.Limit))
	}
	setIf(v, "cursor", q.Cursor)
	var out struct {
		Messages struct {
			Matches []struct {
				Channel struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
				Permalink string `json:"permalink"`
				Text      string `json:"text"`
				TS        string `json:"ts"`
				User      string `json:"user"`
			} `json:"matches"`
		} `json:"messages"`
		cursorMeta
	}
	if err := c.get(ctx, "search.messages", v, &out); err != nil {
		return SearchResult{}, err
	}
	hits := make([]SearchHit, 0, len(out.Messages.Matches))
	for _, m := range out.Messages.Matches {
		hits = append(hits, SearchHit{Kind: "message", Channel: m.Channel.ID, Name: m.Channel.Name, TS: m.TS, User: m.User, Text: m.Text, Permalink: m.Permalink})
	}
	return SearchResult{Engine: "legacy", Hits: hits, NextCursor: out.Meta.NextCursor}, nil
}

// searchUnavailable reports whether err says the workspace has no Real-time
// Search, the only case that falls back to search.messages.
func searchUnavailable(err error) bool {
	var f *apiFailure
	return errors.As(err, &f) && (f.Code == "feature_not_enabled" || f.Code == "assistant_search_context_disabled")
}

func searchText(q SearchQuery) string {
	terms := []string{}
	if s := strings.TrimSpace(q.Query); s != "" {
		terms = append(terms, s)
	}
	if q.In != "" {
		terms = append(terms, "in:<#"+q.In+">")
	}
	if q.From != "" {
		terms = append(terms, "from:<@"+q.From+">")
	}
	if q.After != "" {
		terms = append(terms, "after:"+q.After)
	}
	if q.Before != "" {
		terms = append(terms, "before:"+q.Before)
	}
	return strings.Join(terms, " ")
}

func searchFirst(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Emoji returns the workspace's custom emoji whose names contain any of the
// comma-separated terms in query, ignoring case, sorted by name. An empty
// query returns all of them.
func (c *Client) Emoji(ctx context.Context, query string) ([]Emoji, error) {
	var out struct {
		Emoji map[string]string `json:"emoji"`
	}
	if err := c.get(ctx, "emoji.list", nil, &out); err != nil {
		return nil, err
	}
	var terms []string
	for _, t := range strings.Split(query, ",") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			terms = append(terms, t)
		}
	}
	list := []Emoji{}
	for name, val := range out.Emoji {
		if !emojiMatches(name, terms) {
			continue
		}
		e := Emoji{Name: name}
		if alias, ok := strings.CutPrefix(val, "alias:"); ok {
			e.AliasOf = alias
		} else {
			e.URL = val
		}
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func emojiMatches(name string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	name = strings.ToLower(name)
	for _, t := range terms {
		if strings.Contains(name, t) {
			return true
		}
	}
	return false
}
