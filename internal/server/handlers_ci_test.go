package server

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

type noGitHub struct{}

func (noGitHub) PRView(context.Context, string) (ciwatch.PRView, error) {
	return ciwatch.PRView{}, ciwatch.ErrNoPR
}
func (noGitHub) FailedJobLog(context.Context, string, int64) (string, error) { return "", nil }
func (noGitHub) ReviewComments(context.Context, string, int) ([]ciwatch.ReviewComment, error) {
	return nil, nil
}
func (noGitHub) ViewerLogin(context.Context) (string, error) { return "me", nil }

// ciServer is a serve instance with a watcher over its own repo and no GitHub.
func ciServer(t *testing.T) (*Server, *sqlite.Repository) {
	t.Helper()
	srv, _, _ := todoServer(t, domain.Session{ID: "s1", WorktreePath: "/w/s1", Mode: domain.SessionModeAutonomous})
	repo := srv.repo.(*sqlite.Repository)
	srv.SetCIWatcher(ciwatch.New(ciwatch.Deps{
		Sessions: repo,
		GitHub:   noGitHub{},
		Todos:    srv.TodoAccess(),
		Steer:    func(string, domain.TodoAuthor, string) {},
		State:    repo,
		Now:      func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
	}))
	return srv, repo
}

type ciResult struct {
	Type string `json:"type"`
	CI   CIInfo `json:"ci"`
}

func TestCIWatchHandlerPersistsFlag(t *testing.T) {
	srv, repo := ciServer(t)
	var res ciResult
	rpcOK(t, srv, "", "ci.watch", map[string]any{"session": "s1", "on": false}, &res)
	got, err := repo.Get(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.WatchPR != domain.WatchPROff || res.Type != "ci" || res.CI.Watch {
		t.Fatalf("off: watch_pr=%q result %+v", got.WatchPR, res)
	}
	rpcOK(t, srv, "", "ci.watch", map[string]any{"session": "s1", "on": true}, &res)
	got, _ = repo.Get(context.Background(), "s1")
	if got.WatchPR != domain.WatchPROn || !res.CI.Watch {
		t.Fatalf("on: watch_pr=%q result %+v", got.WatchPR, res)
	}
	rpcOK(t, srv, "", "ci.get", map[string]any{"session": "s1"}, &res)
	if !res.CI.Watch {
		t.Fatalf("ci.get %+v", res)
	}
}

func TestCIWatchRequiresOn(t *testing.T) {
	srv, _ := ciServer(t)
	resp := rpc(t, srv, "", "ci.watch", map[string]any{"session": "s1"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("resp %+v", resp)
	}
}

func TestCIGetWithoutWatcher(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	for _, method := range []string{"ci.get", "ci.watch"} {
		resp := rpc(t, srv, "", method, map[string]any{"session": "s1", "on": true})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams || resp.Error.Message != "ci watcher not running" {
			t.Fatalf("%s: %+v", method, resp.Error)
		}
	}
}

func TestForgetClearsCIState(t *testing.T) {
	srv, repo := ciServer(t)
	ctx := context.Background()
	if err := repo.SavePRWatch(ctx, domain.PRWatchState{SessionID: "s1", PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordCheckFailure(ctx, "s1", "lint", "a"); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, srv, "", "session.forget", map[string]any{"id": "s1"}, nil)
	st, err := repo.LoadPRWatch(ctx, "s1")
	if err != nil || st.PRNumber != 0 {
		t.Fatalf("state %+v %v", st, err)
	}
	if n, _ := repo.RecordCheckFailure(ctx, "s1", "lint", "a"); n != 1 {
		t.Fatalf("failures survived forget: %d", n)
	}
}
