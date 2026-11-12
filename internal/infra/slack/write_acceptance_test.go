package slack

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// Acceptance tests for slack-parity piece E (writes). Do not edit.

func TestSendOptionsUseMarkdownTextAlone(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("chat.postMessage", `{"ok":true,"channel":"C1","ts":"5.0"}`)
	got, err := c.Post(context.Background(), Post{Channel: "C1", Text: "**bold**", ThreadTS: "1.0", Broadcast: true, NoUnfurl: true})
	if err != nil || got.TS != "5.0" {
		t.Fatalf("post %+v %v", got, err)
	}
	p := f.called("chat.postMessage")[0].Params
	if p["markdown_text"] != "**bold**" {
		t.Fatalf("markdown_text %q", p["markdown_text"])
	}
	if _, ok := p["text"]; ok {
		t.Fatal("text sent with markdown_text (markdown_text_conflict)")
	}
	if p["thread_ts"] != "1.0" || p["reply_broadcast"] != "true" || p["unfurl_links"] != "false" || p["unfurl_media"] != "false" {
		t.Fatalf("post params %v", p)
	}
	if _, err := c.Post(context.Background(), Post{Channel: "C1", Text: "plain"}); err != nil {
		t.Fatal(err)
	}
	p = f.called("chat.postMessage")[1].Params
	for _, k := range []string{"reply_broadcast", "unfurl_links", "unfurl_media"} {
		if _, ok := p[k]; ok {
			t.Errorf("%s sent without its flag: %v", k, p)
		}
	}
}

func TestScheduleListAndCancel(t *testing.T) {
	f, c := newFakeSlack(t)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	unix := strconv.FormatInt(at.Unix(), 10)
	f.on("chat.scheduleMessage", `{"ok":true,"channel":"C1","scheduled_message_id":"Q1","post_at":"`+unix+`"}`)
	f.on("chat.scheduledMessages.list", `{"ok":true,"scheduled_messages":[{"id":"Q1","channel_id":"C1","post_at":`+unix+`,"text":"later"}],"response_metadata":{"next_cursor":""}}`)
	f.on("chat.deleteScheduledMessage", `{"ok":true}`)
	s, err := c.Schedule(context.Background(), ScheduleRequest{Channel: "C1", Text: "later", At: at, Thread: "1.0"})
	if err != nil || s.ID != "Q1" || s.Channel != "C1" || s.PostAt != at.Unix() {
		t.Fatalf("schedule %+v %v", s, err)
	}
	p := f.called("chat.scheduleMessage")[0].Params
	if p["post_at"] != strconv.FormatInt(at.Unix(), 10) || p["markdown_text"] != "later" || p["thread_ts"] != "1.0" {
		t.Fatalf("schedule params %v", p)
	}
	list, err := c.ScheduledList(context.Background(), "C1")
	if err != nil || len(list) != 1 || list[0].ID != "Q1" || list[0].Text != "later" {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := c.ScheduleCancel(context.Background(), "C1", "Q1"); err != nil {
		t.Fatal(err)
	}
	d := f.called("chat.deleteScheduledMessage")[0].Params
	if d["channel"] != "C1" || d["scheduled_message_id"] != "Q1" {
		t.Fatalf("delete params %v", d)
	}
}

func TestScheduleRefusesPastAndTooFar(t *testing.T) {
	f, c := newFakeSlack(t)
	for _, at := range []time.Time{time.Now().Add(-time.Minute), time.Now().Add(121 * 24 * time.Hour)} {
		if _, err := c.Schedule(context.Background(), ScheduleRequest{Channel: "C1", Text: "x", At: at}); err == nil {
			t.Fatalf("schedule at %v accepted", at)
		}
	}
	if len(f.called("chat.scheduleMessage")) != 0 {
		t.Fatal("a refused schedule reached Slack")
	}
}

func TestCreateChannelAndInvite(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("conversations.create", `{"ok":true,"channel":{"id":"C8","name":"qa-nov","is_private":true}}`)
	f.on("conversations.invite", `{"ok":true,"channel":{"id":"C8"}}`)
	ch, err := c.CreateChannel(context.Background(), "qa-nov", true, []string{"U1", "U2"})
	if err != nil || ch.ID != "C8" || ch.Name != "qa-nov" {
		t.Fatalf("create %+v %v", ch, err)
	}
	p := f.called("conversations.create")[0].Params
	if p["name"] != "qa-nov" || p["is_private"] != "true" {
		t.Fatalf("create params %v", p)
	}
	inv := f.called("conversations.invite")[0].Params
	if inv["channel"] != "C8" || inv["users"] != "U1,U2" {
		t.Fatalf("invite params %v", inv)
	}
	if _, err := c.CreateChannel(context.Background(), "Bad Name!", false, nil); err == nil {
		t.Fatal("invalid channel name accepted")
	}
	if len(f.called("conversations.create")) != 1 {
		t.Fatal("invalid name reached Slack")
	}
}

func TestDMOpensConversation(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("conversations.open", `{"ok":true,"channel":{"id":"G5"}}`)
	id, err := c.OpenDM(context.Background(), []string{"U1", "U2"})
	if err != nil || id != "G5" || f.called("conversations.open")[0].Params["users"] != "U1,U2" {
		t.Fatalf("dm %q %v", id, err)
	}
	if _, err := c.OpenDM(context.Background(), []string{"U1", "U2", "U3", "U4", "U5", "U6", "U7", "U8", "U9"}); err == nil {
		t.Fatal("more than 8 users accepted")
	}
}
