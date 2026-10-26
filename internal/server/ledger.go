package server

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// sessionEventAppender is the ledger write surface. The sqlite repository
// implements it; a repository without it simply does not record events.
type sessionEventAppender interface {
	AppendSessionEvent(ctx context.Context, ev domain.SessionEvent) (int64, error)
}

// appendEvent writes one lifecycle fact. It never fails the request: a missing
// ledger, a cancelled client, or a database error is logged and dropped. The
// append uses a detached context with a short budget so a client disconnect
// after the operation does not lose the event.
func (s *Server) appendEvent(ctx context.Context, sessionID string, actor domain.EventActor, kind domain.EventKind, payload any) {
	if s.repo == nil || sessionID == "" {
		return
	}
	appender, ok := s.repo.(sessionEventAppender)
	if !ok {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("ledger: encode %s %s: %v", sessionID, kind, err)
		return
	}
	if actor == "" {
		actor = domain.EventActorUnknown
	}
	appendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := appender.AppendSessionEvent(appendCtx, domain.SessionEvent{
		SessionID: sessionID,
		At:        time.Now().UTC(),
		Actor:     actor,
		Kind:      kind,
		Payload:   raw,
	}); err != nil {
		log.Printf("ledger: append %s %s: %v", sessionID, kind, err)
	}
}
