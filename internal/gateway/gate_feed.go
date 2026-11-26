package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// feedActParams holds the feed.act params the gate reads.
type feedActParams struct {
	ID     int64  `json:"id"`
	Op     string `json:"op"`
	Option int    `json:"option"`
}

// admitFeedAct lets a device without allow_destructive approve only an option
// whose effective kind is safe (domain.FeedSafeKind). Every other op passes:
// it only changes the feed. An action it cannot read refuses the approve,
// since the option's kind is then unknown.
func (s *Server) admitFeedAct(w http.ResponseWriter, body rpcBody) bool {
	var act feedActParams
	if len(body.Params) > 0 {
		if err := json.Unmarshal(body.Params, &act); err != nil {
			writeJSONError(w, http.StatusBadRequest, body.ID, server.CodeInvalidParams, err.Error())
			return false
		}
	}
	if act.Op != "approve" {
		return true
	}
	action, ok := s.readFeedAction(w, body.ID, act.ID)
	if !ok {
		return false
	}
	if act.Option < 0 || act.Option >= len(action.Options) {
		writeJSONError(w, http.StatusBadRequest, body.ID, server.CodeInvalidParams, fmt.Sprintf("action %d has no option %d", act.ID, act.Option))
		return false
	}
	kind := action.Options[act.Option].EffectiveKind(action.Kind)
	if domain.FeedSafeKind(kind) {
		return true
	}
	msg := fmt.Sprintf("approving a %s option needs allow_destructive", kind)
	writeJSONError(w, http.StatusForbidden, body.ID, "forbidden", msg, needsDestructiveData(body.Method))
	return false
}

// readFeedAction fetches one action through serve, answering the request when
// it cannot.
func (s *Server) readFeedAction(w http.ResponseWriter, reqID string, id int64) (domain.FeedAction, bool) {
	params, err := json.Marshal(map[string]int64{"id": id})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return domain.FeedAction{}, false
	}
	resp, err := serveClient(s.Socket).CallRaw("feed.actions", params)
	if err != nil || resp.Error != nil {
		resp.ID = reqID
		writeSocketResponse(w, resp, err)
		return domain.FeedAction{}, false
	}
	var result struct {
		Type    string              `json:"type"`
		Actions []domain.FeedAction `json:"actions"`
	}
	raw, err := json.Marshal(resp.Result)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	if err != nil || result.Type != "feed_actions" {
		http.Error(w, "unreadable feed action", http.StatusBadGateway)
		return domain.FeedAction{}, false
	}
	for _, a := range result.Actions {
		if a.ID == id {
			return a, true
		}
	}
	writeJSONError(w, http.StatusNotFound, reqID, server.CodeNotFound, fmt.Sprintf("feed action %d not found", id))
	return domain.FeedAction{}, false
}
