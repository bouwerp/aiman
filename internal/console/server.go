// Package console is the laptop's web console for a harness run: a local
// HTTP server on 127.0.0.1 that serves an embedded page and a small JSON API.
// The API calls the host's serve as the operator through an RPC and holds the
// ssh forwards to the run's relay ports.
package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/usecase"
)

// PollEvery is how often the console asks serve for the run's state.
const PollEvery = 2 * time.Second

// RPC calls one harness method on the host's serve as the operator.
type RPC interface {
	Call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
}

// Forwards is one set of open forwards to a run's relay ports.
type Forwards interface {
	Forwards() []usecase.HarnessForward
	// Died receives a forward whose child exited.
	Died() <-chan usecase.HarnessForward
	Close()
}

// Forwarder opens forwards to a run's relay ports. The children live until
// Close or until the context given to Open ends.
type Forwarder interface {
	Open(ctx context.Context, ports []usecase.HarnessPort) (Forwards, error)
}

// SSHForwarder opens forwards with usecase.HarnessForwarder.
type SSHForwarder struct {
	F usecase.HarnessForwarder
}

// Open starts one ssh child per port.
func (s SSHForwarder) Open(ctx context.Context, ports []usecase.HarnessPort) (Forwards, error) {
	h, err := s.F.Open(ctx, ports)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// Options configure a console. Session or Run names what it shows.
type Options struct {
	RPC       RPC
	Forwarder Forwarder
	Session   string
	Run       string
	// Token is the per-console secret; empty makes a random one.
	Token string
	// Log receives one line per request and per refusal. It never holds the
	// token, a header value or a query string.
	Log io.Writer
}

// Server is one console.
type Server struct {
	rpc     RPC
	fwd     Forwarder
	token   string
	port    int
	ln      net.Listener
	log     io.Writer
	handler http.Handler

	// refreshMu serialises refresh, which owns fw.
	refreshMu sync.Mutex
	fw        Forwards
	fwRun     string
	// fwCtx is the context forward children live in; Serve sets it.
	fwCtx context.Context

	mu      sync.Mutex
	session string
	// pinned is the run the console follows: --run, then each run it starts.
	// Empty follows the session's live, else newest, run.
	pinned string
	run    *harness.Run
	runErr *apiError
	fwErr  string
	fwds   []forwardView
	polled time.Time
	// bridges counts open debugger WebSockets.
	bridges int
	// leaseToken is this console's debugger lease: every bridge sends it, so
	// an adapter's child sessions join the holder's lease at the edge.
	leaseToken string
}

// New makes a console that will serve on ln, which must listen on 127.0.0.1.
func New(o Options, ln net.Listener) (*Server, error) {
	if o.RPC == nil || o.Forwarder == nil {
		return nil, errors.New("console: RPC and Forwarder are required")
	}
	if o.Session == "" && o.Run == "" {
		return nil, errors.New("console: name a session or a run")
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		return nil, fmt.Errorf("console: listener %s is not on 127.0.0.1", ln.Addr())
	}
	tok := o.Token
	if tok == "" {
		var err error
		if tok, err = newToken(); err != nil {
			return nil, err
		}
	}
	log := o.Log
	if log == nil {
		log = io.Discard
	}
	lease, err := newToken()
	if err != nil {
		return nil, err
	}
	s := &Server{
		rpc: o.RPC, fwd: o.Forwarder, token: tok, port: addr.Port, ln: ln, log: log,
		session: o.Session, pinned: o.Run, fwCtx: context.Background(), leaseToken: lease,
	}
	s.handler = s.routes()
	return s, nil
}

// newToken is 32 random bytes, base64url without padding.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("console token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// URL is the page with the token in the fragment, which the browser never
// sends to a server.
func (s *Server) URL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port) + "/#token=" + s.token
}

