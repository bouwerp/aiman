package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// tmuxTrapRemote is a batch discovery remote that counts every call into the
// tmux side of RemoteExecutor, so a test can prove discovery never asks tmux.
type tmuxTrapRemote struct {
	batchRemote
	tmuxMethodCalls int
}

func (r *tmuxTrapRemote) ScanTmuxSessions(context.Context) ([]string, error) {
	r.tmuxMethodCalls++
	return []string{"live-tmux"}, nil
}

func (r *tmuxTrapRemote) GetTmuxSessionEnv(context.Context, string, string) (string, error) {
	r.tmuxMethodCalls++
	return "", nil
}

func (r *tmuxTrapRemote) GetTmuxSessionCWD(context.Context, string) (string, error) {
	r.tmuxMethodCalls++
	return "", nil
}

func ptyListOutput(sessions string) map[string]string {
	return map[string]string{scanPTYSessionsCmd: `{"sessions":[` + sessions + `]}`}
}

func TestDiscoverRunsNoTmux(t *testing.T) {
	remote := &tmuxTrapRemote{}
	remote.outputs = ptyListOutput(`{"id":"p1","name":"feat","dir":"/repos/app@feat","status":"running"}`)
	remote.tmuxRecords = []domain.TmuxSessionRecord{{Name: "live-tmux", AimanID: "t1", CWD: "/repos/app@t"}}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if remote.tmuxMethodCalls != 0 || remote.tmuxCalls != 0 {
		t.Fatalf("discovery asked tmux: methods=%d batch=%d", remote.tmuxMethodCalls, remote.tmuxCalls)
	}
	requireNoTmux(t, remote.commands)
	if len(sessions) != 1 || sessions[0].ID != "p1" || sessions[0].Backend != domain.BackendPTY {
		t.Fatalf("want only the pty session, got %+v", sessions)
	}
}

// PTY sessions get the same mutagen overlay tmux sessions used to get, so the
// dashboard knows their sync and local path.
func TestDiscoverPTYSessionGetsMutagenOverlay(t *testing.T) {
	remote := &batchRemote{}
	remote.outputs = ptyListOutput(`{"id":"p1","name":"feat","dir":"/repos/app@feat/svc","status":"running"}`)
	syncs := &recordingSyncEngine{syncs: []domain.SyncSession{{
		ID: "uuid-1", Name: aimanSyncNamePrefix + "p1", RemotePath: "/repos/app@feat/svc", LocalPath: "/home/me/.aiman/work/p1",
	}}}

	sessions, err := NewSessionDiscoverer(remote, syncs).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want one session (no ghost sync row), got %+v", sessions)
	}
	got := sessions[0]
	if got.MutagenSyncID != aimanSyncNamePrefix+"p1" || got.LocalPath != "/home/me/.aiman/work/p1" {
		t.Fatalf("no mutagen overlay: %+v", got)
	}
	if got.Status != domain.SessionStatusSyncing {
		t.Fatalf("Status = %q, want syncing", got.Status)
	}
	if len(syncs.terminated) != 0 {
		t.Fatalf("terminated a live session's sync: %v", syncs.terminated)
	}
}

// An ad-hoc PTY session has no worktree. Its aiman-named sync matches only the
// PTY record, and must not be terminated as an orphan.
func TestDiscoverKeepsSyncOfLiveAdHocPTYSession(t *testing.T) {
	remote := &batchRemote{}
	remote.outputs = ptyListOutput(`{"id":"adhoc1","name":"adhoc-x","dir":"/root/adhoc-x","status":"running"}`)
	syncs := &recordingSyncEngine{syncs: []domain.SyncSession{
		{ID: "u1", Name: aimanSyncNamePrefix + "adhoc1", RemotePath: "/root/adhoc-x"},
		// Same path, but under a name the overlay does not claim first.
		{ID: "u2", Name: aimanSyncNamePrefix + "adhoc1-pull", RemotePath: "/root/adhoc-x"},
	}}

	if _, err := NewSessionDiscoverer(remote, syncs).Discover(context.Background(), "regent0"); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(syncs.terminated) != 0 {
		t.Fatalf("terminated syncs of a live pty session: %v", syncs.terminated)
	}
}

// PTY terminal names are labels, not unique on a host: two review children of
// one parent can share one. Both are sessions, so discovery returns both.
func TestDiscoverKeepsPTYSessionsThatShareAName(t *testing.T) {
	remote := &batchRemote{}
	remote.outputs = ptyListOutput(
		`{"id":"child-grok","name":"adhoc-20261022-1130","dir":"/repos/app@nl","status":"running"},` +
			`{"id":"child-muse","name":"adhoc-20261022-1130","dir":"/repos/app@nl","status":"running"}`)

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	ids := map[string]bool{}
	for _, s := range sessions {
		ids[s.ID] = true
	}
	if !ids["child-grok"] || !ids["child-muse"] {
		t.Fatalf("want both sessions sharing a name, got %+v", sessions)
	}
}

// With no live session on its path, an aiman-named sync is still an orphan.
func TestDiscoverStillTerminatesOrphanAimanSync(t *testing.T) {
	remote := &batchRemote{}
	remote.outputs = ptyListOutput("")
	syncs := &recordingSyncEngine{syncs: []domain.SyncSession{
		{ID: "u1", Name: aimanSyncNamePrefix + "gone", RemotePath: "/root/gone"},
	}}
	if _, err := NewSessionDiscoverer(remote, syncs).Discover(context.Background(), "regent0"); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(syncs.terminated) != 1 || syncs.terminated[0] != aimanSyncNamePrefix+"gone" {
		t.Fatalf("terminated = %v", syncs.terminated)
	}
}

// Serve down means the host's sessions are unknown, so Discover fails and the
// host is not marked scanned.
func TestDiscoverFailsWhenServeDown(t *testing.T) {
	remote := &batchRemote{}
	remote.outputs = map[string]string{scanPTYSessionsCmd: serveDownMarker}

	_, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if !errors.Is(err, ErrServeDown) {
		t.Fatalf("err = %v, want ErrServeDown", err)
	}
	if _, ok := DiscoverHostSessions(context.Background(), remote, &recordingSyncEngine{}, "regent0"); ok {
		t.Fatal("serve down must not mark the host scanned")
	}
}

// adHocCreateRemote answers what an ad-hoc create asks: the PTY runtime is up and
// every other command succeeds silently.
type adHocCreateRemote struct {
	discovererRemote
	written map[string][]byte
}

func (r *adHocCreateRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.commands = append(r.commands, cmd)
	if strings.Contains(cmd, "aiman pty list") {
		return "YES", nil
	}
	return "", nil
}

func (r *adHocCreateRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if r.written == nil {
		r.written = map[string][]byte{}
	}
	r.written[path] = content
	return nil
}

func TestCreateSessionIgnoresTmuxBackend(t *testing.T) {
	remote := &adHocCreateRemote{}
	m := &FlowManager{}
	sess, err := m.CreateSession(context.Background(), domain.SessionConfig{
		AdHoc:          true,
		Branch:         "adhoc-x",
		Agent:          &domain.Agent{Name: "Claude Code", Command: "claude"},
		SSHManager:     remote,
		SessionBackend: "tmux",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Backend != domain.BackendPTY {
		t.Fatalf("Backend = %q, want pty", sess.Backend)
	}
	requireNoTmux(t, remote.commands)
	var created bool
	for path := range remote.written {
		if strings.HasPrefix(path, "/tmp/aiman-pty-"+sess.ID) {
			created = true
		}
	}
	if !created {
		t.Fatalf("no pty create params written: %v", remote.written)
	}
}
