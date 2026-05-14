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

	// Run as a slave of the existing ControlMaster: no new TCP connection,
	// no auth round-trip. Falls back to direct connection if the master is gone.
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-S", cp,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-N",
		"-L", forward,
		target,
	)
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
		tunnelsMu.Lock()
		if current, ok := tunnels[key]; ok && current == t {
			delete(tunnels, key)
		}
		tunnelsMu.Unlock()
		exitCh <- err
	}()

	// Give the process 800ms to fail fast (port conflict, auth error, etc.).
	select {
	case err := <-exitCh:
		tunnelsMu.Lock()
		delete(tunnels, key)
		tunnelsMu.Unlock()
		if err != nil {
			return fmt.Errorf("tunnel L%d->R%d failed: %w", localPort, remotePort, err)
		}
		return fmt.Errorf("tunnel L%d->R%d exited immediately (check port and SSH access)", localPort, remotePort)
	case <-time.After(800 * time.Millisecond):
		// Process is still running — tunnel is up.
		return nil
	}
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
