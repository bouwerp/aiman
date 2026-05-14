package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	gsssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// activeTunnel holds the resources for a running port forward.
type activeTunnel struct {
	listener net.Listener
	cancel   context.CancelFunc
}

var (
	tunnelsMu sync.Mutex
	tunnels   = make(map[string]*activeTunnel)
)

func tunnelKey(localPort, remotePort int) string {
	return fmt.Sprintf("%d:%d", localPort, remotePort)
}

// StartTunnel establishes a native Go SSH tunnel that forwards
// localhost:localPort on this machine to localhost:remotePort on the
// SSH target. The tunnel runs entirely in goroutines — no external
// ssh process is forked, so there are no SIGHUP/lifecycle issues.
func (m *Manager) StartTunnel(ctx context.Context, localPort, remotePort int) error {
	key := tunnelKey(localPort, remotePort)

	tunnelsMu.Lock()
	if _, exists := tunnels[key]; exists {
		tunnelsMu.Unlock()
		return nil // already running
	}
	tunnelsMu.Unlock()

	sshClient, err := dialSSH(m.config)
	if err != nil {
		return fmt.Errorf("failed to connect for tunnel: %w", err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		sshClient.Close()
		return fmt.Errorf("failed to bind local port %d: %w", localPort, err)
	}

	tCtx, cancel := context.WithCancel(context.Background())

	tunnelsMu.Lock()
	tunnels[key] = &activeTunnel{listener: ln, cancel: cancel}
	tunnelsMu.Unlock()

	go func() {
		defer sshClient.Close()
		defer ln.Close()
		defer func() {
			tunnelsMu.Lock()
			delete(tunnels, key)
			tunnelsMu.Unlock()
		}()

		for {
			local, err := ln.Accept()
			if err != nil {
				select {
				case <-tCtx.Done():
				default:
				}
				return
			}
			go forwardConn(tCtx, sshClient, local, remotePort)
		}
	}()

	return nil
}

// StopTunnel tears down a running tunnel.
func (m *Manager) StopTunnel(_ context.Context, localPort, remotePort int) error {
	key := tunnelKey(localPort, remotePort)
	tunnelsMu.Lock()
	t, ok := tunnels[key]
	if ok {
		delete(tunnels, key)
	}
	tunnelsMu.Unlock()

	if !ok {
		return nil
	}
	t.cancel()
	t.listener.Close()
	return nil
}

// IsTunnelRunning returns true if the tunnel is active.
func (m *Manager) IsTunnelRunning(_ context.Context, localPort, remotePort int) bool {
	tunnelsMu.Lock()
	defer tunnelsMu.Unlock()
	_, ok := tunnels[tunnelKey(localPort, remotePort)]
	return ok
}

// forwardConn proxies a single local connection through the SSH client to the remote port.
func forwardConn(ctx context.Context, client *gsssh.Client, local net.Conn, remotePort int) {
	defer local.Close()

	remote, err := client.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
	if err != nil {
		return
	}
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// dialSSH opens a native Go SSH connection to the Manager's target host,
// trying the SSH agent first then common key files.
func dialSSH(cfg Config) (*gsssh.Client, error) {
	user := cfg.User
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "root"
	}

	host := cfg.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = host + ":22"
	}

	authMethods := collectAuthMethods()
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no SSH authentication methods available (no agent and no key files found)")
	}

	hostKeyCallback, err := buildHostKeyCallback()
	if err != nil {
		// Fall back to insecure if known_hosts not readable — at least tunnel works.
		hostKeyCallback = gsssh.InsecureIgnoreHostKey()
	}

	sshCfg := &gsssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
	}

	return gsssh.Dial("tcp", host, sshCfg)
}

// collectAuthMethods returns all available SSH auth methods in preference order:
// SSH agent, then key files.
func collectAuthMethods() []gsssh.AuthMethod {
	var methods []gsssh.AuthMethod

	// SSH agent
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, gsssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	// Key files
	home, _ := os.UserHomeDir()
	keyFiles := []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".ssh", "id_dsa"),
	}
	for _, kf := range keyFiles {
		data, err := os.ReadFile(kf)
		if err != nil {
			continue
		}
		signer, err := gsssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		methods = append(methods, gsssh.PublicKeys(signer))
	}

	return methods
}

// buildHostKeyCallback loads ~/.ssh/known_hosts for host key verification.
func buildHostKeyCallback() (gsssh.HostKeyCallback, error) {
	home, _ := os.UserHomeDir()
	knownHostsFile := filepath.Join(home, ".ssh", "known_hosts")
	return knownhosts.New(knownHostsFile)
}
