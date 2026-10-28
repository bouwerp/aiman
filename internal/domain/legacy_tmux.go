package domain

import (
	"errors"
	"fmt"
	"strings"
)

// BackendLegacyTmux marks a session created when aiman still ran tmux. aiman
// never starts, attaches to or stops tmux; the user stops it on the host, and
// a restart then moves the session to pty.
const BackendLegacyTmux = "tmux"

// IsLegacyTmux reports whether the session still lives in a tmux session
// aiman no longer manages. An empty backend is not legacy: it means no
// terminal is recorded.
func (s Session) IsLegacyTmux() bool { return s.Backend == BackendLegacyTmux }

// ErrLegacyTmuxSession is the sentinel for operations refused on a legacy tmux
// session. Match it with errors.Is; use errors.As with *LegacyTmuxError for
// the host and the stop command.
var ErrLegacyTmuxSession = errors.New("session runs in tmux, which aiman no longer runs")

// LegacyTmuxError tells the user how to stop a legacy tmux session themselves.
// Attach and revive refuse such rows, since a second agent on the same
// conversation would fight the one still running in tmux.
type LegacyTmuxError struct {
	Host string
	Name string
}

// NewLegacyTmuxError builds the refusal for s.
func NewLegacyTmuxError(s Session) error {
	return &LegacyTmuxError{Host: s.RemoteHost, Name: s.TerminalName}
}

// StopCommand is the exact command the user runs on the host. The '=' prefix
// makes tmux match the name exactly instead of as a prefix.
func (e *LegacyTmuxError) StopCommand() string {
	return "tmux kill-session -t '=" + strings.ReplaceAll(e.Name, "'", `'\''`) + "'"
}

// Hint is the user-facing instruction naming the host and the stop command.
func (e *LegacyTmuxError) Hint() string {
	host := e.Host
	if host == "" {
		host = "the host"
	}
	return fmt.Sprintf("run %s on %s, then press s to move it to pty", e.StopCommand(), host)
}

func (e *LegacyTmuxError) Error() string {
	return ErrLegacyTmuxSession.Error() + ": " + e.Hint()
}

func (e *LegacyTmuxError) Unwrap() error { return ErrLegacyTmuxSession }
