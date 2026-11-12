//go:build slackparity_b

package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Acceptance tests for slack-parity piece B (search and emoji). Do not edit.

func TestSearchBuildsQueryAndTypes(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("assistant.search.context", `{"ok":true,"results":{"messages":[{"author_user_id":"U2","channel_id":"C1","message_ts":"1.0","content":"freeze starts monday","permalink":"https://x/p1"}]},"response_metadata":{"next_cursor":"n2"}}`)
	res, err := c.Search(context.Background(), SearchQuery{
		Query: "release freeze", Private: true, In: "C1", From: "U2",
		After: "2026-11-01", Before: "2026-11-10", Limit: 5, Cursor: "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "realtime" || res.NextCursor != "n2" || len(res.Hits) != 1 {
		t.Fatalf("result %+v", res)
	}
	h := res.Hits[0]
	if h.Kind != "message" || h.Channel != "C1" || h.TS != "1.0" || h.User != "U2" || h.Text != "freeze starts monday" || h.Permalink != "https://x/p1" {
		t.Fatalf("hit %+v", h)
	}
	p := f.called("assistant.search.context")[0].Params
	for _, want := range []string{"release freeze", "in:<#C1>", "from:<@U2>", "after:2026-11-01", "before:2026-11-10"} {
		if !strings.Contains(p["query"], want) {
			t.Errorf("query %q lacks %q", p["query"], want)
		}
	}
	var types []string
	_ = json.Unmarshal([]byte(p["channel_types"]), &types)
	if strings.Join(types, ",") != "public_channel,private_channel,mpim,im" {
		t.Errorf("private channel_types %v (raw %q)", types, p["channel_types"])
	}
	var kinds []string
	_ = json.Unmarshal([]byte(p["content_types"]), &kinds)
	if strings.Join(kinds, ",") != "messages" {
		t.Errorf("default content_types %v", kinds)
	}
	if p["limit"] != "5" || p["cursor"] != "n1" {
		t.Errorf("limit/cursor %v", p)
	}
	if len(f.called("assistant.search.context")) != 1 {
		t.Fatal("search must make one call per page")
	}
}

func TestSearchPublicOnlyByDefault(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("assistant.search.context", `{"ok":true,"results":{"channels":[{"id":"C9","name":"eng"}],"users":[{"user_id":"U9","full_name":"Ann"}]}}`)
	res, err := c.Search(context.Background(), SearchQuery{Query: "eng", Kinds: []string{"channels", "users"}})
	if err != nil {
		t.Fatal(err)
	}
	p := f.called("assistant.search.context")[0].Params
	var types, kinds []string
	_ = json.Unmarshal([]byte(p["channel_types"]), &types)
	_ = json.Unmarshal([]byte(p["content_types"]), &kinds)
	if strings.Join(types, ",") != "public_channel" || strings.Join(kinds, ",") != "channels,users" {
		t.Fatalf("types %v kinds %v", types, kinds)
	}
	var sawChannel, sawUser bool
	for _, h := range res.Hits {
		sawChannel = sawChannel || (h.Kind == "channel" && h.ID == "C9" && h.Name == "eng")
		sawUser = sawUser || h.Kind == "user"
	}
	if !sawChannel || !sawUser {
		t.Fatalf("hits %+v", res.Hits)
	}
}

func TestSearchFallsBackToLegacyForMessages(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("assistant.search.context", `{"ok":false,"error":"feature_not_enabled"}`)
	f.on("search.messages", `{"ok":true,"messages":{"matches":[{"channel":{"id":"C1","name":"general"},"permalink":"https://x/p","text":"42","ts":"9.0","user":"U1"}]}}`)
	res, err := c.Search(context.Background(), SearchQuery{Query: "meaning", In: "C1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "legacy" || len(res.Hits) != 1 || res.Hits[0].Channel != "C1" || res.Hits[0].Text != "42" {
		t.Fatalf("legacy result %+v", res)
	}
	lc := f.called("search.messages")[0]
	if lc.HTTPMethod != http.MethodGet || !strings.Contains(lc.Params["query"], "meaning") {
		t.Fatalf("legacy call %+v", lc)
	}
	if _, err := c.Search(context.Background(), SearchQuery{Query: "eng", Kinds: []string{"channels"}}); err == nil {
		t.Fatal("channel search cannot fall back to search.messages; want an error")
	}
}

func TestSearchOtherErrorsDoNotFallBack(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("assistant.search.context", `{"ok":false,"error":"missing_scope"}`)
	_, err := c.Search(context.Background(), SearchQuery{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "missing_scope") {
		t.Fatalf("err %v", err)
	}
	if len(f.called("search.messages")) != 0 {
		t.Fatal("fell back on a non-availability error")
	}
}

func TestEmojiFiltersByTerms(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("emoji.list", `{"ok":true,"emoji":{"shipit":"alias:squirrel","squirrel":"https://e/s.png","bowtie":"https://e/b.png"}}`)
	got, err := c.Emoji(context.Background(), "ship, bow")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Emoji{}
	for _, e := range got {
		names[e.Name] = e
	}
	if len(got) != 2 || names["shipit"].AliasOf != "squirrel" || names["bowtie"].URL != "https://e/b.png" {
		t.Fatalf("emoji %+v", got)
	}
	if f.called("emoji.list")[0].HTTPMethod != http.MethodGet {
		t.Fatal("emoji.list is GET")
	}
	all, err := c.Emoji(context.Background(), "")
	if err != nil || len(all) != 3 {
		t.Fatalf("all emoji %+v %v", all, err)
	}
}
