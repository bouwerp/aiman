package gateway

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/server"
)

// restartServeCooldown is how long after an accepted gateway.restart_serve a
// second call is refused with reason "cooldown".
const restartServeCooldown = 60 * time.Second

// restartServeResult is gateway.restart_serve's answer.
type restartServeResult struct {
	Type     string `json:"type"`
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// handleRestartServe answers gateway.restart_serve without calling serve: it
// is the phone's way out when serve is alive but not answering. admitDevice has
// already required the host token or allow_destructive.
func (s *Server) handleRestartServe(w http.ResponseWriter, body rpcBody) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	now := s.clock()
	if !s.lastRestart.IsZero() && now.Sub(s.lastRestart) < restartServeCooldown {
		writeRPC(w, body.ID, restartServeResult{Type: "gateway_restart_serve", Reason: "cooldown"}, nil)
		return
	}
	run := s.RestartServe
	if run == nil {
		run = startServeRestart
	}
	if err := run(); err != nil {
		writeRPC(w, body.ID, nil, &server.Error{Code: "internal", Message: err.Error()})
		return
	}
	s.lastRestart = now
	writeRPC(w, body.ID, restartServeResult{Type: "gateway_restart_serve", Accepted: true}, nil)
}

// startServeRestart runs remotesvc.RestartScript(serve) detached (nohup, a
// session of its own), so it outlives this request and the gateway, appending
// its output to ~/.aiman/logs/restart-serve-gateway.log.
func startServeRestart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".aiman", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(dir, "restart-serve-gateway.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: fixed path under ~/.aiman
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command("nohup", "bash", "-c", remotesvc.RestartScript(remotesvc.KindServe)) //nolint:gosec // G204: a fixed script built from constants
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart serve: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
