package feedcollect_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

// namedSlack adds users.info to fakeSlack.
type namedSlack struct {
	*fakeSlack
	users   map[string]slack.User
	lookups map[string]int
}

func (n *namedSlack) User(_ context.Context, id string) (slack.User, error) {
	n.lookups[id]++
	u, ok := n.users[id]
	if !ok {
		return slack.User{}, errors.New("user_not_found")
	}
	return u, nil
}

// A DM carries its sender's name, mentions become @Name, a name is looked up
// once, and an unknown member keeps its id.
func TestSlackEventsCarryMemberNames(t *testing.T) {
	api := &namedSlack{fakeSlack: newFakeSlack(im("D1")), lookups: map[string]int{},
		users: map[string]slack.User{"U2": {ID: "U2", Name: "ana", RealName: "Ana Smith"}, "U7": {ID: "U7", Name: "bo"}}}
	env := newSlackEnvAPI(t, api, 40)
	at := collectT0.Add(30 * time.Second)
	api.post("D1", msg("U2", slackTS(at, 1), "ask <@U7> and <@U9|x> about it"))
	api.post("D1", msg("U2", slackTS(at, 2), "second"))
	env.at(16 * time.Minute)
	evs := env.poll(t, nil)
	if len(evs) != 2 {
		t.Fatalf("%d events, want 2", len(evs))
	}
	for _, ev := range evs {
		if ev.ActorName != "Ana Smith" || ev.Actor != "U2" {
			t.Errorf("actor %q name %q; want U2 named Ana Smith", ev.Actor, ev.ActorName)
		}
		if strings.Contains(ev.Body, "ask") && !strings.Contains(ev.Body, "ask @bo and <@U9|x>") {
			t.Errorf("body %q; want @bo and the unknown mention kept", ev.Body)
		}
	}
	if api.lookups["U2"] != 1 || api.lookups["U7"] != 1 || api.lookups["U9"] != 1 {
		t.Errorf("lookups = %v; want one each", api.lookups)
	}
}
