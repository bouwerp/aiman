package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/server"
)

// The event stream replaces asking a remote what its sessions are doing with
// being told. serve knows the moment a holder records output or an agent changes
// its terminal title, so the dashboard holds one long-lived `aiman pty events`
// per remote and reacts to what arrives.
//
// The stream is a trigger, not a second source of truth: an event says something
// changed, and the existing classification then decides what it changed to. That
// keeps one authority for session state while letting it run when there is news
// instead of on a fixed timer.
const (
	// eventRefreshInterval rate-limits event-driven refreshes per session. An
	// agent animating a spinner in its title changes it several times a second,
	// and each refresh is an SSH round trip.
	eventRefreshInterval = 1500 * time.Millisecond

	// eventStreamRetryAfter is how long to wait before redialling a stream that
	// ended — most often because serve is not running on that remote.
	eventStreamRetryAfter = 60 * time.Second
)

// sessionEventMsg carries one event from a remote's stream.
type sessionEventMsg struct {
	host  string
	event server.SessionEvent
}

// sessionStreamEndedMsg reports that a remote's stream stopped, so it can be
// redialled later.
type sessionStreamEndedMsg struct {
	host string
	err  error
}

// sessionStream is a running `aiman pty events` for one remote.
type sessionStream struct {
	host   string
	events chan server.SessionEvent
	cancel context.CancelFunc
}

// startSessionStream launches the stream for a remote and returns a command that
// waits for its first event.
func (m *Model) startSessionStream(remote config.Remote) tea.Cmd {
	if m.streams == nil {
		m.streams = map[string]*sessionStream{}
	}
	if _, running := m.streams[remote.Host]; running {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &sessionStream{
		host:   remote.Host,
		events: make(chan server.SessionEvent, 64),
		cancel: cancel,
	}
	m.streams[remote.Host] = st

	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	cmd := mgr.SessionEventsCommand(ctx)
	m.log("session events: connecting to %s", remote.Host)

	return tea.Batch(
		runSessionStream(cmd, st),
		waitForSessionEvent(st),
	)
}

// stopSessionStream ends a remote's stream.
func (m *Model) stopSessionStream(host string) {
	st, ok := m.streams[host]
	if !ok {
		return
	}
	st.cancel()
	delete(m.streams, host)
}

// runSessionStream reads the subprocess's stdout, feeding decoded events into
// the stream's channel until it ends.
func runSessionStream(cmd *exec.Cmd, st *sessionStream) tea.Cmd {
	return func() tea.Msg {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			close(st.events)
			return sessionStreamEndedMsg{host: st.host, err: err}
		}
		if err := cmd.Start(); err != nil {
			close(st.events)
			return sessionStreamEndedMsg{host: st.host, err: err}
		}
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			var ev server.SessionEvent
			if jerr := json.Unmarshal(scanner.Bytes(), &ev); jerr != nil {
				continue // a line we do not understand is not worth ending the stream
			}
			select {
			case st.events <- ev:
			default: // a full channel means the UI is behind; dropping is correct
			}
		}
		werr := cmd.Wait()
		close(st.events)
		return sessionStreamEndedMsg{host: st.host, err: werr}
	}
}

// waitForSessionEvent blocks for the stream's next event. Re-issued on each
// event, which is how a channel is consumed in this framework.
func waitForSessionEvent(st *sessionStream) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-st.events
		if !ok {
			return sessionStreamEndedMsg{host: st.host}
		}
		return sessionEventMsg{host: st.host, event: ev}
	}
}

