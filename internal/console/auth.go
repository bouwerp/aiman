package console

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// TokenHeader carries the console token on every API request. The page reads
// the token from the URL fragment, keeps it in sessionStorage (per origin, so
// a forwarded app on another laptop port cannot read it) and sends it here.
// It is never a cookie: a cookie for 127.0.0.1 would also go to every
// forwarded app port.
const TokenHeader = "X-Aiman-Console-Token" //nolint:gosec // G101: a header name, not a credential

// pageCSP allows only the console's own embedded script and style, and the
// API and the debugger WebSocket on the same origin. The ws: sources are
// spelled out: older browsers do not count a WebSocket as 'self'.
func pageCSP(port int) string {
	p := strconv.Itoa(port)
	return "default-src 'none'; script-src 'self'; style-src 'self'; " +
		"connect-src 'self' ws://127.0.0.1:" + p + " ws://localhost:" + p + "; " +
		"img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

// statusWriter records the status for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Hijack hands the connection to the debugger WebSocket; the status of a
// hijacked request is 101.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("console: the connection cannot be hijacked")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the server's writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// guard wraps every request: security headers, the Host check, and for the
// API the token, then for writes the Origin and JSON checks. It logs refusals
// and failed requests by method, known path and status, never headers or the
// query.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		h := sw.Header()
		h.Set("Content-Security-Policy", pageCSP(s.port))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if reason := s.refusal(r); reason != "" {
			writeError(sw, refusalStatus[reason], &apiError{Code: reason, Message: refusalText[reason]})
			s.logf("refused %s %s: %s", r.Method, logPath(r.URL.Path), reason)
			return
		}
		next.ServeHTTP(sw, r)
		// The page polls every 2s; only failures are worth a line.
		if sw.status >= http.StatusBadRequest {
			s.logf("%s %s %d", r.Method, logPath(r.URL.Path), sw.status)
		}
	})
}

var refusalStatus = map[string]int{
	"bad_host":     http.StatusForbidden,
	"bad_token":    http.StatusUnauthorized,
	"bad_origin":   http.StatusForbidden,
	"not_json":     http.StatusUnsupportedMediaType,
	"bad_method":   http.StatusMethodNotAllowed,
	"unknown_path": http.StatusNotFound,
}

var refusalText = map[string]string{ //nolint:gosec // G101: user-facing messages, no credential
	"bad_host":     "this console answers only on 127.0.0.1 or localhost at its own port",
	"bad_token":    "open the URL the console printed; the token is missing or wrong",
	"bad_origin":   "a change must come from the console page itself",
	"not_json":     "send application/json",
	"bad_method":   "method not allowed",
	"unknown_path": "not found",
}

// refusal names why a request is refused, or is empty.
func (s *Server) refusal(r *http.Request) string {
	if !s.hostOK(r.Host) {
		return "bad_host"
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return ""
	}
	if r.URL.Path == DAPPath {
		// A WebSocket handshake is a GET that a hostile page can start too:
		// the token comes in the subprotocol offer and the Origin must be
		// this console's.
		if !s.tokenOK(wsToken(r)) {
			return "bad_token"
		}
		if !s.originOK(r.Header.Get("Origin")) {
			return "bad_origin"
		}
		return ""
	}
	if !s.tokenOK(r.Header.Get(TokenHeader)) {
		return "bad_token"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return ""
	}
	if !s.originOK(r.Header.Get("Origin")) {
		return "bad_origin"
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return "not_json"
	}
	return ""
}

// hostOK accepts exactly 127.0.0.1:<port> and localhost:<port>, which stops
// DNS rebinding: a page on another name reaching this port has another Host.
func (s *Server) hostOK(host string) bool {
	p := ":" + strconv.Itoa(s.port)
	return host == "127.0.0.1"+p || strings.EqualFold(host, "localhost"+p)
}

func (s *Server) originOK(origin string) bool {
	p := ":" + strconv.Itoa(s.port)
	return origin == "http://127.0.0.1"+p || strings.EqualFold(origin, "http://localhost"+p)
}

func (s *Server) tokenOK(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// logPath is the path when it is one the console serves, so a token pasted
// into a path never reaches the log.
func logPath(p string) string {
	if _, ok := staticFiles[p]; ok {
		return p
	}
	if _, ok := apiRoutes[p]; ok {
		return p
	}
	return "(other)"
}
