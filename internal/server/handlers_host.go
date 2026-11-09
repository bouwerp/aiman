package server

import (
	"context"
	"log"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/hoststats"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// handleHostStats answers host.stats. It only reads.
func (s *Server) handleHostStats(ctx context.Context, req Request) Response {
	return Response{ID: req.ID, Result: struct {
		Type string `json:"type"`
		hoststats.Report
		RPC RPCStats `json:"rpc"`
	}{"host_stats", s.hostStats.Report(ctx, s.sessionRoots(ctx)), s.RPCStats()}}
}

// sessionRoots maps each live session to the PID its process tree hangs off.
// Hidden helpers are left out: no client lists them, and their processes still
// show under the host's top processes. A
// recorded PID can outlive its holder and be reused by any process, so it
// counts only while a holder still runs it; otherwise the session reports zero
// usage rather than a stranger's.
func (s *Server) sessionRoots(ctx context.Context) []hoststats.SessionRoot {
	if s.pty == nil {
		return nil
	}
	names := map[string]string{}
	hidden := map[string]bool{}
	if s.repo != nil {
		list, err := s.storedSessions(ctx)
		if err != nil {
			log.Printf("host.stats: list sessions: %v", err)
		}
		for _, sess := range list {
			names[sess.ID] = sess.Name
			hidden[sess.ID] = sess.Role.Hidden()
		}
	}
	held := s.holderHeld
	if held == nil {
		held = heldByHolder
	}
	infos := s.pty.List()
	confirmed := holderRoots(infos, held)
	roots := make([]hoststats.SessionRoot, 0, len(infos))
	for _, info := range infos {
		if strings.TrimSpace(info.ID) == "" || info.Status != ptyruntime.StatusRunning {
			continue
		}
		if hidden[info.ID] || domain.SessionRole(info.Role).Hidden() {
			continue
		}
		root := hoststats.SessionRoot{Session: info.ID, Name: names[info.ID]}
		if root.Name == "" {
			root.Name = info.Name
		}
		if confirmed[info.PID] {
			root.PID = info.PID
		}
		roots = append(roots, root)
	}
	return roots
}