// Handler is the console's HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Serve polls serve and answers requests until ctx ends, then stops the HTTP
// server and closes the forwards.
func (s *Server) Serve(parent context.Context) error {
	ctx, stop := context.WithCancel(parent)
	s.refreshMu.Lock()
	s.fwCtx = ctx
	s.refreshMu.Unlock()
	polled := make(chan struct{})
	defer func() {
		stop()
		<-polled
		s.closeForwards()
	}()

	hs := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          stdlog.New(s.log, "console: ", 0),
	}
	go func() {
		defer close(polled)
		s.pollLoop(ctx)
	}()
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(s.ln) }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = hs.Shutdown(shut)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) pollLoop(ctx context.Context) {
	t := time.NewTicker(PollEvery)
	defer t.Stop()
	for {
		s.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.log, time.Now().Format("15:04:05")+" "+format+"\n", args...)
}

// target is what harness.status, list and up are asked about.
func (s *Server) target() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinned != "" {
		return map[string]any{"run": s.pinned}
	}
	return map[string]any{"session": s.session}
}

func (s *Server) sessionName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

// currentRun is the id of the run the console shows, or empty.
func (s *Server) currentRun() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		return ""
	}
	return s.run.ID
}

// refresh reads the run from serve and keeps the forwards in step with it:
// open while the run is ready, reopened after a child dies or the run
// changes, closed otherwise.
func (s *Server) refresh(ctx context.Context) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	raw, err := s.call(ctx, "harness.status", s.target())
	var run *harness.Run
	var runErr *apiError
	if err != nil {
		runErr = toAPIError(err)
		if runErr.Code != harness.CodeNotRunning {
			s.logf("harness.status failed: %s", runErr.Code)
		}
	} else if r, derr := decodeRun(raw); derr != nil {
		runErr = &apiError{Code: "bad_answer", Message: derr.Error()}
	} else {
		run = &r
	}
	fwds, fwErr := s.syncForwards(run)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run, s.runErr, s.polled = run, runErr, time.Now()
	s.fwds, s.fwErr = fwds, fwErr
	if run != nil && s.session == "" {
		s.session = run.Session
	}
}

// syncForwards runs under refreshMu.
func (s *Server) syncForwards(run *harness.Run) ([]forwardView, string) {
	note := ""
	if s.fw != nil {
		select {
		case f := <-s.fw.Died():
			note = fmt.Sprintf("forward %s ended; reopening", f.Name)
			s.logf("forward %s (laptop port %d) ended", f.Name, f.LocalPort)
			s.dropForwards()
		default:
		}
	}
	if run == nil || run.State != harness.StateReady || len(run.Ports) == 0 || s.fwCtx.Err() != nil {
		s.dropForwards()
		return nil, ""
	}
	if s.fw != nil && s.fwRun != run.ID {
		s.dropForwards()
	}
	if s.fw == nil {
		ports := make([]usecase.HarnessPort, 0, len(run.Ports))
		for _, p := range run.Ports {
			ports = append(ports, usecase.HarnessPort{Name: p.Name, HostPort: p.HostPort})
		}
		fw, err := s.fwd.Open(s.fwCtx, ports)
		if err != nil {
			s.logf("forwards for run %s failed", run.ID)
			return nil, "forwards failed: " + err.Error()
		}
		s.fw, s.fwRun = fw, run.ID
	}
	out := make([]forwardView, 0, len(run.Ports))
	for _, f := range s.fw.Forwards() {
		out = append(out, forwardView{Name: f.Name, URL: f.URL(), LocalPort: f.LocalPort, HostPort: f.HostPort})
	}
	return out, note
}

// dropForwards runs under refreshMu.
func (s *Server) dropForwards() {
	if s.fw != nil {
		s.fw.Close()
		s.fw, s.fwRun = nil, ""
	}
}

func (s *Server) closeForwards() {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.dropForwards()
}

type runReply struct {
	Run *harness.Run `json:"run"`
}

func decodeRun(raw json.RawMessage) (harness.Run, error) {
	var r runReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return harness.Run{}, fmt.Errorf("decode run: %w", err)
	}
	if r.Run == nil || r.Run.ID == "" {
		return harness.Run{}, errors.New("answer has no run")
	}
	return *r.Run, nil
}

// call sends one RPC; serve's refusals come back as *usecase.RPCError.
func (s *Server) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return s.rpc.Call(ctx, method, params)
}
