package ciwatch

import (
	"testing"
)

// trustLogins rebuilds the rig's watcher through New with the given trusted
// logins, so any normalisation New applies to Deps is exercised.
func (r *rig) trustLogins(t *testing.T, logins []string) {
	t.Helper()
	d := r.w.d
	d.TrustedLogins = logins
	r.w = New(d)
}

func TestTrustedLoginCommentBecomesTodo(t *testing.T) {
	r := newRig(t)
	r.trustLogins(t, []string{"review-bot"})
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 31, Body: "Nil check missing", Path: "a.go", Line: 4, Author: "Review-Bot", Association: "NONE", CreatedAt: r.now}}
	r.poll(t, testSession())

	items := r.todos.items()
	if len(items) != 1 || items[0].Ref != ReviewRef(31) || items[0].Ref != "review:31" {
		t.Fatalf("todos %+v", items)
	}
	if events := r.ignoredEvents(t); len(events) != 0 {
		t.Fatalf("trusted login recorded as ignored: %+v", events)
	}
}

func TestUntrustedLoginStillIgnored(t *testing.T) {
	r := newRig(t)
	r.trustLogins(t, []string{"review-bot"})
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 32, Body: "x", Path: "a.go", Line: 1, Author: "stranger", Association: "NONE", CreatedAt: r.now}}
	r.poll(t, testSession())
	r.poll(t, testSession())

	if len(r.todos.items()) != 0 || len(r.steers.all()) != 0 {
		t.Fatalf("todos %+v steers %q", r.todos.items(), r.steers.all())
	}
	if events := r.ignoredEvents(t); len(events) != 1 {
		t.Fatalf("events %+v", events)
	}
}

func TestBotIgnoredWithoutTrustedLogins(t *testing.T) {
	r := newRig(t)
	r.trustLogins(t, nil)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 33, Body: "x", Path: "a.go", Line: 1, Author: "Review-Bot", Association: "NONE", CreatedAt: r.now}}
	r.poll(t, testSession())

	if len(r.todos.items()) != 0 || len(r.steers.all()) != 0 {
		t.Fatalf("todos %+v steers %q", r.todos.items(), r.steers.all())
	}
	if events := r.ignoredEvents(t); len(events) != 1 {
		t.Fatalf("events %+v", events)
	}
}
