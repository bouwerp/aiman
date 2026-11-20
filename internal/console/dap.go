package console

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// The Debug tab speaks the Debug Adapter Protocol over a WebSocket: one text
// message is one DAP message. The console bridges each WebSocket to one TCP
// connection on the forwarded dap port, which reaches the edge's DAP relay.
// The bridge knows no debugger; it only frames messages.
const (
	// DAPPath is the bridge's endpoint.
	DAPPath = "/api/dap"
	// DAPSubprotocol is the WebSocket subprotocol the page asks for.
	DAPSubprotocol = "aiman-dap"
	// tokenSubprotocol carries the console token: a browser cannot set a
	// header on a WebSocket, and the URL would put it in history.
	tokenSubprotocol = "aiman-token."
	// ConsoleClientName is the clientName the page's initialize sends.
	ConsoleClientName = "aiman harness console"
	dapDialTimeout    = 5 * time.Second
)

// wsToken is the console token from the Sec-WebSocket-Protocol offer.
func wsToken(r *http.Request) string {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if tok, ok := strings.CutPrefix(strings.TrimSpace(p), tokenSubprotocol); ok {
				return tok
			}
		}
	}
	return ""
}

// dapForwardPort is the laptop port of the current run's dap forward.
func (s *Server) dapForwardPort() (int, *harness.Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || !s.run.Debug {
		return 0, s.run
	}
	for _, f := range s.fwds {
		if f.Name == harness.RelayDAP {
			return f.LocalPort, s.run
		}
	}
	return 0, s.run
}

// handleDAP bridges one WebSocket to one DAP connection. Every connection of
// this console opens with the console's lease token, so the adapter's child
// sessions join the same lease at the edge.
func (s *Server) handleDAP(w http.ResponseWriter, r *http.Request) {
	port, run := s.dapForwardPort()
	if port == 0 {
		msg := "the console shows no debug run with an open debugger forward"
		if run != nil && !run.Debug {
			msg = "run " + run.ID + " was not started with the debugger"
		}
		writeError(w, http.StatusConflict, &apiError{Code: "no_debugger", Message: msg})
		return
	}
	if !s.addBridge() {
		writeError(w, http.StatusTooManyRequests, &apiError{Code: "too_many", Message: fmt.Sprintf("at most %d debugger connections", edge.MaxLeaseConns)})
		return
	}
	defer s.doneBridge()
	tcp, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), dapDialTimeout)
	if err != nil {
		writeError(w, http.StatusBadGateway, &apiError{Code: "dap_unreachable", Message: "the debugger forward does not answer: " + err.Error()})
		return
	}
	defer func() { _ = tcp.Close() }()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{DAPSubprotocol}})
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(edge.MaxDAPMessage)
	if _, err := fmt.Fprintf(tcp, "%s%s\n", edge.LeasePrefix, s.leaseToken); err != nil {
		_ = ws.Close(websocket.StatusInternalError, "debugger connection failed")
		return
	}
	s.logf("debugger connected (run %s)", run.ID)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	done := make(chan string, 2)
	go func() { done <- bridgeToDAP(ctx, ws, tcp) }()
	go func() { done <- bridgeToWS(ctx, ws, tcp) }()
	reason := <-done
	cancel()
	_ = tcp.Close()
	_ = ws.Close(websocket.StatusNormalClosure, reason)
	<-done
	s.logf("debugger disconnected (run %s): %s", run.ID, reason)
}

// bridgeToDAP frames each text message from the page onto the TCP side. A
// message that is not one JSON object ends the bridge.
func bridgeToDAP(ctx context.Context, ws *websocket.Conn, tcp net.Conn) string {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return "page closed"
		}
		if typ != websocket.MessageText || !json.Valid(data) || !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
			return "the page sent a message that is not DAP JSON"
		}
		if err := edge.WriteDAPBody(tcp, data); err != nil {
			return "debugger connection closed"
		}
	}
}

// bridgeToWS sends each DAP message from the TCP side to the page.
func bridgeToWS(ctx context.Context, ws *websocket.Conn, tcp net.Conn) string {
	br := bufio.NewReader(tcp)
	for {
		body, err := edge.ReadDAPMessage(br, edge.MaxDAPMessage)
		if err != nil {
			return "debugger connection closed"
		}
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = ws.Write(wctx, websocket.MessageText, body)
		cancel()
		if err != nil {
			return "page closed"
		}
	}
}

