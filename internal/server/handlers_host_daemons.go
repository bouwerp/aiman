package server

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/redact"
)

// Limits on host.logs.
const (
	HostLogsDefaultLines = 200
	HostLogsMaxLines     = 1000
	// HostLogsLineBytes caps one returned line, after redaction.
	HostLogsLineBytes = 2 << 10
	// HostLogsReplyBytes caps the lines of one reply; the oldest go first.
	HostLogsReplyBytes = 256 << 10
	// HostDaemonsTimeout bounds one host.daemons, host.logs or host.restart.
	HostDaemonsTimeout = 20 * time.Second
)

// daemonKinds are the daemons host.logs and host.restart accept.
var daemonKinds = []string{"serve", "gateway", "trigger"}

// DaemonControl reads and restarts this host's aiman daemons (serve, gateway,
// trigger). serve wires the real one; tests fake it.
type DaemonControl interface {
	// Daemons returns one row per daemon; refresh skips any cache.
	Daemons(ctx context.Context, refresh bool) ([]DaemonState, error)
	// Logs returns the last lines of kind's output, unredacted.
	Logs(ctx context.Context, kind string, lines int) (DaemonLogs, error)
	// Restart starts a detached restart of kind.
	Restart(ctx context.Context, kind string, force bool) (RestartOutcome, error)
}

// DaemonRedactor is an optional DaemonControl method: exact values host.logs
// replaces wherever they appear (the gateway token). The values never leave
// serve.
type DaemonRedactor interface {
	RedactValues() []string
}

// DaemonState is one row of host.daemons. A daemon that is not installed has
// Installed false and nothing else.
type DaemonState struct {
	Kind      string `json:"kind"`
	Installed bool   `json:"installed"`
	// Driver is systemd, nohup or none.
	Driver    string    `json:"driver"`
	Active    string    `json:"active"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at,omitzero"`
	RSSBytes  int64     `json:"rss_bytes"`
	// Restart is the last restart operation of this daemon; nil when none.
	Restart *DaemonRestart `json:"restart,omitempty"`
}

// DaemonRestart is a restart operation: Name restart-<kind>, Status started,
// ok or failed, Exit the script's exit once it ended.
type DaemonRestart struct {
	Name   string    `json:"name"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	Exit   int       `json:"exit"`
}

// DaemonLogs is what a control read: Source is journal or file.
type DaemonLogs struct {
	Source string
	Lines  []string
}

// RestartOutcome answers Restart. Reason is busy or running when refused.
type RestartOutcome struct {
	Accepted bool
	Reason   string
}

// HostDaemonsResult is the host.daemons answer.
type HostDaemonsResult struct {
	Type    string        `json:"type"`
	Host    string        `json:"host"`
	Daemons []DaemonState `json:"daemons"`
}

// HostLogsResult is the host.logs answer.
type HostLogsResult struct {
	Type     string   `json:"type"`
	Daemon   string   `json:"daemon"`
	Source   string   `json:"source"`
	Lines    []string `json:"lines"`
	Redacted int      `json:"redacted"`
}

// HostRestartResult is the host.restart answer.
type HostRestartResult struct {
	Type     string `json:"type"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Daemon   string `json:"daemon"`
}

// daemonControl holds the control behind its own lock, so it needs one
// Server field.
type daemonControl struct {
	mu sync.RWMutex
	c  DaemonControl
}

// SetDaemonControl attaches the control behind host.daemons, host.logs and
// host.restart. Without one they answer unavailable.
func (s *Server) SetDaemonControl(c DaemonControl) {
	s.daemons.mu.Lock()
	defer s.daemons.mu.Unlock()
	s.daemons.c = c
}

func (s *Server) daemonControl() DaemonControl {
	s.daemons.mu.RLock()
	defer s.daemons.mu.RUnlock()
	return s.daemons.c
}

// dispatchHostDaemons routes host.daemons, host.logs and host.restart.
func (s *Server) dispatchHostDaemons(ctx context.Context, req Request) Response {
	switch req.Method {
	case "host.daemons":
		return s.handleHostDaemons(ctx, req)
	case "host.logs":
		return s.handleHostLogs(ctx, req)
	default:
		return s.handleHostRestart(ctx, req)
	}
}

