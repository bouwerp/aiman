package server

import (
	"context"
	"strings"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/domain"
)

// handleArtifactOperator answers share, unshare and delete, which only the
// operator may call. Each success records a row on domain.ArtifactsLedger:
// counts only, never the addresses.
func (s *Server) handleArtifactOperator(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, req.Method+" is the operator's")
	}
	if s.artifacts == nil {
		return errResp(req.ID, CodeUnavailable, "artifact publishing is not configured on this host")
	}
	var p struct {
		ID      string   `json:"id"`
		Emails  []string `json:"emails"`
		Domains []string `json:"domains"`
		All     bool     `json:"all"`
	}
	if bad := decodeParams(req, &p); bad != nil {
		return *bad
	}
	if !artifact.ValidID(p.ID) {
		return errResp(req.ID, CodeInvalidParams, "id is required: 20 lower-case base32 characters")
	}
	list := artifact.ShareList{Emails: trimAll(p.Emails), Domains: trimAll(p.Domains)}
	if req.Method == "artifact.delete" {
		if err := s.artifacts.Delete(ctx, p.ID); err != nil {
			return artifactErr(req.ID, err)
		}
		s.appendEvent(ctx, domain.ArtifactsLedger, domain.EventActorUser, domain.EventArtifactDeleted, map[string]any{"id": p.ID})
		return Response{ID: req.ID, Result: map[string]any{"type": "artifact_deleted", "id": p.ID}}
	}
	var (
		add, remove artifact.ShareList
		all         bool
		action      string
	)
	if req.Method == "artifact.share" {
		if len(list.Emails)+len(list.Domains) == 0 {
			return errResp(req.ID, CodeInvalidParams, "emails or domains are required")
		}
		add, action = list, "share"
	} else {
		if !p.All && len(list.Emails)+len(list.Domains) == 0 {
			return errResp(req.ID, CodeInvalidParams, "emails, domains or all is required")
		}
		remove, all, action = list, p.All, "unshare"
	}
	m, err := s.artifacts.Share(ctx, p.ID, add, remove, all)
	if err != nil {
		return artifactErr(req.ID, err)
	}
	s.appendEvent(ctx, domain.ArtifactsLedger, domain.EventActorUser, domain.EventArtifactShared, map[string]any{
		"id": p.ID, "action": action, "all": all,
		"changed_emails": len(list.Emails), "changed_domains": len(list.Domains),
		"emails": len(m.Share.Emails), "domains": len(m.Share.Domains),
	})
	return Response{ID: req.ID, Result: map[string]any{"type": "artifact_share", "share": m.Share}}
}

// trimAll drops blank entries and surrounding space.
func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
