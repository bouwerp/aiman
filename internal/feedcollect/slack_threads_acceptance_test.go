//go:build feedaccept || feedaccept_p3d

package feedcollect_test

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/slack"
)

// A reply to a thread the member is in reaches the feed even when the thread's
// parent is older than the conversation cursor, so history never returns it.
func TestSlackFollowsThreadsTheMemberIsIn(t *testing.T) {
	api := newFakeSlack(channel("C1"))
	env := newSlackEnv(t, api, 40, nil, nil)
	at := collectT0.Add(30 * time.Second)
	parent := slackTS(at, 1)
	api.post("C1", slack.Message{Type: "message", User: "U2", TS: parent, Text: "question for the team", ReplyCount: 1})
	api.reply("C1", parent, msg(self, slackTS(at, 2), "I will look"))
	// A quiet channel is read within 15 minutes; by then the thread is known.
	for m := 1; m <= 16; m++ {
		env.at(time.Duration(m) * time.Minute)
		env.poll(t, nil)
	}

	// The new reply comes after the channel's cursor passed the parent, so
	// channel history no longer returns the parent.
	later := collectT0.Add(40 * time.Minute)
	replyTS := slackTS(later, 3)
	api.reply("C1", parent, msg("U3", replyTS, "any news?"))
	want := "C1/" + replyTS
	var got []domain.FeedEvent
	for m := 41; m <= 46 && len(got) == 0; m++ {
		env.at(time.Duration(m) * time.Minute)
		got = eventsFor(env.poll(t, nil), want)
	}
	if len(got) != 1 {
		t.Fatalf("the new reply %s did not reach the feed within 6 minutes", want)
	}
	if got[0].Class != domain.FeedEventClass("direct") || !got[0].Urgent {
		t.Errorf("reply in the member's thread = class %q urgent %v, want direct and urgent", got[0].Class, got[0].Urgent)
	}
	if got[0].ThreadKey != domain.FeedThreadKeySlack("C1", parent, replyTS) {
		t.Errorf("thread key = %q", got[0].ThreadKey)
	}
}

// A thread the member left more than 14 days ago is no longer read, and its
// thread cursor is removed.
func TestSlackForgetsOldThreads(t *testing.T) {
	api := newFakeSlack(channel("C1"))
	env := newSlackEnv(t, api, 40, nil, nil)
	at := collectT0.Add(30 * time.Second)
	parent := slackTS(at, 1)
	api.post("C1", slack.Message{Type: "message", User: "U2", TS: parent, Text: "old question", ReplyCount: 1})
	api.reply("C1", parent, msg(self, slackTS(at, 2), "noted"))
	for m := 1; m <= 16; m++ {
		env.at(time.Duration(m) * time.Minute)
		env.poll(t, nil)
	}
	hasThreadCursor := func() bool {
		for k := range env.cursors {
			if len(k) > 2 && k[:2] == "t:" {
				return true
			}
		}
		return false
	}
	if !hasThreadCursor() {
		t.Fatalf("no thread cursor after the member replied: %v", env.cursors)
	}
	api.takeCalls()
	env.at(15 * 24 * time.Hour)
	env.poll(t, nil)
	for _, c := range api.takeCalls() {
		if c.method == "replies" && c.ts == parent {
			t.Fatal("a thread idle for 15 days was read")
		}
	}
	if hasThreadCursor() {
		t.Fatal("the thread cursor of a thread idle for 15 days is still there")
	}
}
