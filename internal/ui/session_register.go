package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
)

// registerTimeout bounds one session.register round trip.
const registerTimeout = 30 * time.Second

// sessionsRegisteredMsg reports the outcome of registering sessions with a
// remote serve. forgotten lists the ids serve refused because they were
// terminated or forgotten there.
type sessionsRegisteredMsg struct {
	host      string
	n         int
	forgotten []string
	err       error
}

// sessionsForHost returns copies of the real sessions on host, skipping
// placeholder and empty ids.
func sessionsForHost(all []domain.Session, host string) []domain.Session {
	var out []domain.Session
	for _, s := range all {
		if s.RemoteHost != host || s.ID == "" || domain.IsEphemeralSessionID(s.ID) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// registerSessionsCmd tells the remote's serve about laptop-known sessions. It
// runs off the UI thread and works from the snapshot it is given.
func registerSessionsCmd(remote config.Remote, sessions []domain.Session) tea.Cmd {
	if len(sessions) == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
		defer cancel()
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		forgotten, err := usecase.RegisterSessionsOnRemote(ctx, mgr, sessions)
		return sessionsRegisteredMsg{host: remote.Host, n: len(sessions), forgotten: forgotten, err: err}
	}
}

// applySessionsRegistered logs a register round trip and drops the rows serve
// refused: a session ended on the host must not linger here, or the next
// stream reconnect would offer it again.
func (m *Model) applySessionsRegistered(msg sessionsRegisteredMsg) {
	if msg.err != nil {
		m.logPersistent("register %d sessions with serve on %s failed: %v", msg.n, msg.host, msg.err)
		return
	}
	m.log("registered %d sessions with serve on %s", msg.n-len(msg.forgotten), msg.host)
	if len(msg.forgotten) == 0 {
		return
	}
	gone := make(map[string]bool, len(msg.forgotten))
	for _, id := range msg.forgotten {
		gone[id] = true
	}
	kept := m.allSessions[:0]
	var removed []string
	for _, s := range m.allSessions {
		if s.RemoteHost == msg.host && gone[s.ID] {
			removed = append(removed, s.ID)
			continue
		}
		kept = append(kept, s)
	}
	m.allSessions = kept
	if len(removed) == 0 {
		return
	}
	if m.db != nil {
		for _, id := range removed {
			if err := m.db.Delete(context.Background(), id); err != nil {
				m.log("register: delete forgotten row %s: %v", id, err)
			}
		}
	}
	m.log("serve on %s ended %d sessions; dropped them here: %v", msg.host, len(removed), removed)
	m.maybeRefreshSessionList()
}
