package server

import (
	"context"
	"time"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

// HostUpdateTimeout bounds host.update inside the 30s client deadline. The
// install itself runs detached; the call only polls and starts it.
const HostUpdateTimeout = 25 * time.Second

// SetHostUpdates attaches the host self-update manager and the host name
// host.version reports. A nil manager means updates are off.
func (s *Server) SetHostUpdates(m *selfupdate.Manager, host string) {
	s.hostUpdatesMu.Lock()
	defer s.hostUpdatesMu.Unlock()
	s.hostUpdates = m
	s.hostName = host
}

func (s *Server) hostUpdater() (*selfupdate.Manager, string) {
	s.hostUpdatesMu.RLock()
	defer s.hostUpdatesMu.RUnlock()
	return s.hostUpdates, s.hostName
}

// HostVersionResult is the host.version answer.
type HostVersionResult struct {
	Type        string           `json:"type"`
	Host        string           `json:"host"`
	Version     string           `json:"version"`
	Protocol    int              `json:"protocol"`
	Latest      string           `json:"latest"`
	LatestReady bool             `json:"latest_ready"`
	Mode        string           `json:"mode"`
	Status      string           `json:"status"`
	CheckedAt   string           `json:"checked_at"`
	Attempt     *HostUpdateTried `json:"attempt"`
}

// HostUpdateTried is the last install attempt in host.version.
type HostUpdateTried struct {
	At     string `json:"at"`
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"`
	Exit   int    `json:"exit"`
	Error  string `json:"error"`
}

// HostUpdateResult is the host.update answer.
type HostUpdateResult struct {
	Type     string `json:"type"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Target   string `json:"target"`
}

func (s *Server) handleHostVersion(_ context.Context, req Request) Response {
	m, host := s.hostUpdater()
	out := HostVersionResult{
		Type:     "host_version",
		Host:     host,
		Version:  s.version,
		Protocol: ProtocolVersion,
		Mode:     string(selfupdate.ModeOff),
		Status:   selfupdate.StatusHeld,
	}
	if m == nil {
		return Response{ID: req.ID, Result: out}
	}
	st := m.Status()
	out.Version = st.Version
	out.Latest = st.Latest
	out.LatestReady = st.LatestReady
	out.Mode = string(st.Mode)
	out.Status = st.Status
	out.CheckedAt = rfc3339OrEmpty(st.CheckedAt)
	if a := st.Attempt; a != nil {
		out.Attempt = &HostUpdateTried{
			At:     rfc3339OrEmpty(a.At),
			From:   a.From,
			To:     a.To,
			Status: string(a.Status),
			Exit:   a.Exit,
			Error:  a.Error,
		}
	}
	return Response{ID: req.ID, Result: out}
}

func (s *Server) handleHostUpdate(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may update the host")
	}
	m, _ := s.hostUpdater()
	if m == nil {
		return errResp(req.ID, CodeInvalidParams, "host updates are off on this host")
	}
	ctx, cancel := context.WithTimeout(ctx, HostUpdateTimeout)
	defer cancel()
	o := m.Request(ctx)
	return Response{ID: req.ID, Result: HostUpdateResult{
		Type:     "host_update",
		Accepted: o.Accepted,
		Reason:   o.Reason,
		Target:   o.Target,
	}}
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
