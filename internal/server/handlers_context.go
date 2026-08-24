package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
)

type ContextNote struct {
	ID        string `json:"id"`
	Namespace string `json:"ns"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	Abstract  string `json:"abstract"`
	Body      string `json:"body,omitempty"`
	Session   string `json:"session,omitempty"`
	Created   string `json:"created,omitempty"`
}

type contextQueryParams struct {
	NS    string `json:"ns"`
	Key   string `json:"key"`
	Text  string `json:"text"`
	Limit int    `json:"limit"`
	Group string `json:"group"`
	Repo  string `json:"repo"`
}

type contextPutParams struct {
	ID       string `json:"id"`
	NS       string `json:"ns"`
	Key      string `json:"key"`
	Title    string `json:"title"`
	Abstract string `json:"abstract"`
	Body     string `json:"body"`
	Session  string `json:"session"`
	Group    string `json:"group"`
	Repo     string `json:"repo"`
}

func contextNote(e domain.ContextEntry, body bool) ContextNote {
	n := ContextNote{
		ID:        e.ID,
		Namespace: e.Namespace,
		Key:       e.Key,
		Title:     e.Title,
		Abstract:  e.Abstract,
		Session:   e.SessionID,
	}
	if !e.CreatedAt.IsZero() {
		n.Created = e.CreatedAt.UTC().Format(time.RFC3339)
	}
	if body {
		n.Body = e.Body
	}
	return n
}

func (p contextQueryParams) query() domain.ContextQuery {
	q := domain.ContextQuery{Namespace: p.NS, Key: p.Key, Text: p.Text, Limit: p.Limit}
	if q.Namespace == "" && strings.TrimSpace(p.Group) != "" {
		q.Namespace = domain.ContextNSGroup
		q.Key = p.Group
	}
	if q.Namespace == "" && strings.TrimSpace(p.Repo) != "" {
		q.Namespace = domain.ContextNSRepo
		q.Key = p.Repo
	}
	return q
}

func (p contextPutParams) entry() domain.ContextEntry {
	e := domain.ContextEntry{
		ID:        p.ID,
		Namespace: p.NS,
		Key:       p.Key,
		Title:     p.Title,
		Abstract:  p.Abstract,
		Body:      p.Body,
		SessionID: p.Session,
	}
	if e.Namespace == "" && strings.TrimSpace(p.Group) != "" {
		e.Namespace = domain.ContextNSGroup
		e.Key = p.Group
	}
	if e.Namespace == "" && strings.TrimSpace(p.Repo) != "" {
		e.Namespace = domain.ContextNSRepo
		e.Key = p.Repo
	}
	return e
}

func (s *Server) requireStore(id string) (domain.ContextStore, Response, bool) {
	if s.ctxStore == nil {
		return nil, errResp(id, CodeInvalidParams, "context store unavailable"), false
	}
	return s.ctxStore, Response{}, true
}

func decodeContextQuery(req Request) contextQueryParams {
	var p contextQueryParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	return p
}

func (s *Server) handleContextPut(ctx context.Context, req Request) Response {
	store, fail, ok := s.requireStore(req.ID)
	if !ok {
		return fail
	}
	var p contextPutParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	stored, err := store.Put(ctx, p.entry())
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "context_put",
		"id":   stored.ID,
		"note": contextNote(stored, true),
	}}
}

func (s *Server) handleContextGet(ctx context.Context, req Request) Response {
	store, fail, ok := s.requireStore(req.ID)
	if !ok {
		return fail
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.ID) == "" {
		return errResp(req.ID, CodeInvalidParams, "id is required")
	}
	got, err := store.Get(ctx, p.ID)
	if err != nil {
		return errResp(req.ID, CodeNotFound, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "context_note",
		"note": contextNote(*got, true),
	}}
}

func (s *Server) handleContextList(ctx context.Context, req Request) Response {
	store, fail, ok := s.requireStore(req.ID)
	if !ok {
		return fail
	}
	list, err := store.List(ctx, decodeContextQuery(req).query())
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return contextListResult(req.ID, "context_list", list)
}

func (s *Server) handleContextFind(ctx context.Context, req Request) Response {
	store, fail, ok := s.requireStore(req.ID)
	if !ok {
		return fail
	}
	list, err := store.Find(ctx, decodeContextQuery(req).query())
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	return contextListResult(req.ID, "context_find", list)
}

func (s *Server) handleContextPack(ctx context.Context, req Request) Response {
	store, fail, ok := s.requireStore(req.ID)
	if !ok {
		return fail
	}
	p := decodeContextQuery(req)
	var text string
	if strings.TrimSpace(p.Group) != "" || strings.TrimSpace(p.Repo) != "" {
		text = contextstore.PackForSession(ctx, store, p.Group, p.Repo)
	} else {
		var err error
		text, err = store.Pack(ctx, p.query())
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "context_pack",
		"text": text,
	}}
}

func contextListResult(id, typ string, list []domain.ContextEntry) Response {
	notes := make([]ContextNote, 0, len(list))
	for _, e := range list {
		notes = append(notes, contextNote(e, false))
	}
	return Response{ID: id, Result: map[string]any{
		"type":  typ,
		"notes": notes,
	}}
}
