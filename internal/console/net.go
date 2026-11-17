package console

import (
	"net/http"
	"strconv"
)

// Capture read bounds, as serve's harness.capture.
const (
	DefaultCaptureLimit = 50
	MaxCaptureLimit     = 200
)

// handleCapture relays harness.capture for the shown run: the Network tab's
// flows with an ID above after.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := map[string]any{"run": s.currentRun(), "limit": DefaultCaptureLimit}
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			badParams(w, "after must be a flow id (0 or more)")
			return
		}
		params["after_id"] = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			badParams(w, "limit must be a positive number")
			return
		}
		params["limit"] = min(n, MaxCaptureLimit)
	}
	if !s.sameRun(w, q.Get("run")) {
		return
	}
	s.relay(w, r, "harness.capture", params)
}