func (s *Server) handleHostDaemons(ctx context.Context, req Request) Response {
	c := s.daemonControl()
	if c == nil {
		return errResp(req.ID, CodeUnavailable, "daemon control is not available on this host")
	}
	var p struct {
		Refresh bool `json:"refresh"`
	}
	if !decodeOptional(req.Params, &p) {
		return errResp(req.ID, CodeInvalidParams, "invalid params")
	}
	ctx, cancel := context.WithTimeout(ctx, HostDaemonsTimeout)
	defer cancel()
	rows, err := c.Daemons(ctx, p.Refresh)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	_, host := s.hostUpdater()
	if rows == nil {
		rows = []DaemonState{}
	}
	return Response{ID: req.ID, Result: HostDaemonsResult{Type: "host_daemons", Host: host, Daemons: rows}}
}

// daemonParams decodes {daemon, ...} for host.logs and host.restart after the
// operator and control checks; it returns a refusal or nil.
func (s *Server) daemonParams(ctx context.Context, req Request, what string, p any, daemon *string) (DaemonControl, *Response) {
	if s.isSessionCall(ctx, req) {
		r := errResp(req.ID, CodeNeedsOperator, "only the operator may "+what)
		return nil, &r
	}
	c := s.daemonControl()
	if c == nil {
		r := errResp(req.ID, CodeUnavailable, "daemon control is not available on this host")
		return nil, &r
	}
	if !decodeOptional(req.Params, p) {
		r := errResp(req.ID, CodeInvalidParams, "invalid params")
		return nil, &r
	}
	if !slices.Contains(daemonKinds, *daemon) {
		r := errResp(req.ID, CodeInvalidParams, "daemon must be serve, gateway or trigger")
		return nil, &r
	}
	return c, nil
}

func (s *Server) handleHostLogs(ctx context.Context, req Request) Response {
	var p struct {
		Daemon string `json:"daemon"`
		Lines  int    `json:"lines"`
	}
	c, refused := s.daemonParams(ctx, req, "read daemon logs", &p, &p.Daemon)
	if refused != nil {
		return *refused
	}
	n := p.Lines
	if n <= 0 {
		n = HostLogsDefaultLines
	}
	n = min(n, HostLogsMaxLines)
	ctx, cancel := context.WithTimeout(ctx, HostDaemonsTimeout)
	defer cancel()
	logs, err := c.Logs(ctx, p.Daemon, n)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	var exact []string
	if r, ok := c.(DaemonRedactor); ok {
		exact = r.RedactValues()
	}
	lines, hits := redact.Lines(logs.Lines, exact)
	return Response{ID: req.ID, Result: HostLogsResult{
		Type:     "host_logs",
		Daemon:   p.Daemon,
		Source:   logs.Source,
		Lines:    capLogLines(lines),
		Redacted: hits,
	}}
}

// capLogLines cuts each line to HostLogsLineBytes on a rune boundary, then
// drops the oldest lines until the rest fit HostLogsReplyBytes.
func capLogLines(lines []string) []string {
	total := 0
	for i, l := range lines {
		if len(l) > HostLogsLineBytes {
			cut := HostLogsLineBytes
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			l = l[:cut]
			lines[i] = l
		}
		total += len(l) + 1
	}
	start := 0
	for total > HostLogsReplyBytes && start < len(lines) {
		total -= len(lines[start]) + 1
		start++
	}
	out := lines[start:]
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *Server) handleHostRestart(ctx context.Context, req Request) Response {
	var p struct {
		Daemon string `json:"daemon"`
		Force  bool   `json:"force"`
	}
	c, refused := s.daemonParams(ctx, req, "restart a daemon", &p, &p.Daemon)
	if refused != nil {
		return *refused
	}
	ctx, cancel := context.WithTimeout(ctx, HostDaemonsTimeout)
	defer cancel()
	o, err := c.Restart(ctx, p.Daemon, p.Force)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	return Response{ID: req.ID, Result: HostRestartResult{
		Type:     "host_restart",
		Accepted: o.Accepted,
		Reason:   o.Reason,
		Daemon:   p.Daemon,
	}}
}

// decodeOptional unmarshals params into p; absent or null params are fine.
func decodeOptional(params json.RawMessage, p any) bool {
	if len(params) == 0 || string(params) == "null" {
		return true
	}
	return json.Unmarshal(params, p) == nil
}
