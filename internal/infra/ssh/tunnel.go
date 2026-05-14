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
	cmd *exec.Cmd
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

	// Ensure the ControlMaster is connected so the slave can reuse it.
	if _, err := m.Execute(ctx, "true"); err != nil {
		return fmt.Errorf("cannot connect to remote: %w", err)
	}

	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	cp := m.controlPath()
	target := m.target()

	var stderrBuf strings.Builder

	// Run as a slave of the existing ControlMaster: no new TCP connection,
	// no auth round-trip. Falls back to direct connection if the master is gone.
	cmd := exec.Command("ssh",
		"-v",
		"-o", "BatchMode=yes",
		"-S", cp,
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

	t := &activeTunnel{cmd: cmd}
	tunnelsMu.Lock()
	tunnels[key] = t
	tunnelsMu.Unlock()

	// Watch for unexpected tunnel death and clean up the map.
	// Also used to detect immediate startup failures.
	exitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		detail := strings.TrimSpace(stderrBuf.String())
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
		tunnelsMu.Unlock()
		exitCh <- err
	}()

	// Give the process 2s to fail fast (port conflict, auth error, etc.).
	select {
	case err := <-exitCh:
		tunnelsMu.Lock()
		delete(tunnels, key)
		tunnelsMu.Unlock()
		detail := strings.TrimSpace(stderrBuf.String())
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
