package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// GatewayAnnounceMaxAge is how long an announce counts as the gateway running.
// The gateway announces every minute.
const GatewayAnnounceMaxAge = 3 * time.Minute

// harnessShareStore is what the share methods need from the repository.
type harnessShareStore interface {
	SaveHarnessShare(ctx context.Context, s domain.HarnessShare) error
	GetHarnessShare(ctx context.Context, id string) (domain.HarnessShare, error)
	ListLiveHarnessShares(ctx context.Context, session string) ([]domain.HarnessShare, error)
	RevokeHarnessShares(ctx context.Context, session string, at time.Time) (int, error)
	SaveGatewayAnnounce(ctx context.Context, a domain.GatewayAnnounce) error
	GetGatewayAnnounce(ctx context.Context) (domain.GatewayAnnounce, bool, error)
}

// shareEnded remembers the shares whose end the hub already heard of.
type shareEnded struct {
	mu  sync.Mutex
	ids map[string]bool
}

// first reports whether id is newly marked.
func (e *shareEnded) first(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ids == nil {
		e.ids = map[string]bool{}
	}
	if e.ids[id] {
		return false
	}
	e.ids[id] = true
	return true
}

func (s *Server) shareStore() (harnessShareStore, bool) {
	st, ok := s.repo.(harnessShareStore)
	return st, ok
}

// cleanTesterLogin trims a tester login and drops control characters, so it
// is safe in a ledger actor and a steering note.
func cleanTesterLogin(in string) string {
	in = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, in)
	if len(in) > 200 {
		in = in[:200]
	}
	return in
}

// dispatchHarnessShare routes gateway.announce and the harness share methods.
func (s *Server) dispatchHarnessShare(ctx context.Context, req Request) Response {
	store, ok := s.shareStore()
	if !ok {
		return errResp(req.ID, CodeUnavailable, "this serve's database keeps no harness shares")
	}
	if req.Method == "gateway.announce" {
		return s.handleGatewayAnnounce(ctx, req, store)
	}
	if s.harness == nil {
		return errResp(req.ID, CodeUnavailable, "the harness is not enabled on this serve (harness.enabled, or no docker)")
	}
	var p harnessParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Session) == "" {
		p.Session = req.Caller
	}
	switch req.Method {
	case "harness.share":
		return s.handleHarnessShare(ctx, req, p, store)
	case "harness.unshare":
		return s.handleHarnessUnshare(ctx, req, p, store)
	default:
		return s.handleHarnessShareGet(ctx, req, p, store)
	}
}