// applySessionEvent reacts to one event and queues the wait for the next.
func (m *Model) applySessionEvent(msg sessionEventMsg) tea.Cmd {
	st, ok := m.streams[msg.host]
	if !ok {
		return nil // the stream was stopped; stop consuming it
	}
	next := waitForSessionEvent(st)

	if msg.event.Type == "keepalive" || msg.event.ID == "" {
		return next
	}
	if m.eventSeen == nil {
		m.eventSeen = map[string]sessionEventState{}
	}
	state := m.eventSeen[msg.event.ID]
	state.title = msg.event.Title
	state.titleChanged = parseEventTime(msg.event.TitleChanged)
	state.lastOutput = parseEventTime(msg.event.LastOutput)

	// Immediate feedback while an agent is visibly working: a title that moved a
	// moment ago is direct evidence, and waiting for the next poll to show it
	// wastes the whole point of the stream. Anything more nuanced — blocked,
	// idle, exited — needs the pane, so it stays with the classifier.
	if cmds := m.markSessionBusyFromEvent(msg.event.ID, state); cmds != nil {
		m.eventSeen[msg.event.ID] = state
		return tea.Batch(next, cmds)
	}

	// Otherwise ask the classifier, rate-limited: the title changes several
	// times a second and each refresh is a round trip.
	if time.Since(state.lastRefresh) >= eventRefreshInterval {
		if sess, found := m.sessionByID(msg.event.ID); found {
			state.lastRefresh = time.Now()
			m.eventSeen[msg.event.ID] = state
			return tea.Batch(next, checkInputHint(m.cfg, sess))
		}
	}
	m.eventSeen[msg.event.ID] = state
	return next
}

// sessionEventState is the latest a stream has said about one session.
type sessionEventState struct {
	title        string
	titleChanged time.Time
	lastOutput   time.Time
	lastRefresh  time.Time
}

// markSessionBusyFromEvent shows a session as busy straight away when its title
// has just moved, returning nil when the event says nothing that certain.
func (m *Model) markSessionBusyFromEvent(id string, state sessionEventState) tea.Cmd {
	if state.titleChanged.IsZero() || time.Since(state.titleChanged) > pane.TitleActivityWindow {
		return nil
	}
	items := m.list.Items()
	for idx, it := range items {
		sessItem, ok := it.(item)
		if !ok || sessItem.session.ID != id {
			continue
		}
		if sessItem.activity == "busy" {
			return nil // already shown as busy; nothing to redraw
		}
		sessItem.activity = "busy"
		sessItem.needsInput = false
		items[idx] = sessItem
		m.list.SetItems(items)
		return func() tea.Msg { return nil }
	}
	return nil
}

// applyStreamEnded forgets a dead stream and schedules a redial.
func (m *Model) applyStreamEnded(msg sessionStreamEndedMsg) tea.Cmd {
	delete(m.streams, msg.host)
	if msg.err != nil {
		m.log("session events: %s ended (%v); retrying in %s", msg.host, msg.err, eventStreamRetryAfter)
	}
	host := msg.host
	return tea.Tick(eventStreamRetryAfter, func(time.Time) tea.Msg {
		return sessionStreamRetryMsg{host: host}
	})
}

// sessionStreamRetryMsg asks for a stream to be redialled.
type sessionStreamRetryMsg struct{ host string }

// ensureSessionStreams opens a stream for every remote that hosts a PTY session
// and closes those that no longer do.
//
// Only PTY sessions publish activity, so a tmux-only remote has nothing to
// stream and is left alone rather than holding an idle SSH connection open.
func (m *Model) ensureSessionStreams() tea.Cmd {
	want := map[string]config.Remote{}
	for _, s := range m.allSessions {
		if !s.IsPTY() {
			continue
		}
		if remote, ok := resolveRemote(m.cfg, s); ok {
			want[remote.Host] = remote
		}
	}

	var cmds []tea.Cmd
	for host, remote := range want {
		if _, running := m.streams[host]; !running {
			if cmd := m.startSessionStream(remote); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	for host := range m.streams {
		if _, still := want[host]; !still {
			m.stopSessionStream(host)
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// sessionByID finds a session in the model's own list.
func (m *Model) sessionByID(id string) (domain.Session, bool) {
	for _, s := range m.allSessions {
		if s.ID == id {
			return s, true
		}
	}
	return domain.Session{}, false
}

func parseEventTime(v string) time.Time {
	if strings.TrimSpace(v) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// remoteByHost finds a configured remote by host.
func remoteByHost(cfg *config.Config, host string) (config.Remote, bool) {
	for _, r := range cfg.Remotes {
		if r.Host == host {
			return r, true
		}
	}
	return config.Remote{}, false
}
