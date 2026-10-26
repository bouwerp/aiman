package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
)

func TestUsageGetMethod(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	for _, sess := range []domain.Session{
		{ID: "s1", Name: "fix-login", Group: "work", TmuxSession: "s1", Status: domain.SessionStatusActive},
		{ID: "s2", Name: "fix-login", Group: "other", TmuxSession: "s2", Status: domain.SessionStatusActive},
		{ID: "s3", Name: "only-name", Group: "work", TmuxSession: "s3", Status: domain.SessionStatusActive},
	} {
		if err := repo.Save(ctx, &sess); err != nil {
			t.Fatal(err)
		}
	}
	hour := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	store := usageFixture{
		metas: []domain.UsageSessionMeta{
			{SessionID: "s1", Name: "fix-login", Group: "work", IssueKey: "PROJ-1"},
			{SessionID: "s2", Name: "fix-login", Group: "other", IssueKey: "PROJ-2"},
		},
		buckets: []domain.UsageBucket{
			{SessionID: "s1", Hour: hour, Model: "claude-haiku-4-5", Tokens: domain.TokenCounts{Input: 1_000_000}},
			{SessionID: "s2", Hour: hour, Model: "claude-haiku-4-5", Tokens: domain.TokenCounts{Input: 2_000_000}},
		},
	}
	srv := New(nil, repo, nil, nil, nil, nil, "test")

	t.Run("disabled", func(t *testing.T) {
		resp := rpc(t, srv, "", "usage.get", map[string]any{"session": "s1"})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams || resp.Error.Message != "usage tracking is not running on this host" {
			t.Fatalf("%+v", resp.Error)
		}
	})

	srv.EnableUsage(context.Background(), nil, usage.Reporter{
		Store:  store,
		Prices: usage.DefaultPrices(),
		Now:    func() time.Time { return now },
	})

	t.Run("name", func(t *testing.T) {
		res := usageGet(t, srv, map[string]any{
			"session": "only-name", "by": "model", "days": 7, "tz_offset_minutes": 120,
		})
		if res.Type != "usage" || res.Report.Known || res.Report.Total.CostUSD != 0 || len(res.Report.Rows) != 0 {
			t.Fatalf("%+v", res)
		}
	})

	t.Run("group and name", func(t *testing.T) {
		res := usageGet(t, srv, map[string]any{
			"session": "work/fix-login", "by": "model", "days": 7, "tz_offset_minutes": 120,
		})
		if res.Type != "usage" || res.Report.Total.CostUSD != 1 || len(res.Report.Rows) != 1 || res.Report.Rows[0].Key != "claude-haiku-4-5" || !res.Report.Known {
			t.Fatalf("%+v", res)
		}
		other := usageGet(t, srv, map[string]any{"session": "other/fix-login", "by": "session"})
		if other.Report.Total.CostUSD != 2 || len(other.Report.Rows) != 1 || other.Report.Rows[0].Key != "s2" {
			t.Fatalf("%+v", other.Report)
		}
	})

	t.Run("id", func(t *testing.T) {
		res := usageGet(t, srv, map[string]any{"session": "s1"})
		if res.Report.Total.CostUSD != 1 || len(res.Report.Rows) != 0 {
			t.Fatalf("%+v", res.Report)
		}
	})

	t.Run("all sessions", func(t *testing.T) {
		res := usageGet(t, srv, map[string]any{"by": "issue", "days": 7})
		if res.Report.Total.CostUSD != 3 || len(res.Report.Rows) != 2 {
			t.Fatalf("%+v", res.Report)
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		resp := rpc(t, srv, "", "usage.get", map[string]any{"session": "nope"})
		if resp.Error == nil || resp.Error.Code != CodeNotFound {
			t.Fatalf("%+v", resp.Error)
		}
	})

	t.Run("bad by", func(t *testing.T) {
		resp := rpc(t, srv, "", "usage.get", map[string]any{"by": "week"})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("%+v", resp.Error)
		}
	})

	t.Run("store error", func(t *testing.T) {
		broken := New(nil, repo, nil, nil, nil, nil, "test")
		broken.EnableUsage(context.Background(), nil, usage.Reporter{Store: usageFixture{err: errors.New("db down")}})
		resp := rpc(t, broken, "", "usage.get", map[string]any{"session": "s1"})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("%+v", resp.Error)
		}
	})
}