func (s *Server) handleGatewayAnnounce(ctx context.Context, req Request, store harnessShareStore) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator announces the gateway")
	}
	var p struct {
		Base        string `json:"base"`
		ConsolePort int    `json:"console_port"`
		Scheme      string `json:"scheme"`
		PID         int    `json:"pid"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	base := strings.TrimRight(strings.TrimSpace(p.Base), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Port() != "" {
		return errResp(req.ID, CodeInvalidParams, "base must be scheme://host with no port, path or credentials")
	}
	if p.Scheme != "http" && p.Scheme != "https" || u.Scheme != p.Scheme {
		return errResp(req.ID, CodeInvalidParams, "scheme must be http or https and match base")
	}
	if p.ConsolePort < 1 || p.ConsolePort > 65535 {
		return errResp(req.ID, CodeInvalidParams, "console_port must be 1-65535")
	}
	a := domain.GatewayAnnounce{Base: base, ConsolePort: p.ConsolePort, Scheme: p.Scheme, PID: p.PID, At: time.Now().UTC()}
	if err := store.SaveGatewayAnnounce(ctx, a); err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "gateway_announced"}}
}

func (s *Server) publishShare(id, state string) {
	if s.hub == nil {
		return
	}
	if s.harness != nil && state == "ended" {
		s.harness.shareEnded.first(id + "/ended") // share_get must not repeat it
	}
	s.hub.Publish(SessionEvent{Type: "harness_share", ID: id, State: state})
}

func (s *Server) handleHarnessShare(ctx context.Context, req Request, p harnessParams, store harnessShareStore) Response {
	sess, fail := s.harnessSession(ctx, req, p.Session)
	if fail != nil {
		return *fail
	}
	m := s.harness.d.Manager
	run, ok := m.LiveRun(sess.ID)
	if !ok || run.State != harness.StateReady {
		return errResp(req.ID, harness.CodeNotRunning, "session "+sess.ID+" has no ready harness run")
	}
	if want := strings.TrimSpace(p.Run); want != "" && want != run.ID {
		return errResp(req.ID, harness.CodeNotRunning, "run "+want+" is not the session's ready run")
	}
	a, announced, err := store.GetGatewayAnnounce(ctx)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	if !announced || time.Since(a.At) > GatewayAnnounceMaxAge {
		return errResp(req.ID, CodeUnavailable, "the gateway is not running on this host (gateway is not running: start aiman-gateway)")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	id := base64.RawURLEncoding.EncodeToString(buf)
	s.revokeShares(ctx, store, sess.ID)
	now := time.Now().UTC()
	share := domain.HarnessShare{ID: id, Session: sess.ID, Run: run.ID, CreatedAt: now}
	if ttl := s.shareTTL(); ttl > 0 {
		capAt := now.Add(ttl)
		share.CapAt = &capAt
	}
	if err := store.SaveHarnessShare(ctx, share); err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	s.appendEvent(ctx, sess.ID, s.harnessActor(ctx, req), domain.EventHarnessShared, map[string]any{
		"session": sess.ID, "run": run.ID, "share": id,
	})
	s.publishShare(id, "live")
	return Response{ID: req.ID, Result: map[string]any{
		"type": "harness_share", "share": id, "run": run.ID,
		"url": fmt.Sprintf("%s:%d/s/%s", a.Base, a.ConsolePort, id),
	}}
}

// shareTTL is how long a new share lives; zero means no cap.
func (s *Server) shareTTL() time.Duration {
	if s.harness != nil && s.harness.d.ShareTTL > 0 {
		return s.harness.d.ShareTTL
	}
	return config.DefaultHarnessShareTTL
}

// revokeShares ends the session's live shares and tells the hub.
func (s *Server) revokeShares(ctx context.Context, store harnessShareStore, session string) int {
	live, err := store.ListLiveHarnessShares(ctx, session)
	if err != nil {
		log.Printf("harness shares of %s: %v", session, err)
	}
	n, err := store.RevokeHarnessShares(ctx, session, time.Now().UTC())
	if err != nil {
		log.Printf("revoking harness shares of %s: %v", session, err)
	}
	for _, sh := range live {
		s.publishShare(sh.ID, "ended")
	}
	return n
}

func (s *Server) handleHarnessUnshare(ctx context.Context, req Request, p harnessParams, store harnessShareStore) Response {
	sess, fail := s.harnessSession(ctx, req, p.Session)
	if fail != nil {
		return *fail
	}
	n := s.revokeShares(ctx, store, sess.ID)
	return Response{ID: req.ID, Result: map[string]any{"type": "harness_unshared", "revoked": n}}
}

func (s *Server) handleHarnessShareGet(ctx context.Context, req Request, p harnessParams, store harnessShareStore) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator reads a share's state")
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return errResp(req.ID, CodeInvalidParams, "id is required")
	}
	sh, err := store.GetHarnessShare(ctx, id)
	if errors.Is(err, sqlite.ErrShareNotFound) {
		return errResp(req.ID, CodeNotFound, "no such share")
	}
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	state := "live"
	switch {
	case sh.RevokedAt != nil:
		state = "ended"
	case sh.CapAt != nil && !time.Now().Before(*sh.CapAt):
		state = "ended"
	default:
		if run, err := s.harness.d.Manager.Get(sh.Run); err != nil || run.State != harness.StateReady {
			state = "ended"
		}
	}
	if state == "ended" && s.harness.shareEnded.first(sh.ID+"/ended") && s.hub != nil {
		s.hub.Publish(SessionEvent{Type: "harness_share", ID: sh.ID, State: "ended"})
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "harness_share_state", "share": sh.ID, "session": sh.Session, "run": sh.Run, "state": state,
	}}
}