func (s *Server) addBridge() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridges >= edge.MaxLeaseConns {
		return false
	}
	s.bridges++
	return true
}

func (s *Server) doneBridge() {
	s.mu.Lock()
	s.bridges--
	s.mu.Unlock()
}

// ensureEditor opens the editor port once and returns it.
func (s *Server) ensureEditor() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editor == nil {
		e, err := newEditorPort(func() int {
			p, _ := s.dapForwardPort()
			return p
		})
		if err != nil {
			return 0, err
		}
		s.editor = e
	}
	return s.editor.port, nil
}

func (s *Server) closeEditor() {
	s.mu.Lock()
	e := s.editor
	s.editor = nil
	s.mu.Unlock()
	if e != nil {
		e.close()
	}
}

// DebugOpPath takes over or releases the debugger; /api/debug reads it.
const DebugOpPath = "/api/debug/op"

// debugView is /api/debug's answer: serve's debugger state plus what only
// the console knows, the laptop port and the editor configuration for it.
type debugView struct {
	Debug     json.RawMessage `json:"debug"`
	LocalPort int             `json:"local_port"`
	Editor    map[string]any  `json:"editor,omitempty"`
	// EditorPort is the console's lease-adding DAP port for an editor, set
	// for an adapter with child sessions.
	EditorPort int    `json:"editor_port,omitempty"`
	Bridges    int    `json:"bridges"`
	Client     string `json:"client_name"`
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	op := harness.DebugStatus
	run := r.URL.Query().Get("run")
	if r.URL.Path == DebugOpPath {
		var b struct {
			Run string `json:"run"`
			Op  string `json:"op"`
		}
		if !decodeBody(w, r, &b) {
			return
		}
		if b.Op != harness.DebugTake && b.Op != harness.DebugRelease {
			badParams(w, "op is take or release")
			return
		}
		op, run = b.Op, b.Run
	}
	if !s.sameRun(w, run) {
		return
	}
	raw, err := s.call(r.Context(), "harness.debug", map[string]any{"run": s.currentRun(), "op": op})
	if err != nil {
		s.writeCallError(w, "harness.debug", err)
		return
	}
	var reply struct {
		Debug json.RawMessage `json:"debug"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		writeError(w, http.StatusBadGateway, &apiError{Code: "bad_answer", Message: err.Error()})
		return
	}
	port, cur := s.dapForwardPort()
	v := debugView{Debug: reply.Debug, LocalPort: port, Client: ConsoleClientName}
	// The launch is the current debug target's: the app's, or a debug
	// check's while one runs.
	var st harness.DebugState
	_ = json.Unmarshal(reply.Debug, &st)
	if st.Launch == nil && cur != nil {
		st.Launch = cur.TargetLaunch()
	}
	// A shared console offers no editor entry: an editor on the tester's
	// machine cannot reach the host's loopback ports.
	if port > 0 && cur != nil && st.Launch != nil && !s.shared() {
		editorPort := port
		if st.Launch.EditorPort() {
			// The editor port adds the lease line, points each child session
			// at itself and maps the editor's paths to /src.
			if editorPort, err = s.ensureEditor(); err != nil {
				s.logf("editor port: %v", err)
				editorPort = 0
			}
			v.EditorPort = editorPort
		}
		if editorPort > 0 {
			if cfg, err := harness.EditorConfig(*st.Launch, editorPort); err == nil {
				v.Editor = cfg
			}
		}
	}
	s.mu.Lock()
	v.Bridges = s.bridges
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if !s.sameRun(w, r.URL.Query().Get("run")) {
		return
	}
	s.relay(w, r, "harness.files", map[string]any{"run": s.currentRun()})
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := strings.TrimSpace(q.Get("path"))
	if path == "" {
		badParams(w, "name a path")
		return
	}
	params := map[string]any{"path": path}
	if o := q.Get("offset"); o != "" {
		n, err := strconv.ParseInt(o, 10, 64)
		if err != nil || n < 0 {
			badParams(w, "offset must be a byte offset")
			return
		}
		params["offset"] = n
	}
	if !s.sameRun(w, q.Get("run")) {
		return
	}
	params["run"] = s.currentRun()
	s.relay(w, r, "harness.file", params)
}
