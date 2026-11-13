package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

// Server is the HTTP+WebSocket proxy in front of aiman serve's unix socket.
type Server struct {
	Socket    string
	Auth      Auth
	Push      *PushStore
	PairCodes *PairCodes

	// RestartServe restarts serve for gateway.restart_serve; nil runs
	// remotesvc.RestartScript(serve) detached.
	RestartServe func() error

	// now is the clock for the pairing lockout and the restart cooldown; nil
	// means time.Now.
	now        func() time.Time
	pairMu     sync.Mutex
	pairFailed []time.Time

	restartMu   sync.Mutex
	lastRestart time.Time
}

// Handler serves the phone API. GET /v1/health is unauthenticated, and
// POST /v1/pair needs a pairing code and the tailnet identity instead of a
// token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("POST /v1/rpc", s.withAuth(false, s.rpc))
	mux.HandleFunc("GET /v1/sessions", s.withAuth(false, s.listSessions))
	mux.HandleFunc("GET /v1/sessions/{id}/terminal", s.withAuth(true, s.terminal))
	mux.HandleFunc("GET /v1/sessions/{id}", s.withAuth(false, s.getSession))
	mux.HandleFunc("GET /v1/events", s.withAuth(true, s.events))
	mux.HandleFunc("GET /v1/devices", s.withAuth(false, s.listDevices))
	mux.HandleFunc("DELETE /v1/devices/{id}", s.withAuth(false, s.revokeDevice))
	return mux
}

// withAuth admits the host token or a device token. allowQuery also takes
// the token from access_token, for the WebSocket routes.
func (s *Server) withAuth(allowQuery bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := s.Auth.authorize(r, requestToken(r, allowQuery))
		switch {
		case errors.Is(err, errUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case err != nil:
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			next(w, r.WithContext(withPrincipal(r.Context(), p)))
		}
	}
}

func (s *Server) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// serveClient is how the gateway reaches serve. It sends no caller: a phone is
// never a session, even when the gateway was started from a session's terminal
// and inherited its AIMAN_ID.
func serveClient(socket string) server.Client {
	return server.Client{Socket: socket}
}

func writeSocketResponse(w http.ResponseWriter, resp server.Response, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeJSONError answers with status and a server.Response error body. data,
// when given, is the error's structured detail.
func writeJSONError(w http.ResponseWriter, status int, id, code, msg string, data ...any) {
	e := &server.Error{Code: code, Message: msg}
	if len(data) > 0 {
		e.Data = data[0]
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(server.Response{ID: id, Error: e})
}

// needsDestructive is the data of a refusal that allow_destructive would lift.
type needsDestructive struct {
	Method string `json:"method,omitempty"`
	Needs  string `json:"needs"`
}

func needsDestructiveData(method string) needsDestructive {
	return needsDestructive{Method: method, Needs: "allow_destructive"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
