package ui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// ciClient reads and toggles a session's PR watch through serve.
type ciClient interface {
	Get(ctx context.Context, remote config.Remote, sessionID string) (usecase.CIStatus, error)
	SetWatch(ctx context.Context, remote config.Remote, sessionID string, on bool) (usecase.CIStatus, error)
}

type sshCIClient struct {
	dial func(config.Remote) usecase.TerminalExecutor
}

func (c sshCIClient) Get(ctx context.Context, remote config.Remote, sessionID string) (usecase.CIStatus, error) {
	return usecase.GetCIStatusOnRemote(ctx, c.dial(remote), sessionID)
}

func (c sshCIClient) SetWatch(ctx context.Context, remote config.Remote, sessionID string, on bool) (usecase.CIStatus, error) {
	return usecase.SetPRWatchOnRemote(ctx, c.dial(remote), sessionID, on)
}

var _ ciClient = sshCIClient{}

// ciStatusMsg is the answer to ci.get, or to ci.watch when toggled is set.
// host is the resolved remote, so an unsupported answer marks the right one.
type ciStatusMsg struct {
	id      string
	host    string
	status  usecase.CIStatus
	err     error
	toggled bool
}

// watchingPRNumber is the PR number the meta line names, or 0 when the
// session is not watched or has no PR yet.
func watchingPRNumber(st usecase.CIStatus, ok bool) int {
	if ok && st.Watch && st.PRNumber > 0 {
		return st.PRNumber
	}
	return 0
}

// ciNeedsUser reports a check that escalated: the agent failed to fix it
// too many times, so the row shows the needs-you glyph.
func ciNeedsUser(st usecase.CIStatus, ok bool) bool {
	return ok && len(st.Escalated) > 0
}

// nextPRWatch is the value w sends. Without a cached status it flips the
// session's own default, which is what serve uses until told otherwise.
func nextPRWatch(s domain.Session, st usecase.CIStatus, ok bool) bool {
	if ok {
		return !st.Watch
	}
	return !s.WatchesPR()
}

// fetchCIStatusCmd reads ci.get for one session. It returns nil when there is
// no client, the host's serve cannot answer, a read is already in flight, or
// the session is being created or terminated.
func (m *Model) fetchCIStatusCmd(s domain.Session) tea.Cmd {
	if s.ID == "" || m.ciClient == nil || m.ciInFlight[s.ID] || m.skipSessionPolling(s.ID) {
		return nil
	}
	remote, ok := resolveRemote(m.cfg, s)
	if !ok || m.ciUnsupported[remote.Host] {
		return nil
	}
	if m.ciInFlight == nil {
		m.ciInFlight = map[string]bool{}
	}
	m.ciInFlight[s.ID] = true
	client := m.ciClient
	id := s.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := client.Get(ctx, remote, id)
		return ciStatusMsg{id: id, host: remote.Host, status: st, err: err}
	}
}

// togglePRWatchCmd sends ci.watch with the opposite of the current watch.
// An unsupported host is still asked, so the user sees why w did nothing.
func (m *Model) togglePRWatchCmd(s domain.Session) tea.Cmd {
	if s.ID == "" || m.ciClient == nil {
		return nil
	}
	remote, ok := resolveRemote(m.cfg, s)
	if !ok {
		return nil
	}
	if m.ciToggling[s.ID] {
		return m.showToast("PR watch change already in progress…", false, 2*time.Second)
	}
	if m.ciToggling == nil {
		m.ciToggling = map[string]bool{}
	}
	m.ciToggling[s.ID] = true
	st, cached := m.ciStatus[s.ID]
	on := nextPRWatch(s, st, cached)
	client := m.ciClient
	id := s.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		st, err := client.SetWatch(ctx, remote, id, on)
		return ciStatusMsg{id: id, host: remote.Host, status: st, err: err, toggled: true}
	}
}

// handleWatchKey toggles the selected session's PR watch.
func (m *Model) handleWatchKey() tea.Cmd {
	it, ok := m.selectedSessionItem()
	if !ok {
		return nil
	}
	cmd := m.togglePRWatchCmd(it.session)
	if cmd == nil {
		return m.showToast("⚠️  PR watch is unavailable for this session.", true, 4*time.Second)
	}
	return cmd
}

// applyCIStatus caches a ci.get or ci.watch answer, fills the meta line and
// the needs-you flag, and toasts the outcome of a toggle.
func (m *Model) applyCIStatus(msg ciStatusMsg) tea.Cmd {
	if msg.toggled {
		delete(m.ciToggling, msg.id)
	} else {
		delete(m.ciInFlight, msg.id)
	}
	if msg.err != nil {
		return m.noteCIError(msg)
	}
	// Any answer proves the host can serve ci.get again.
	delete(m.ciUnsupported, msg.host)
	if m.ciStatus == nil {
		m.ciStatus = map[string]usecase.CIStatus{}
	}
	m.ciStatus[msg.id] = msg.status
	if m.metaExtras == nil {
		m.metaExtras = map[string]detailMetaExtras{}
	}
	extras := m.metaExtras[msg.id]
	extras.WatchingPR = watchingPRNumber(msg.status, true)
	m.metaExtras[msg.id] = extras
	m.refreshCIEscalation(msg.id)
	if !msg.toggled {
		return nil
	}
	label := msg.id
	if s, ok := m.sessionByID(msg.id); ok {
		label = notify.Label(s.IssueKey, s.Name, s.ID)
	}
	if msg.status.Watch {
		return m.showToast("Watching PR for "+label, false, 3*time.Second)
	}
	return m.showToast("Stopped watching PR for "+label, false, 3*time.Second)
}

// noteCIError toasts a failed toggle. A failed read is quiet: an old binary or
// a stopped serve marks the host so it is not asked again, anything else is
// logged.
func (m *Model) noteCIError(msg ciStatusMsg) tea.Cmd {
	unsupported := errors.Is(msg.err, usecase.ErrCIUnsupported)
	if msg.toggled {
		if unsupported {
			return m.showToast("⚠️  Update aiman on "+msg.host+" to use PR watch", true, 6*time.Second)
		}
		return m.showToast("⚠️  PR watch: "+msg.err.Error(), true, 6*time.Second)
	}
	if unsupported || errors.Is(msg.err, usecase.ErrServeNotRunning) {
		if m.ciUnsupported == nil {
			m.ciUnsupported = map[string]bool{}
		}
		m.ciUnsupported[msg.host] = true
		return nil
	}
	m.log("ci.get %s on %s: %v", msg.id, msg.host, msg.err)
	return nil
}

// refreshCIEscalation copies the cached escalation onto the matching list row.
func (m *Model) refreshCIEscalation(id string) {
	st, ok := m.ciStatus[id]
	for idx, li := range m.list.Items() {
		it, isItem := li.(item)
		if !isItem || it.header || it.session.ID != id {
			continue
		}
		it.escalated = ciNeedsUser(st, ok)
		m.list.SetItem(idx, it)
		return
	}
}

// ciEscalationFetch reads ci.get for the session a ci_escalation notification
// names, selected or not, so its row turns to needs-you.
func (m *Model) ciEscalationFetch(ev server.SessionEvent) tea.Cmd {
	if domain.NotifyKind(ev.Kind) != domain.NotifyCIEscalation {
		return nil
	}
	s, ok := m.sessionByID(ev.ID)
	if !ok {
		return nil
	}
	return m.fetchCIStatusCmd(s)
}
