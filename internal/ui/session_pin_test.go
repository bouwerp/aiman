package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// pinRepo records SetPinned on top of the mock repository.
type pinRepo struct {
	mockSessionRepo
	pins map[string]bool
}

func (r *pinRepo) SetPinned(_ context.Context, id string, pinned bool) error {
	if r.pins == nil {
		r.pins = map[string]bool{}
	}
	r.pins[id] = pinned
	return nil
}

func TestGroupedSessionItemsPinnedFirst(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "1", Name: "impl", Group: "alpha"}, remoteName: "r"},
		{session: domain.Session{ID: "2", Name: "child", Group: "alpha", ParentID: "1", Pinned: true}, remoteName: "r"},
		{session: domain.Session{ID: "3", Name: "grandchild", Group: "alpha", ParentID: "2"}, remoteName: "r"},
		{session: domain.Session{ID: "4", Name: "solo", Group: "pinned", Pinned: true}, remoteName: "r"},
		{session: domain.Session{ID: "5", Name: "other", Group: "pinned"}, remoteName: "r"},
	}
	got := groupedSessionItems(flat, nil, nil)
	var names []string
	for _, li := range got {
		it := li.(item)
		switch {
		case it.header && it.pinned:
			names = append(names, "[pinned]")
		case it.header:
			names = append(names, "["+it.session.Group+"]")
		default:
			names = append(names, it.session.Name)
		}
	}
	want := []string{"[pinned]", "child", "grandchild", "solo", "[alpha]", "impl", "[pinned]", "other"}
	if len(names) != len(want) {
		t.Fatalf("rows %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("rows %v, want %v", names, want)
		}
	}
	h := got[0].(item)
	if h.headerKey() != pinnedBucketKey || got[6].(item).headerKey() == pinnedBucketKey {
		t.Fatal("the Pinned section shares a key with a group called pinned")
	}
	if title := h.Title(); !strings.HasPrefix(title, "▾ "+pinnedHeaderLabel+" · 3") {
		t.Fatalf("pinned header title %q", title)
	}
	// Collapsing the Pinned section leaves the group called pinned open.
	got = groupedSessionItems(flat, map[string]bool{pinnedBucketKey: true}, nil)
	if len(got) != 5 {
		t.Fatalf("collapsed pinned: %d rows, want 5", len(got))
	}
}

func TestPinKeyTellsServeAndStoresPin(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	repo := &pinRepo{}
	s := domain.Session{ID: "s1", Name: "impl", Group: "g", RemoteHost: "10.0.1.9"}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{s}, repo, nil, nil, nil)
	m.applyRemoteFilter()
	if !selectSessionByID(&m.list, m.list.Items(), "s1") {
		t.Fatal("session not listed")
	}
	cmd := m.toggleSessionPin("s1")
	if !m.allSessions[0].Pinned || !repo.pins["s1"] {
		t.Fatalf("pin not applied: mem %v db %v", m.allSessions[0].Pinned, repo.pins)
	}
	if first := m.list.Items()[0].(item); !first.header || !first.pinned {
		t.Fatalf("first row %+v, want the Pinned header", first)
	}
	if sel, ok := m.selectedSessionItem(); !ok || sel.session.ID != "s1" {
		t.Fatal("selection left the pinned session")
	}
	runIdentityCmd(t, m, cmd)
	if len(*calls) != 1 {
		t.Fatalf("calls %+v", *calls)
	}
	c := (*calls)[0]
	if c.method != "session.pin" || c.params["id"] != "s1" || c.params["pinned"] != true {
		t.Fatalf("call %+v", c)
	}
	_ = m.toggleSessionPin("s1")
	if m.allSessions[0].Pinned || repo.pins["s1"] {
		t.Fatal("unpin not applied")
	}
}

func TestServeAnnouncementCarriesPin(t *testing.T) {
	repo := &pinRepo{}
	s := domain.Session{ID: "s1", Name: "impl", Group: "g", RemoteHost: "10.0.1.9", Pinned: true}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{s}, repo, nil, nil, nil)
	if m.streams == nil {
		m.streams = map[string]*sessionStream{}
	}
	m.streams["10.0.1.9"] = &sessionStream{host: "10.0.1.9", events: make(chan server.SessionEvent), todoSwept: true}
	// A scan knows no pin and keeps the stored one.
	m.upsertLiveSession(domain.Session{ID: "s1", RemoteHost: "10.0.1.9"})
	if !m.allSessions[0].Pinned {
		t.Fatal("scan cleared the pin")
	}
	// An older serve sends no pin.
	info := server.SessionInfo{ID: "s1", Name: "impl", Group: "g"}
	_ = m.applySessionEvent(sessionEventMsg{host: "10.0.1.9", event: server.SessionEvent{Type: "session_created", ID: "s1", Session: &info}})
	if !m.allSessions[0].Pinned {
		t.Fatal("an announcement without a pin cleared it")
	}
	off := false
	info.Pinned = &off
	_ = m.applySessionEvent(sessionEventMsg{host: "10.0.1.9", event: server.SessionEvent{Type: "session_created", ID: "s1", Session: &info}})
	if m.allSessions[0].Pinned {
		t.Fatal("serve's unpin did not land")
	}
	if p, ok := repo.pins["s1"]; !ok || p {
		t.Fatalf("laptop DB pin %v, want false", repo.pins)
	}
}
