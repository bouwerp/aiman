package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bouwerp/aiman/internal/server"
)

// Server is the HTTP+WebSocket proxy in front of aiman serve's unix socket.
type Server struct {
	Socket string
	Auth   Auth
	Push   *PushStore
}

// Handler serves the phone API. GET /v1/health is unauthenticated.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/rpc", s.withAuth(s.rpc))
	mux.HandleFunc("GET /v1/sessions", s.withAuth(s.listSessions))
	mux.HandleFunc("GET /v1/sessions/{id}/terminal", s.withAuth(s.terminal))
	mux.HandleFunc("GET /v1/sessions/{id}", s.withAuth(s.getSession))
	mux.HandleFunc("GET /v1/events", s.withAuth(s.events))
	return mux
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch err := s.Auth.authorize(r); {
		case errors.Is(err, errUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case err != nil:
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			next(w, r)
		}
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
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
