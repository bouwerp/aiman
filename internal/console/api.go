package console

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Log line bounds, as serve's harness.logs.
const (
	DefaultLogLines = 200
	MaxLogLines     = 1000
)

// maxBody bounds a request body.
const maxBody = 64 << 10

//go:embed web
var webFS embed.FS

// staticFiles maps a served path to its embedded file and type.
var staticFiles = map[string][2]string{
	"/":          {"web/index.html", "text/html; charset=utf-8"},
	"/app.js":    {"web/app.js", "text/javascript; charset=utf-8"},
	"/app.css":   {"web/app.css", "text/css; charset=utf-8"},
	"/debug.js":  {"web/debug.js", "text/javascript; charset=utf-8"},
	"/debug.css": {"web/debug.css", "text/css; charset=utf-8"},

	"/debug-browser.js":  {"web/debug-browser.js", "text/javascript; charset=utf-8"},
	"/debug-browser.css": {"web/debug-browser.css", "text/css; charset=utf-8"},
}

type apiRoute struct {
	method string
	h      func(s *Server, w http.ResponseWriter, r *http.Request)
}

var apiRoutes = map[string]apiRoute{
	"/api/state":   {http.MethodGet, (*Server).handleState},
	"/api/list":    {http.MethodGet, (*Server).handleList},
	"/api/logs":    {http.MethodGet, (*Server).handleLogs},
	"/api/verdict": {http.MethodPost, (*Server).handleVerdict},
	"/api/check":   {http.MethodPost, (*Server).handleCheck},
	"/api/up":      {http.MethodPost, (*Server).handleUp},
	"/api/down":    {http.MethodPost, (*Server).handleDown},
	"/api/debug":   {http.MethodGet, (*Server).handleDebug},
	DebugOpPath:    {http.MethodPost, (*Server).handleDebug},
	"/api/files":   {http.MethodGet, (*Server).handleFiles},
	"/api/file":    {http.MethodGet, (*Server).handleFile},
	DAPPath:        {http.MethodGet, (*Server).handleDAP},
}

func (s *Server) routes() http.Handler {
	return s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route, ok := apiRoutes[r.URL.Path]; ok {
			if r.Method != route.method {
				w.Header().Set("Allow", route.method)
				writeError(w, http.StatusMethodNotAllowed, &apiError{Code: "bad_method", Message: refusalText["bad_method"]})
				return
			}
			if r.URL.Path != DAPPath {
				r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			}
			route.h(s, w, r)
			return
		}
		f, ok := staticFiles[r.URL.Path]
		if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			writeError(w, http.StatusNotFound, &apiError{Code: "unknown_path", Message: refusalText["unknown_path"]})
			return
		}
		b, err := webFS.ReadFile(f[0])
		if err != nil {
			http.Error(w, "missing asset", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", f[1])
		_, _ = w.Write(b)
	}))
}

// apiError is how the page sees a refusal: serve's code and message, or the
// console's own.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func toAPIError(err error) *apiError {
	var re *usecase.RPCError
	switch {
	case errors.As(err, &re):
		return &apiError{Code: re.Code, Message: re.Message}
	case errors.Is(err, usecase.ErrServeNotRunning):
		return &apiError{Code: "server_not_running", Message: err.Error()}
	case errors.Is(err, usecase.ErrServeUnreachable):
		return &apiError{Code: "serve_unreachable", Message: err.Error()}
	case errors.Is(err, usecase.ErrRPCUnsupported):
		return &apiError{Code: "unsupported", Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &apiError{Code: "timeout", Message: "serve did not answer in time"}
	}
	return &apiError{Code: "rpc_failed", Message: err.Error()}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, e *apiError) {
	writeJSON(w, code, map[string]any{"error": e})
}

// relay answers with serve's result, or its refusal as 502.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, method string, params map[string]any) (json.RawMessage, bool) {
	raw, err := s.call(r.Context(), method, params)
	if err != nil {
		e := toAPIError(err)
		s.logf("%s refused: %s", method, e.Code)
		writeError(w, http.StatusBadGateway, e)
		return nil, false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
	return raw, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, &apiError{Code: "invalid_params", Message: "bad request body: " + err.Error()})
		return false
	}
	return true
}

func badParams(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, &apiError{Code: "invalid_params", Message: msg})
}

// sameRun refuses an action on a run other than the one the console shows,
// so a page that has not caught up never acts on a newer run.
func (s *Server) sameRun(w http.ResponseWriter, run string) bool {
	cur := s.currentRun()
	if cur == "" {
		writeError(w, http.StatusConflict, &apiError{Code: harness.CodeNotRunning, Message: "the console shows no run"})
		return false
	}
	if strings.TrimSpace(run) != cur {
		writeError(w, http.StatusConflict, &apiError{Code: "run_changed", Message: "the console now shows run " + cur + "; check it and try again"})
		return false
	}
	return true
}

type forwardView struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	LocalPort int    `json:"local_port"`
	HostPort  int    `json:"host_port"`
}

