//go:build acceptance

package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

const (
	hostUpdateSelf   = "v0.34.0"
	hostUpdateNext   = "v0.35.0"
	hostUpdateName   = "remote1"
	hostUpdatePeer   = 5151
	hostUpdateCaller = "sess-host-update"
)

// hostFetcher always answers with one ready release, newer than the server.
type hostFetcher struct{}

func (hostFetcher) Latest(context.Context, string) (selfupdate.Release, bool, error) {
	return selfupdate.Release{Tag: hostUpdateNext, Ready: true, ETag: `"e1"`}, true, nil
}

// hostRunner records the tags it was asked to install and starts nothing.
type hostRunner struct {
	mu   sync.Mutex
	tags []string
}

func (r *hostRunner) Start(tag, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tags = append(r.tags, tag)
	return nil
}

func (r *hostRunner) started() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tags...)
}

func hostUpdateManager(t *testing.T, mode selfupdate.Mode) (*selfupdate.Manager, *hostRunner) {
	t.Helper()
	dir := t.TempDir()
	run := &hostRunner{}
	now := time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)
	return &selfupdate.Manager{
		Version:    hostUpdateSelf,
		Fetcher:    hostFetcher{},
		Runner:     run,
		Script:     func(string) string { return "true\n" },
		StatePath:  filepath.Join(dir, "host-update.json"),
		ResultPath: filepath.Join(dir, "host-update.result.json"),
		Policy:     selfupdate.Policy{Mode: mode, CheckEvery: 10 * time.Minute, RetryAfter: time.Hour},
		Logf:       t.Logf,
		Now:        func() time.Time { return now },
	}, run
}

func hostUpdateServer(t *testing.T) *Server {
	t.Helper()
	srv := New(nil, testRepo(t), nil, nil, nil, nil, hostUpdateSelf)
	srv.procInSession = func(pid int) bool { return pid == hostUpdatePeer }
	return srv
}

type hostVersionResult struct {
	Type        string          `json:"type"`
	Host        string          `json:"host"`
	Version     string          `json:"version"`
	Protocol    int             `json:"protocol"`
	Latest      string          `json:"latest"`
	LatestReady bool            `json:"latest_ready"`
	Mode        string          `json:"mode"`
	Status      string          `json:"status"`
	CheckedAt   string          `json:"checked_at"`
	Attempt     json.RawMessage `json:"attempt"`
}

