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
// remote serve. It is only logged.
type sessionsRegisteredMsg struct {
	host string
	n    int
	err  error
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
		err := usecase.RegisterSessionsOnRemote(ctx, mgr, sessions)
		return sessionsRegisteredMsg{host: remote.Host, n: len(sessions), err: err}
	}
}