type stateView struct {
	Session      string        `json:"session"`
	Pinned       string        `json:"pinned,omitempty"`
	Run          *harness.Run  `json:"run"`
	Error        *apiError     `json:"error,omitempty"`
	Forwards     []forwardView `json:"forwards"`
	ForwardError string        `json:"forward_error,omitempty"`
	PolledAt     *time.Time    `json:"polled_at,omitempty"`
	// Browser is the Debug tab's Browser section (browser.go).
	Browser *browserView `json:"browser,omitempty"`
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	v := stateView{Session: s.session, Pinned: s.pinned, Run: s.run, Error: s.runErr, Forwards: s.fwds, ForwardError: s.fwErr}
	if !s.polled.IsZero() {
		t := s.polled
		v.PolledAt = &t
	}
	s.mu.Unlock()
	if v.Forwards == nil {
		v.Forwards = []forwardView{}
	}
	v.Browser = browserFor(v.Run, v.Forwards)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionName()
	if sess == "" {
		writeError(w, http.StatusConflict, &apiError{Code: "no_session", Message: "the session is not known yet"})
		return
	}
	s.relay(w, r, "harness.list", map[string]any{"session": sess})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	if service == "" {
		badParams(w, "name a service")
		return
	}
	lines := DefaultLogLines
	if l := q.Get("lines"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			badParams(w, "lines must be a positive number")
			return
		}
		lines = min(n, MaxLogLines)
	}
	if !s.sameRun(w, q.Get("run")) {
		return
	}
	s.relay(w, r, "harness.logs", map[string]any{"run": s.currentRun(), "service": service, "lines": lines})
}

var verdictResults = map[string]bool{harness.ResultPass: true, harness.ResultFail: true, harness.ResultSkip: true}

func (s *Server) handleVerdict(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Run    string `json:"run"`
		Step   int    `json:"step"`
		Result string `json:"result"`
		Note   string `json:"note"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if !verdictResults[b.Result] || b.Step < 0 {
		badParams(w, "result is pass, fail or skip; step is 0 for the scenario or a step number")
		return
	}
	if !s.sameRun(w, b.Run) {
		return
	}
	params := map[string]any{"run": b.Run, "result": b.Result}
	if b.Step > 0 {
		params["step"] = b.Step
	}
	if n := strings.TrimSpace(b.Note); n != "" {
		params["note"] = n
	}
	raw, ok := s.relay(w, r, "harness.verdict", params)
	if !ok {
		return
	}
	if run, err := decodeRun(raw); err == nil {
		s.mu.Lock()
		if s.run != nil && s.run.ID == run.ID {
			s.run = &run
		}
		s.mu.Unlock()
	}
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Run  string `json:"run"`
		Name string `json:"name"`
		// Debug runs the named check as the run's debug target; Take
		// closes a client holding the app's debugger (the page's own).
		Debug bool `json:"debug"`
		Take  bool `json:"take"`
	}
	if !decodeBody(w, r, &b) || !s.sameRun(w, b.Run) {
		return
	}
	params := map[string]any{"run": b.Run}
	if n := strings.TrimSpace(b.Name); n != "" {
		params["name"] = n
	}
	if b.Debug {
		if params["name"] == nil {
			badParams(w, "name the check to debug")
			return
		}
		params["debug"] = true
		if b.Take {
			params["take"] = true
		}
	}
	if _, ok := s.relay(w, r, "harness.check", params); ok && b.Debug {
		s.refresh(r.Context())
	}
}

func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Scenario string `json:"scenario"`
		Dirty    bool   `json:"dirty"`
		Force    bool   `json:"force"`
		Debug    bool   `json:"debug"`
		// DebugChecks starts a run that debugs only its checks.
		DebugChecks bool `json:"debug_checks"`
	}
	if !decodeBody(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.Scenario) == "" {
		badParams(w, "pick a scenario")
		return
	}
	sess := s.sessionName()
	if sess == "" {
		writeError(w, http.StatusConflict, &apiError{Code: "no_session", Message: "the session is not known yet"})
		return
	}
	params := map[string]any{"session": sess, "scenario": strings.TrimSpace(b.Scenario)}
	if b.Dirty {
		params["dirty"] = true
	}
	if b.Force {
		params["force"] = true
	}
	if b.Debug {
		params["debug"] = true
	}
	if b.DebugChecks {
		params["debug_checks"] = true
	}
	raw, ok := s.relay(w, r, "harness.up", params)
	if !ok {
		return
	}
	if run, err := decodeRun(raw); err == nil {
		s.mu.Lock()
		s.pinned = run.ID
		s.mu.Unlock()
	}
	s.refresh(r.Context())
}

func (s *Server) handleDown(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Run string `json:"run"`
	}
	if !decodeBody(w, r, &b) || !s.sameRun(w, b.Run) {
		return
	}
	if _, ok := s.relay(w, r, "harness.down", map[string]any{"run": b.Run}); ok {
		s.refresh(r.Context())
	}
}
