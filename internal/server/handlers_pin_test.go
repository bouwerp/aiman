package server

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func pinServer(t *testing.T) (*Server, interface {
	Get(context.Context, string) (*domain.Session, error)
}) {
	t.Helper()
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "id-1", Name: "alpha", Group: "g", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(nil, repo, nil, nil, nil, nil, "t")
	return srv, repo
}

func TestSessionPinRefusesSessionCaller(t *testing.T) {
	srv, repo := pinServer(t)
	resp := rpc(t, srv, "id-1", "session.pin", map[string]any{"id": "alpha", "pinned": true})
	if resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Fatalf("session caller got %+v, want needs_operator", resp.Error)
	}
	if got, _ := repo.Get(context.Background(), "id-1"); got.Pinned {
		t.Fatal("refused call pinned the session")
	}
}

func TestSessionPinSetsAndClears(t *testing.T) {
	srv, repo := pinServer(t)
	var out struct {
		Type    string      `json:"type"`
		Session SessionInfo `json:"session"`
	}
	rpcOK(t, srv, "", "session.pin", map[string]any{"id": "alpha", "pinned": true}, &out)
	if out.Type != "session" || out.Session.Pinned == nil || !*out.Session.Pinned {
		t.Fatalf("pin reply = %+v", out)
	}
	if got, _ := repo.Get(context.Background(), "id-1"); !got.Pinned {
		t.Fatal("pin not stored")
	}
	var list struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	rpcOK(t, srv, "", "session.list", map[string]any{}, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Pinned == nil || !*list.Sessions[0].Pinned {
		t.Fatalf("session.list = %+v, want the pin", list.Sessions)
	}

	rpcOK(t, srv, "", "session.pin", map[string]any{"id": "alpha", "pinned": false}, &out)
	if got, _ := repo.Get(context.Background(), "id-1"); got.Pinned {
		t.Fatal("unpin not stored")
	}
	if resp := rpc(t, srv, "", "session.pin", map[string]any{"id": "alpha"}); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("missing pinned got %+v, want invalid_params", resp.Error)
	}
}

func TestCreatedEventsAnnouncePinChange(t *testing.T) {
	announced := map[string]string{}
	s := domain.Session{ID: "a", Name: "alpha"}
	if got := collectRepoCreatedEvents([]domain.Session{s}, announced); len(got) != 1 {
		t.Fatalf("first announce = %d events", len(got))
	}
	s.Pinned = true
	got := collectRepoCreatedEvents([]domain.Session{s}, announced)
	if len(got) != 1 || got[0].Session.Pinned == nil || !*got[0].Session.Pinned {
		t.Fatalf("pin change announced %+v", got)
	}
}
