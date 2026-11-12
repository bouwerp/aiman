package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
)

func fakeScope(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// resourcesRig is a server whose sampler reads a fake cgroup tree holding a
// scope for session "a".
func resourcesRig(t *testing.T) (*Server, *[][]string) {
	t.Helper()
	srv, opts := resourceServer(t, rulesRow("a", ""), rulesRow("b", ""))
	var calls [][]string
	sd := cgroup.NewSystemd(filepath.Join(t.TempDir(), "user-1.slice", "user@1.service"),
		func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return nil, nil
		})
	fakeScope(t, sd.UserSliceDir(), map[string]string{"memory.current": "100\n", "memory.high": "25769803776\n"})
	fakeScope(t, sd.ScopeDir("a"), map[string]string{
		"memory.current": "5368709120\n", "memory.high": "6442450944\n", "cgroup.procs": "1\n", "pids.current": "7\n",
	})
	opts.Systemd, opts.SliceMemoryHigh = sd, 20<<30
	srv.SetResources(opts)
	sampler := cgroup.NewSampler(sd)
	sampler.Sessions = srv.resourceSessions
	sampler.Sample(context.Background(), time.Now())
	srv.sampler.Store(sampler)
	return srv, &calls
}

func resourcesOf(t *testing.T, resp Response) ResourcesReport {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("resources.get: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var body struct {
		Type      string          `json:"type"`
		Resources ResourcesReport `json:"resources"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Type != "resources" {
		t.Fatalf("result %s: %v", raw, err)
	}
	return body.Resources
}

func TestResourcesGetReportsSessionsSliceAndHost(t *testing.T) {
	srv, _ := resourcesRig(t)
	r := resourcesOf(t, rulesCall(t, srv, "", "resources.get", nil))
	if !r.Supported || len(r.Sessions) != 1 || r.Sessions[0].ID != "a" || r.Sessions[0].Name == "" {
		t.Fatalf("sessions: %+v", r)
	}
	if u := r.Sessions[0]; u.MemoryCurrent != 5<<30 || u.MemoryHigh != 6<<30 || u.Pids != 7 {
		t.Fatalf("usage: %+v", u)
	}
	if r.UserSlice == nil || r.UserSlice.MemoryHigh != 24<<30 || r.SliceMemoryHigh != 20<<30 || !r.Tmpdir {
		t.Fatalf("totals: %+v", r)
	}
	if r.Limits.MemoryHigh != 6<<30 {
		t.Fatalf("limits: %+v", r.Limits)
	}

	// One session, by name, also from a session caller; a helper id maps
	// to its session.
	for _, target := range []string{"a", "a-run"} {
		one := resourcesOf(t, rulesCall(t, srv, "b", "resources.get", map[string]any{"session": target}))
		if len(one.Sessions) != 1 || one.Sessions[0].ID != "a" || one.Missing != "" {
			t.Fatalf("session %s: %+v", target, one)
		}
	}
	// A session without a scope says so.
	none := resourcesOf(t, rulesCall(t, srv, "", "resources.get", map[string]any{"session": "b"}))
	if len(none.Sessions) != 0 || none.Missing != "b" {
		t.Fatalf("unscoped session: %+v", none)
	}
}

func TestResourcesGetUnsupported(t *testing.T) {
	srv, _ := resourceServer(t, rulesRow("a", ""))
	srv.RunResources(context.Background()) // no systemd: returns at once
	r := resourcesOf(t, rulesCall(t, srv, "", "resources.get", nil))
	if r.Supported || r.Sessions == nil || len(r.Sessions) != 0 {
		t.Fatalf("unsupported host: %+v", r)
	}
}

func TestSessionLimitsStoresAndAppliesForTheOperatorOnly(t *testing.T) {
	srv, calls := resourcesRig(t)
	if resp := rulesCall(t, srv, "b", "session.limits", map[string]any{"session": "a", "memory_high": "12G"}); resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Fatalf("session caller: %+v", resp)
	}
	if resp := rulesCall(t, srv, "", "session.limits", map[string]any{"session": "a", "memory_high": "lots"}); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("bad size: %+v", resp)
	}
	if resp := rulesCall(t, srv, "", "session.limits", map[string]any{"session": "a", "memory_high": "12G", "memory_max": "8G"}); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("max below high: %+v", resp)
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused call reached systemd: %v", *calls)
	}

	resp := rulesCall(t, srv, "", "session.limits", map[string]any{"session": "a", "memory_high": "12G"})
	if resp.Error != nil {
		t.Fatalf("session.limits: %+v", resp.Error)
	}
	got := resp.Result.(map[string]any)
	if got["applied"] != true || got["scoped"] != true {
		t.Fatalf("result: %+v", got)
	}
	want := "systemctl --user set-property --runtime aiman-session-a.scope MemoryHigh=12884901888 MemoryMax=12884901888 TasksMax=4096"
	if len(*calls) != 1 || strings.Join((*calls)[0], " ") != want {
		t.Fatalf("systemd calls: %v\nwant %s", *calls, want)
	}
	if srv.resourceOptions().Overrides.Get("a").MemoryHigh != 12<<30 {
		t.Fatal("override not stored")
	}

	// No scope: stored for the next start, not applied. No value: a read,
	// which a session may make.
	resp = rulesCall(t, srv, "", "session.limits", map[string]any{"session": "b", "memory_max": "infinity"})
	if got := resp.Result.(map[string]any); resp.Error != nil || got["applied"] != false {
		t.Fatalf("unscoped: %+v %+v", resp.Error, got)
	}
	resp = rulesCall(t, srv, "b", "session.limits", map[string]any{"session": "a"})
	if resp.Error != nil || resp.Result.(map[string]any)["limits"].(cgroup.Limits).MemoryHigh != 12<<30 {
		t.Fatalf("read: %+v", resp)
	}
}

func TestCreateLimitsOperatorOnly(t *testing.T) {
	srv, opts := resourceServer(t, rulesRow("a", ""))
	req := Request{ID: "1", Caller: "a"}
	if r := srv.createLimits(context.Background(), req, limitParams{MemoryHigh: "12G"}, "new"); r == nil || r.Error.Code != CodeNeedsOperator {
		t.Fatalf("session caller: %+v", r)
	}
	if r := srv.createLimits(context.Background(), Request{ID: "1"}, limitParams{MemoryHigh: "nope"}, "new"); r == nil || r.Error.Code != CodeInvalidParams {
		t.Fatalf("bad size: %+v", r)
	}
	if r := srv.createLimits(context.Background(), Request{ID: "1"}, limitParams{}, "new"); r != nil {
		t.Fatalf("no override refused: %+v", r)
	}
	if r := srv.createLimits(context.Background(), Request{ID: "1"}, limitParams{MemoryHigh: "12G", MemoryMax: "16G"}, "new"); r != nil {
		t.Fatalf("operator refused: %+v", r)
	}
	if got := opts.Overrides.Get("new"); got.MemoryHigh != 12<<30 || got.MemoryMax != 16<<30 {
		t.Fatalf("stored: %+v", got)
	}
}

func TestResourceAlertNotifiesAndSteers(t *testing.T) {
	srv, _, notes := todoServer(t, todoSession("a"))
	store := &notifyStore{}
	srv.EnableNotifications(notify.NewRecorder(store, notify.Settings{
		Events: map[domain.NotifyKind]bool{domain.NotifyResources: true}, Host: "h",
	}, nil), nil)
	ctx := context.Background()

	usage := cgroup.Usage{MemoryCurrent: 58 << 27, MemoryHigh: 6 << 30, MemoryPressure: 61}
	srv.raiseResourceAlert(ctx, cgroup.Alert{Scope: cgroup.AlertSession, SessionID: "a", SessionName: "a", Usage: usage, Throttled: true})
	srv.raiseResourceAlert(ctx, cgroup.Alert{Scope: cgroup.AlertUserSlice, Usage: cgroup.Usage{MemoryCurrent: 22 << 30, MemoryHigh: 24 << 30}})

	want := "[aiman] this session is at 7.2G/6G memory, pressure 61% and is being throttled; reduce parallelism. Do not retry the same job."
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for !found && time.Now().Before(deadline) {
		for _, n := range notes.records() {
			if n.SessionID == "a" && n.Author == domain.AuthorAiman && n.Text == want {
				found = true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("no steering note %q; notes %+v", want, notes.records())
	}
	if len(store.rows) != 2 {
		t.Fatalf("notifications: %+v", store.rows)
	}
	if n := store.rows[0]; n.SessionID != "a" || n.Kind != domain.NotifyResources || !strings.Contains(n.Text, "7.2G/6G memory, pressure 61%, throttled") {
		t.Fatalf("session notification: %+v", n)
	}
	if n := store.rows[1]; n.SessionID != "" || n.Text != "on h this host's user slice is at 22G/24G memory: the whole host slows down past the limit" {
		t.Fatalf("slice notification: %+v", n)
	}
}
