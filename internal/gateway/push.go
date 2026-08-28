package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/bouwerp/aiman/internal/server"
)

func (s *Server) handlePushRPC(w http.ResponseWriter, body rpcBody) {
	if s.Push == nil {
		writeRPC(w, body.ID, nil, &server.Error{Code: server.CodeInvalidParams, Message: "push store is not configured"})
		return
	}
	var params struct {
		Token    string   `json:"token"`
		DeviceID string   `json:"device_id"`
		States   []string `json:"states"`
	}
	if len(body.Params) > 0 {
		if err := json.Unmarshal(body.Params, &params); err != nil {
			writeRPC(w, body.ID, nil, &server.Error{Code: server.CodeInvalidParams, Message: "invalid params"})
			return
		}
	}
	switch body.Method {
	case "push.register":
		if err := s.Push.Register(PushDevice{Token: params.Token, DeviceID: params.DeviceID, States: params.States}); err != nil {
			writeRPC(w, body.ID, nil, &server.Error{Code: server.CodeInvalidParams, Message: err.Error()})
			return
		}
		writeRPC(w, body.ID, map[string]any{"type": "push_registered", "registered": true}, nil)
	case "push.unregister":
		if err := s.Push.Unregister(params.Token, params.DeviceID); err != nil {
			writeRPC(w, body.ID, nil, &server.Error{Code: server.CodeInvalidParams, Message: err.Error()})
			return
		}
		writeRPC(w, body.ID, map[string]any{"type": "push_unregistered"}, nil)
	}
}

func writeRPC(w http.ResponseWriter, id string, result any, rpcErr *server.Error) {
	status := http.StatusOK
	if rpcErr != nil {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(server.Response{ID: id, Result: result, Error: rpcErr})
}