func TestPublishUsageEvent(t *testing.T) {
	srv := New(nil, nil, nil, nil, nil, nil, "test")
	ch, stop := srv.hub.Subscribe()
	defer stop()
	srv.PublishUsage("s1")
	select {
	case ev := <-ch:
		if ev.Type != "usage_changed" || ev.ID != "s1" {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no usage_changed event")
	}
}

func TestReportAgentSessionKicksUsage(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "s1", Name: "fix-login", Group: "work", TmuxSession: "s1",
		Status: domain.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	lister := &blockingLister{
		release: make(chan struct{}),
		path:    filepath.Join(t.TempDir(), ".claude", "projects", "repo", "missing.jsonl"),
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		close(lister.release)
	}()
	srv := New(nil, repo, nil, nil, nil, nil, "test")
	srv.EnableUsage(runCtx, usage.NewTracker(usageFixture{}, lister, srv.PublishUsage), usage.Reporter{})
	waitLister(t, lister, 1)

	ghost := reportAgent(t, srv, map[string]any{"id": "ghost", "agent_session_id": "n1"})
	if ghost.Error != nil {
		t.Fatalf("unknown session: %+v", ghost.Error)
	}
	if got := lister.count(); got != 1 {
		t.Fatalf("scans after unknown session = %d, want 1", got)
	}

	done := make(chan Response, 1)
	go func() {
		done <- reportAgent(t, srv, map[string]any{
			"id": "s1", "agent_session_id": "native-1", "state": "idle",
		})
	}()
	select {
	case resp := <-done:
		if resp.Error != nil {
			t.Fatalf("per-file tracker error failed the hook: %+v", resp.Error)
		}
		raw, _ := json.Marshal(resp.Result)
		var got struct {
			AgentSessionID string `json:"agent_session_id"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.AgentSessionID != "native-1" {
			t.Fatalf("result = %s", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("hook report blocked on the usage tracker")
	}
	waitLister(t, lister, 2)
}

func usageGet(t *testing.T, srv *Server, params map[string]any) usageGetResult {
	t.Helper()
	var res usageGetResult
	rpcOK(t, srv, "", "usage.get", params, &res)
	return res
}

func reportAgent(t *testing.T, srv *Server, params map[string]any) Response {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return srv.handleReportAgentSession(context.Background(), Request{
		ID: "1", Method: "session.report_agent_session", Params: b,
	})
}

func waitLister(t *testing.T, l *blockingLister, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.count() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scans = %d, want >= %d", l.count(), n)
}

type usageGetResult struct {
	Type   string       `json:"type"`
	Report usage.Report `json:"report"`
}

type usageFixture struct {
	metas   []domain.UsageSessionMeta
	buckets []domain.UsageBucket
	err     error
}

func (f usageFixture) UsageCursors(context.Context, string) ([]domain.UsageCursor, error) {
	if f.err != nil {
		return nil, f.err
	}
	return nil, nil
}

func (f usageFixture) ApplyUsage(context.Context, domain.UsageCursor, []domain.UsageRecord) (bool, error) {
	return false, f.err
}

func (f usageFixture) UpsertUsageSession(context.Context, domain.UsageSessionMeta) error {
	return f.err
}

func (f usageFixture) UsageSessions(context.Context) ([]domain.UsageSessionMeta, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.metas, nil
}

func (f usageFixture) UsageBuckets(_ context.Context, ids []string, _ time.Time) ([]domain.UsageBucket, error) {
	if f.err != nil {
		return nil, f.err
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []domain.UsageBucket
	for _, b := range f.buckets {
		if _, ok := want[b.SessionID]; ok {
			out = append(out, b)
		}
	}
	return out, nil
}

// blockingLister counts scans. The first List returns immediately. Later lists
// wait, so a synchronous kick cannot hide inside the hook report.
type blockingLister struct {
	mu      sync.Mutex
	n       int
	release chan struct{}
	path    string
}

func (l *blockingLister) List(ctx context.Context) ([]domain.Session, error) {
	l.mu.Lock()
	l.n++
	n := l.n
	l.mu.Unlock()
	if n > 1 {
		select {
		case <-l.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []domain.Session{{ID: "s1", AgentSessionPath: l.path}}, nil
}

func (l *blockingLister) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}
