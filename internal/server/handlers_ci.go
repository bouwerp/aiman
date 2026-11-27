package server

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/domain"
)

// CIInfo is a session's PR watch as ci.get and ci.watch report it.
type CIInfo struct {
	Watch      bool     `json:"watch"`
	PRNumber   int      `json:"pr_number,omitempty"`
	PRState    string   `json:"pr_state,omitempty"`
	HeadSHA    string   `json:"head_sha,omitempty"`
	Escalated  []string `json:"escalated,omitempty"`
	LastPolled string   `json:"last_polled_at,omitempty"` // RFC3339
	LastError  string   `json:"last_error,omitempty"`
	// Repos is one entry per member of a workspace session, in Ord order;
	// the scalar fields above are then empty. A plain session has none.
	Repos []CIRepoInfo `json:"repos,omitempty"`
}

// CIRepoInfo is one workspace member's PR watch.
type CIRepoInfo struct {
	Repo       string   `json:"repo"`
	PRNumber   int      `json:"pr_number"`
	PRState    string   `json:"pr_state,omitempty"`
	HeadSHA    string   `json:"head_sha,omitempty"`
	Escalated  []string `json:"escalated,omitempty"`
	LastPolled string   `json:"last_polled_at,omitempty"` // RFC3339
	LastError  string   `json:"last_error,omitempty"`
}

// SetCIWatcher attaches the PR watcher. Must be called before Serve; without
// it ci.get and ci.watch fail.
func (s *Server) SetCIWatcher(w *ciwatch.Watcher) {
	s.ciWatch = w
}

type ciParams struct {
	Session string `json:"session"`
	On      *bool  `json:"on"`
}

// ciRequest decodes params and resolves the session for the ci methods.
func (s *Server) ciRequest(ctx context.Context, req Request) (ciParams, domain.Session, *Response) {
	var p ciParams
	if s.ciWatch == nil {
		r := errResp(req.ID, CodeInvalidParams, "ci watcher not running")
		return p, domain.Session{}, &r
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			r := errResp(req.ID, CodeInvalidParams, err.Error())
			return p, domain.Session{}, &r
		}
	}
	if strings.TrimSpace(p.Session) == "" {
		r := errResp(req.ID, CodeInvalidParams, "session is required")
		return p, domain.Session{}, &r
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		r := errResp(req.ID, CodeInvalidParams, err.Error())
		return p, domain.Session{}, &r
	}
	sess, ok := resolveSession(list, p.Session)
	if !ok {
		r := errResp(req.ID, CodeNotFound, "session not found")
		return p, domain.Session{}, &r
	}
	return p, sess, nil
}

func (s *Server) handleCIGet(ctx context.Context, req Request) Response {
	_, sess, fail := s.ciRequest(ctx, req)
	if fail != nil {
		return *fail
	}
	return s.ciResponse(ctx, req.ID, sess)
}

// handleCIWatch restarts the session's watch state, then stores the explicit
// on/off choice on it and, when on, polls at once. The flag is saved last so
// a failed reset never leaves it claiming a watch the rows do not match.
func (s *Server) handleCIWatch(ctx context.Context, req Request) Response {
	p, sess, fail := s.ciRequest(ctx, req)
	if fail != nil {
		return *fail
	}
	if p.On == nil {
		return errResp(req.ID, CodeInvalidParams, "on is required")
	}
	sess.WatchPR = domain.WatchPROff
	if *p.On {
		sess.WatchPR = domain.WatchPROn
	}
	if err := s.ciWatch.SetWatch(ctx, sess.ID, *p.On); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if s.repo != nil {
		if err := s.repo.Save(ctx, &sess); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if *p.On {
		s.ciWatch.Kick(sess.ID)
	}
	return s.ciResponse(ctx, req.ID, sess)
}

func (s *Server) ciResponse(ctx context.Context, id string, sess domain.Session) Response {
	if sess.IsWorkspace() {
		return s.ciWorkspaceResponse(ctx, id, sess)
	}
	st, err := s.ciWatch.Status(ctx, sess.ID)
	if err != nil {
		return errResp(id, CodeInvalidParams, err.Error())
	}
	info := CIInfo{
		Watch:     sess.WatchesPR(),
		PRNumber:  st.PRNumber,
		PRState:   st.PRState,
		HeadSHA:   st.HeadSHA,
		Escalated: st.Escalated,
		LastError: st.LastError,
	}
	if !st.LastPolledAt.IsZero() {
		info.LastPolled = st.LastPolledAt.UTC().Format(time.RFC3339)
	}
	return Response{ID: id, Result: map[string]any{"type": "ci", "ci": info}}
}

// ciWorkspaceResponse lists each member's watch state, a member never polled
// included, with no top-level PR.
func (s *Server) ciWorkspaceResponse(ctx context.Context, id string, sess domain.Session) Response {
	members := append([]domain.SessionRepo(nil), sess.Members...)
	sort.SliceStable(members, func(i, j int) bool { return members[i].Ord < members[j].Ord })
	info := CIInfo{Watch: sess.WatchesPR(), Repos: make([]CIRepoInfo, 0, len(members))}
	for _, m := range members {
		st, err := s.ciWatch.StatusRepo(ctx, sess.ID, m.Repo)
		if err != nil {
			return errResp(id, CodeInvalidParams, err.Error())
		}
		r := CIRepoInfo{
			Repo:      m.Repo,
			PRNumber:  st.PRNumber,
			PRState:   st.PRState,
			HeadSHA:   st.HeadSHA,
			Escalated: st.Escalated,
			LastError: st.LastError,
		}
		if !st.LastPolledAt.IsZero() {
			r.LastPolled = st.LastPolledAt.UTC().Format(time.RFC3339)
		}
		info.Repos = append(info.Repos, r)
	}
	return Response{ID: id, Result: map[string]any{"type": "ci", "ci": info}}
}
