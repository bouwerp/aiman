package gateway

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/bouwerp/aiman/internal/server"
)

type rpcBody struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	ID     string          `json:"id"`
}

func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	var body rpcBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Method == "" {
		http.Error(w, "missing method", http.StatusBadRequest)
		return
	}
	if body.Method == "pty.attach" || body.Method == "session.events" {
		http.Error(w, body.Method+" is a websocket method", http.StatusBadRequest)
		return
	}
	if body.Method == "push.register" || body.Method == "push.unregister" {
		s.handlePushRPC(w, body)
		return
	}
	params := body.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	resp, err := server.CallRaw(s.Socket, body.Method, params)
	writeSocketResponse(w, resp, err)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	params := json.RawMessage("{}")
	if g := r.URL.Query().Get("group"); g != "" {
		b, err := json.Marshal(map[string]string{"group": g})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		params = b
	}
	resp, err := server.CallRaw(s.Socket, "session.list", params)
	writeSocketResponse(w, resp, err)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	params, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := server.CallRaw(s.Socket, "session.get", params)
	writeSocketResponse(w, resp, err)
}
