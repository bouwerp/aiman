package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

func reportAgentSession(t *testing.T, srv *Server, params map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": "r1", "method": "session.report_agent_session", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if resp := srv.dispatch(context.Background(), raw); resp.Error != nil {
		t.Fatalf("report_agent_session: %+v", resp.Error)
	}
}

func TestLiveSessionsCarriesRoleAndParent(t *testing.T) {
	srv, pty, _ := ptyServer(t)
	pty.Add(ptyruntime.SessionInfo{ID: "h1", Name: "handover-ab12", Role: "handover", ParentID: "p"})
	live := srv.liveSessions(context.Background())
	if len(live) != 1 || live[0].Role != domain.SessionRoleHandover || live[0].ParentID != "p" {
		t.Fatalf("live = %+v, want role handover and parent p", live)
	}
}

// A visible child reporting before its row is saved is stored with the
// parent the holder recorded.
func TestReportForLiveChildStoresParent(t *testing.T) {
	srv, pty, _ := ptyServer(t)
	pty.Add(ptyruntime.SessionInfo{ID: "c1", Name: "child-term", ParentID: "p"})
	reportAgentSession(t, srv, map[string]any{"id": "c1", "state": "idle", "source": "lifecycle"})
	got, err := srv.repo.Get(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "p" {
		t.Fatalf("ParentID = %q, want p", got.ParentID)
	}
}

// A hidden helper's first hook report can beat its creator's save. It must
// not create the row: the creator's save stays the first identity write.
func TestReportForUnsavedHiddenHelperDoesNotSave(t *testing.T) {
	repo, err := sqlite.NewRepository(filepath.Join(shortTempDir(t), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	srv, pty, _ := ptyServer(t)
	srv.repo = repo
	pty.Add(ptyruntime.SessionInfo{ID: "h1", Name: "handover-ab12", Role: "handover", ParentID: "p"})
	reportAgentSession(t, srv, map[string]any{"id": "h1", "state": "idle", "source": "lifecycle"})
	if got, gerr := repo.Get(context.Background(), "h1"); gerr == nil {
		t.Fatalf("helper row saved before its creator: %+v", got)
	}
	events, err := repo.ListSessionEvents(context.Background(), "h1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != domain.EventHookReported {
		t.Fatalf("ledger = %+v, want one hook.reported", events)
	}
}

func TestCollectRepoCreatedEventsReannouncesIdentityChange(t *testing.T) {
	announced := map[string]string{}
	first := collectRepoCreatedEvents([]domain.Session{{ID: "h1", Name: "handover-ab12"}}, announced)
	if len(first) != 1 {
		t.Fatalf("first = %+v", first)
	}
	fixed := []domain.Session{{ID: "h1", Name: "handover", Role: domain.SessionRoleHandover, ParentID: "p"}}
	second := collectRepoCreatedEvents(fixed, announced)
	if len(second) != 1 || second[0].Type != "session_created" || second[0].Session.Role != "handover" || second[0].Session.ParentID != "p" {
		t.Fatalf("second = %+v, want a session_created carrying the role", second)
	}
	if again := collectRepoCreatedEvents(fixed, announced); len(again) != 0 {
		t.Fatalf("unchanged row re-announced: %+v", again)
	}
}

func TestCollectRepoCreatedEventsHiddenFirstEventCarriesRole(t *testing.T) {
	got := collectRepoCreatedEvents([]domain.Session{{ID: "h1", Name: "handover", Role: domain.SessionRoleConsult, ParentID: "p"}}, map[string]string{})
	if len(got) != 1 || got[0].Session == nil || got[0].Session.Role != string(domain.SessionRoleConsult) {
		t.Fatalf("got %+v, want role consult on the first event", got)
	}
}