type hostUpdateResult struct {
	Type     string `json:"type"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Target   string `json:"target"`
}

func hostVersion(t *testing.T, srv *Server, caller string) hostVersionResult {
	t.Helper()
	var got hostVersionResult
	rpcOK(t, srv, caller, "host.version", map[string]any{}, &got)
	if got.Type != "host_version" {
		t.Fatalf("type = %q, want host_version", got.Type)
	}
	return got
}

// sessionPeerCall dispatches as a process inside a holder with no caller id.
func sessionPeerCall(t *testing.T, srv *Server, method string) Response {
	t.Helper()
	line, err := json.Marshal(Request{ID: "1", Method: method, Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return srv.dispatch(withPeerPID(context.Background(), hostUpdatePeer, true), line)
}

func TestHostVersionWithoutManagerIsHeld(t *testing.T) {
	srv := hostUpdateServer(t)
	got := hostVersion(t, srv, "")
	if got.Mode != "off" || got.Status != "held" || got.Protocol != 1 || got.Latest != "" {
		t.Fatalf("host.version = %+v, want mode off, status held, protocol 1, no latest", got)
	}
	if got.Version != hostUpdateSelf {
		t.Fatalf("version = %q, want the server's %q", got.Version, hostUpdateSelf)
	}
	if got.Protocol != ProtocolVersion {
		t.Fatalf("protocol = %d, want ProtocolVersion %d", got.Protocol, ProtocolVersion)
	}
}

func TestHostUpdateWithoutManagerIsInvalidParams(t *testing.T) {
	srv := hostUpdateServer(t)
	wantCode(t, rpc(t, srv, "", "host.update", map[string]any{}), CodeInvalidParams)
}

func TestHostVersionReflectsManagerStatus(t *testing.T) {
	srv := hostUpdateServer(t)
	m, run := hostUpdateManager(t, selfupdate.ModeNotify)
	m.Poll(context.Background())
	srv.SetHostUpdates(m, hostUpdateName)

	want := m.Status()
	got := hostVersion(t, srv, "")
	if got.Host != hostUpdateName {
		t.Fatalf("host = %q, want %q", got.Host, hostUpdateName)
	}
	if got.Version != want.Version || got.Latest != want.Latest || got.LatestReady != want.LatestReady ||
		got.Mode != string(want.Mode) || got.Status != want.Status || got.Protocol != 1 {
		t.Fatalf("host.version = %+v, want fields of %+v", got, want)
	}
	if want.Status != selfupdate.StatusAvailable || got.Latest != hostUpdateNext || !got.LatestReady {
		t.Fatalf("notify mode with a ready newer release: %+v", got)
	}
	wantChecked := ""
	if !want.CheckedAt.IsZero() {
		wantChecked = want.CheckedAt.Format(time.RFC3339)
	}
	if got.CheckedAt != wantChecked {
		if ts, err := time.Parse(time.RFC3339, got.CheckedAt); err != nil || !ts.Equal(want.CheckedAt) {
			t.Fatalf("checked_at = %q, want %q", got.CheckedAt, wantChecked)
		}
	}
	if want.Attempt == nil && string(got.Attempt) != "null" && len(got.Attempt) != 0 {
		t.Fatalf("attempt = %s, want null", got.Attempt)
	}
	if n := len(run.started()); n != 0 {
		t.Fatalf("notify mode started %d installs", n)
	}
}

func TestHostVersionAnySessionMayRead(t *testing.T) {
	srv := hostUpdateServer(t)
	m, _ := hostUpdateManager(t, selfupdate.ModeNotify)
	srv.SetHostUpdates(m, hostUpdateName)
	if got := hostVersion(t, srv, hostUpdateCaller); got.Host != hostUpdateName {
		t.Fatalf("session read: %+v", got)
	}
	if resp := sessionPeerCall(t, srv, "host.version"); resp.Error != nil {
		t.Fatalf("holder peer read: %+v", resp.Error)
	}
}

func TestHostUpdateSessionCallerNeedsOperator(t *testing.T) {
	srv := hostUpdateServer(t)
	m, run := hostUpdateManager(t, selfupdate.ModeAuto)
	srv.SetHostUpdates(m, hostUpdateName)

	wantCode(t, rpc(t, srv, hostUpdateCaller, "host.update", map[string]any{}), CodeNeedsOperator)
	wantCode(t, sessionPeerCall(t, srv, "host.update"), CodeNeedsOperator)
	time.Sleep(50 * time.Millisecond)
	if n := len(run.started()); n != 0 {
		t.Fatalf("a refused host.update started %d installs", n)
	}
}

func TestHostUpdateOperatorAccepted(t *testing.T) {
	srv := hostUpdateServer(t)
	m, run := hostUpdateManager(t, selfupdate.ModeAuto)
	srv.SetHostUpdates(m, hostUpdateName)

	var got hostUpdateResult
	rpcOK(t, srv, "", "host.update", map[string]any{}, &got)
	if got.Type != "host_update" || !got.Accepted || got.Target != hostUpdateNext {
		t.Fatalf("host.update = %+v, want accepted %s", got, hostUpdateNext)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(run.started()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if tags := run.started(); len(tags) != 1 || tags[0] != hostUpdateNext {
		t.Fatalf("runner tags = %v, want [%s]", tags, hostUpdateNext)
	}
}

// A long call counts as in flight for exactly as long as it runs.
func TestInflightCountsLongCall(t *testing.T) {
	t.Setenv("AIMAN_ID", "")
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	creator := &fakeCreator{onCreate: func() {
		once.Do(func() { close(entered) })
		<-release
	}}
	srv := New(ln, testRepo(t), &fakeRemote{}, creator, nil, nil, hostUpdateSelf)
	srv.procInSession = func(int) bool { return false }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx) }()

	if n := srv.Inflight(); n != 0 {
		t.Fatalf("Inflight at rest = %d, want 0", n)
	}
	done := make(chan Response, 1)
	go func() {
		resp, err := Call(SocketPath(dir), "session.create", map[string]any{"repo": "o/r", "branch": "feature", "agent": "codex"})
		if err != nil {
			resp = Response{Error: &Error{Code: "transport", Message: err.Error()}}
		}
		done <- resp
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("session.create never reached the creator")
	}
	if n := srv.Inflight(); n != 1 {
		close(release)
		t.Fatalf("Inflight during session.create = %d, want 1", n)
	}
	// A short call alongside is not counted.
	if resp := rpc(t, srv, "", "session.list", map[string]any{}); resp.Error != nil {
		close(release)
		t.Fatalf("session.list: %+v", resp.Error)
	}
	if n := srv.Inflight(); n != 1 {
		close(release)
		t.Fatalf("Inflight after a session.list = %d, want 1", n)
	}
	close(release)
	select {
	case resp := <-done:
		if resp.Error != nil {
			t.Fatalf("session.create: %+v", resp.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("session.create never answered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for srv.Inflight() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := srv.Inflight(); n != 0 {
		t.Fatalf("Inflight after session.create = %d, want 0", n)
	}
}

func TestLongCallMethods(t *testing.T) {
	for _, m := range []string{"session.create", "session.terminate", "design.publish", "review.run", "handover.write"} {
		if !longCall(m) {
			t.Errorf("longCall(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"repo.list", "aws.status", "ping", "session.list"} {
		if longCall(m) {
			t.Errorf("longCall(%q) = true, want false", m)
		}
	}
}
