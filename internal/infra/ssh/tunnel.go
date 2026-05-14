package ssh

// SSH port-forwarding tunnels via a managed ssh -N -L subprocess.
//
// We start ssh -N -L as a subprocess (no -f), so we own the process lifetime:
//   - We can kill it explicitly on StopTunnel.
//   - A background goroutine watches for unexpected exit and cleans up state.
//   - Setsid isolates it from any SIGHUP sent to the parent process group.
//   - The slave shares the existing ControlMaster connection (-S socket), so
//     no new TCP handshake or auth is needed.
//   - If the ControlMaster socket is stale, ssh falls back to a direct
//     connection using ~/.ssh/config and the SSH agent.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type activeTunnel struct {
	cmd    *exec.Cmd
	doneCh chan struct{} // closed when the subprocess exits
}

var (
	tunnelsMu sync.Mutex
	tunnels   = make(map[string]*activeTunnel)
	// tunnelLastErrors stores the last failure reason for a tunnel key so
	// post-startup exits (after StartTunnel returned nil) can be surfaced in
	// the next refresh cycle.
	tunnelLastErrors = make(map[string]string)
)

func tunnelKey(localPort, remotePort int) string {
	return fmt.Sprintf("%d:%d", localPort, remotePort)
}

// StartTunnel launches an ssh -N -L subprocess that forwards
// 127.0.0.1:localPort on this machine to 127.0.0.1:remotePort on the remote.
//
// The subprocess inherits the environment (SSH_AUTH_SOCK etc.) and uses the
// system ssh binary, which reads ~/.ssh/config. It runs in its own session
// (Setsid) so it is not killed by SIGHUP on terminal close.
func (m *Manager) StartTunnel(ctx context.Context, localPort, remotePort int) error {
	key := tunnelKey(localPort, remotePort)

	tunnelsMu.Lock()
	if _, exists := tunnels[key]; exists {
		tunnelsMu.Unlock()
		return nil
	}
	tunnelsMu.Unlock()

	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	target := m.target()

	var stderrBuf strings.Builder

	// Run as a standalone ssh -N -L process. Using a direct connection (no -S
	// socket) avoids ControlMaster mux quirks and keeps the tunnel alive
	// independently of the control master's ControlPersist lifetime.
	// SSH reads ~/.ssh/config and the SSH agent for auth automatically.
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-N",
		"-L", forward,
		target,
	)
	cmd.Stderr = &stderrBuf
	cmd.SysProcAttr = sysProcAttrSetsid()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start tunnel L%d->R%d: %w", localPort, remotePort, err)
	}

	t := &activeTunnel{cmd: cmd, doneCh: make(chan struct{})}
	tunnelsMu.Lock()
	tunnels[key] = t
	tunnelsMu.Unlock()

	// Watch for unexpected tunnel death and clean up the map.
	// Also used to detect immediate startup failures.
	exitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		detail := sshErrorSummary(stderrBuf.String())
		tunnelsMu.Lock()
		if current, ok := tunnels[key]; ok && current == t {
			delete(tunnels, key)
			// Store the exit reason so the next refresh can surface it.
			if detail != "" {
				tunnelLastErrors[key] = detail
			} else if err != nil {
				tunnelLastErrors[key] = err.Error()
			}
		}
		close(t.doneCh)
		tunnelsMu.Unlock()
		exitCh <- err
	}()

	// Give the process 2s to fail fast (port conflict, auth error, etc.).
	select {
	case err := <-exitCh:
		tunnelsMu.Lock()
		delete(tunnels, key)
		tunnelsMu.Unlock()
		detail := sshErrorSummary(stderrBuf.String())
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return fmt.Errorf("tunnel L%d->R%d failed: %s", localPort, remotePort, detail)
	case <-time.After(2 * time.Second):
		// Process is still running — tunnel is up; clear any previous error.
		tunnelsMu.Lock()
		delete(tunnelLastErrors, key)
		tunnelsMu.Unlock()
		return nil
	}
}

// TunnelLastError returns the last exit reason for a tunnel that died
// unexpectedly (either during startup or after). Empty string if none.
func (m *Manager) TunnelLastError(localPort, remotePort int) string {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	defer tunnelsMu.Unlock()
	return tunnelLastErrors[key]
}

// ClearTunnelError clears the stored last-error for a tunnel key.
func (m *Manager) ClearTunnelError(localPort, remotePort int) {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	delete(tunnelLastErrors, key)
	tunnelsMu.Unlock()
}

// StopTunnel kills the ssh subprocess and removes the tunnel from the map.
func (m *Manager) StopTunnel(_ context.Context, localPort, remotePort int) error {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	t, ok := tunnels[key]
	if ok {
		delete(tunnels, key)
	}
	tunnelsMu.Unlock()

	if !ok || t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	if err := t.cmd.Process.Kill(); err != nil {
		if !strings.Contains(err.Error(), "process already finished") {
			return fmt.Errorf("failed to stop tunnel L%d->R%d: %w", localPort, remotePort, err)
		}
	}
	return nil
}

// IsTunnelRunning returns true if the ssh subprocess is still alive.
func (m *Manager) IsTunnelRunning(_ context.Context, localPort, remotePort int) bool {
	tunnelsMu.Lock()
	defer tunnelsMu.Unlock()
	_, ok := tunnels[tunnelKey(localPort, remotePort)]
	return ok
}

// WatchTunnel blocks until the tunnel subprocess exits (or ctx is cancelled).
// Returns nil when the tunnel exits naturally. Intended for use as a tea.Cmd watcher:
// call StartTunnel first, then call WatchTunnel to be notified when it dies.
func (m *Manager) WatchTunnel(ctx context.Context, localPort, remotePort int) {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	t, ok := tunnels[key]
	tunnelsMu.Unlock()
	if !ok {
		return
	}
	select {
	case <-t.doneCh:
	case <-ctx.Done():
	}
}

// sshErrorSummary extracts the meaningful lines from ssh -v stderr output,
// stripping debug1/debug2/debug3 noise so only actual errors remain.
func sshErrorSummary(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	lines := strings.Split(stderr, "\n")
	var errLines []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// Skip SSH verbose debug lines.
		if strings.HasPrefix(l, "debug1:") ||
			strings.HasPrefix(l, "debug2:") ||
			strings.HasPrefix(l, "debug3:") ||
			strings.HasPrefix(l, "OpenSSH_") ||
			strings.HasPrefix(l, "Authenticated to") {
			continue
		}
		errLines = append(errLines, l)
	}
	if len(errLines) == 0 {
		// All lines were debug noise — return empty so the caller uses exit error.
		return ""
	}
	return strings.Join(errLines, "\n")
}
