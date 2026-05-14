package ssh

// SSH port-forwarding tunnels using the existing ControlMaster socket.
//
// ssh -O forward delegates the port-binding to the already-running master
// process, which has the user's ~/.ssh/config applied and is already
// authenticated. No new process is forked (no -f), so there are no
// SIGHUP / lifecycle issues.

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
)

var (
	tunnelsMu sync.Mutex
	// tunnels tracks which local:remote port pairs we have active.
	// The value is a placeholder; the ControlMaster owns the actual socket.
	tunnels = make(map[string]struct{})
)

func tunnelKey(localPort, remotePort int) string {
	return fmt.Sprintf("%d:%d", localPort, remotePort)
}

// StartTunnel asks the existing ControlMaster to forward
// 127.0.0.1:localPort → 127.0.0.1:remotePort on the remote.
//
// The ControlMaster is kept alive by ControlPersist=10m; Execute calls from
// the dashboard refresh cycle ensure it stays up. No goroutine is started here.
func (m *Manager) StartTunnel(ctx context.Context, localPort, remotePort int) error {
	key := tunnelKey(localPort, remotePort)

	tunnelsMu.Lock()
	if _, exists := tunnels[key]; exists {
		tunnelsMu.Unlock()
		return nil
	}
	tunnelsMu.Unlock()

	// Ensure the ControlMaster is up (it will auto-connect if needed).
	if _, err := m.Execute(ctx, "true"); err != nil {
		return fmt.Errorf("cannot connect to remote: %w", err)
	}

	cp := m.controlPath()
	target := m.target()
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-S", cp,
		"-O", "forward",
		"-L", forward,
		target,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start tunnel L%d->R%d: %w\nOutput: %s",
			localPort, remotePort, err, strings.TrimSpace(string(output)))
	}

	tunnelsMu.Lock()
	tunnels[key] = struct{}{}
	tunnelsMu.Unlock()
	return nil
}

// StopTunnel cancels the port forward on the ControlMaster and removes it
// from the in-memory set.
func (m *Manager) StopTunnel(ctx context.Context, localPort, remotePort int) error {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	delete(tunnels, key)
	tunnelsMu.Unlock()

	cp := m.controlPath()
	target := m.target()
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-S", cp,
		"-O", "cancel",
		"-L", forward,
		target,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		out := strings.ToLower(strings.TrimSpace(string(output)))
		// Ignore "master not found" errors — the tunnel is effectively gone.
		if strings.Contains(out, "no such file") ||
			strings.Contains(out, "does not exist") ||
			strings.Contains(out, "control socket") ||
			strings.Contains(out, "no forward matching") {
			return nil
		}
		return fmt.Errorf("failed to stop tunnel L%d->R%d: %w\nOutput: %s",
			localPort, remotePort, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// IsTunnelRunning returns true if the tunnel is tracked in the in-memory set
// AND the local port is actually bound (which would fail if the ControlMaster
// died and took the forward with it).
func (m *Manager) IsTunnelRunning(_ context.Context, localPort, remotePort int) bool {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	_, ok := tunnels[key]
	tunnelsMu.Unlock()
	if !ok {
		return false
	}

	// Verify the local port is actually listening. If we can bind it, the
	// forward is gone (ControlMaster died); clean up our state.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return true // port in use → tunnel alive
	}
	ln.Close()
	// Port was free — forward disappeared (ControlMaster cycled or crashed).
	tunnelsMu.Lock()
	delete(tunnels, key)
	tunnelsMu.Unlock()
	return false
}
