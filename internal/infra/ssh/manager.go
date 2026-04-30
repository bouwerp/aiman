package ssh

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type Config struct {
	Host string
	User string
	Root string
}

type Manager struct {
	config Config
}

func NewManager(config Config) *Manager {
	return &Manager{
		config: config,
	}
}

func (m *Manager) target() string {
	// If Host already contains @, use it as is
	if strings.Contains(m.config.Host, "@") {
		return m.config.Host
	}
	if m.config.User != "" {
		return fmt.Sprintf("%s@%s", m.config.User, m.config.Host)
	}
	return m.config.Host
}

func (m *Manager) controlPath() string {
	home, _ := os.UserHomeDir()
	// Keep unix socket path short to avoid OS length limits.
	targetHash := shortTargetHash(m.target())
	return filepath.Join(home, ".aiman", "sockets", "ssh-"+targetHash+".sock")
}

func shortTargetHash(target string) string {
	sum := sha1.Sum([]byte(target))
	// 16 hex chars is compact while still collision-resistant enough for this use.
	return hex.EncodeToString(sum[:8])
}

func (m *Manager) tunnelControlPath(localPort, remotePort int) string {
	home, _ := os.UserHomeDir()
	targetHash := shortTargetHash(m.target())
	name := "t-" + targetHash + "-l" + strconv.Itoa(localPort) + "-r" + strconv.Itoa(remotePort) + ".sock"
	return filepath.Join(home, ".aiman", "tunnels", name)
}

func (m *Manager) Connect(ctx context.Context) error {
	// Simple connectivity and directory validation if Root is set
	_, err := m.Execute(ctx, "true")
	if err != nil {
		return err
	}
	return nil
}

func (m *Manager) GetRoot() string {
	return m.config.Root
}

// sshCommandTimeout is the per-call deadline for individual SSH commands.
// This prevents a single hung command from blocking the entire restart for minutes.
const sshCommandTimeout = 30 * time.Second

func (m *Manager) Execute(ctx context.Context, cmdStr string) (string, error) {
	target := m.target()
	cp := m.controlPath()

	// Ensure sockets directory exists
	if err := os.MkdirAll(filepath.Dir(cp), 0700); err != nil {
		return "", fmt.Errorf("failed to create sockets directory: %w", err)
	}

	run := func() (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, sshCommandTimeout)
		defer cancel()
		// We use ControlMaster=auto and ControlPersist to handle multiplexing automatically.
		// ServerAliveInterval/CountMax ensure dead connections are detected within ~15s.
		cmd := exec.CommandContext(callCtx, "ssh",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=3",
			"-o", "ControlMaster=auto",
			"-o", "ControlPersist=10m",
			"-S", cp,
			"-A",
			"-X",
			target, cmdStr)

		output, err := cmd.CombinedOutput()
		outStr := strings.TrimSpace(string(output))
		if err != nil {
			return outStr, fmt.Errorf("remote command failed on %s: %w\nOutput: %s", target, err, outStr)
		}
		return outStr, nil
	}

	runDirect := func() (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, sshCommandTimeout)
		defer cancel()
		// Final fallback without SSH multiplexing, for cases where the control
		// socket/session is flaky but direct SSH still works.
		cmd := exec.CommandContext(callCtx, "ssh",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=3",
			"-o", "ControlMaster=no",
			"-A",
			target, cmdStr)

		output, err := cmd.CombinedOutput()
		outStr := strings.TrimSpace(string(output))
		if err != nil {
			return outStr, fmt.Errorf("remote command failed on %s: %w\nOutput: %s", target, err, outStr)
		}
		return outStr, nil
	}

	out, err := run()
	if err == nil || !isRetriableSSHTransportError(err.Error()) {
		return out, err
	}

	// Stale/broken control socket or dropped SSH transport.
	// Clear the socket and retry with a fresh multiplexed connection.
	for i := 0; i < 2; i++ {
		_ = os.Remove(cp)
		out, err = run()
		if err == nil || !isRetriableSSHTransportError(err.Error()) {
			return out, err
		}
	}

	// Last resort: bypass multiplexing entirely for this command.
	return runDirect()
}

func isRetriableSSHTransportError(errText string) bool {
	s := strings.ToLower(errText)
	return strings.Contains(s, "permission denied") ||
		strings.Contains(s, "connection closed by") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "kex_exchange_identification") ||
		strings.Contains(s, "mux_client_request_session") ||
		strings.Contains(s, "control socket connect") ||
		strings.Contains(s, "connect to new control master") ||
		strings.Contains(s, "broken pipe")
}

func (m *Manager) WriteFile(ctx context.Context, path string, content []byte) error {
	// Safety check: refuse to write to critical user config files unless explicitly scoped
	base := filepath.Base(path)
	if base == ".bashrc" || base == ".profile" || base == ".bash_profile" || base == ".zshrc" {
		return fmt.Errorf("refusing to overwrite critical user configuration file: %s", path)
	}

	target := m.target()
	cp := m.controlPath()

	// Ensure sockets directory exists
	if err := os.MkdirAll(filepath.Dir(cp), 0700); err != nil {
		return fmt.Errorf("failed to create sockets directory: %w", err)
	}

	doWrite := func(useControlMaster bool) error {
		callCtx, cancel := context.WithTimeout(ctx, sshCommandTimeout)
		defer cancel()

		args := []string{
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=10",
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=3",
		}
		if useControlMaster {
			args = append(args,
				"-o", "ControlMaster=auto",
				"-o", "ControlPersist=10m",
				"-S", cp,
			)
		} else {
			args = append(args, "-o", "ControlMaster=no")
		}
		args = append(args, "-A", "-X", target, fmt.Sprintf("cat > %q", path))

		cmd := exec.CommandContext(callCtx, "ssh", args...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("failed to get stdin pipe for ssh: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start ssh for WriteFile: %w", err)
		}
		if _, err := stdin.Write(content); err != nil {
			return fmt.Errorf("failed to write content to ssh stdin: %w", err)
		}
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("ssh Wait failed for WriteFile: %w", err)
		}
		return nil
	}

	err := doWrite(true)
	if err == nil || !isRetriableSSHTransportError(err.Error()) {
		return err
	}

	// Stale/broken control socket — clear it and retry.
	_ = os.Remove(cp)
	err = doWrite(true)
	if err == nil || !isRetriableSSHTransportError(err.Error()) {
		return err
	}

	// Last resort: direct connection without multiplexing.
	return doWrite(false)
}

func (m *Manager) ValidateDir(ctx context.Context, path string) error {
	// Use 'test -d' which is more standard. Quote the path to handle spaces.
	cmd := fmt.Sprintf("test -d %q", path)
	_, err := m.Execute(ctx, cmd)
	if err != nil {
		return fmt.Errorf("directory %s not found or not accessible: %w", path, err)
	}
	return nil
}

func (m *Manager) ScanTmuxSessions(ctx context.Context) ([]string, error) {
	// tmux ls -F '#S'
	output, err := m.Execute(ctx, "tmux ls -F '#S'")
	if err != nil {
		// If tmux ls fails, it might mean there are no sessions
		return nil, nil
	}

	sessions := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func (m *Manager) ScanGitRepos(ctx context.Context) ([]string, error) {
	if m.config.Root == "" {
		return nil, nil
	}

	// Use find to look for .git directories up to 2 levels deep
	// This will find repos at <root>/repo or <root>/org/repo
	cmd := fmt.Sprintf("find %q -maxdepth 3 -name .git -type d -prune", m.config.Root)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for git repos: %w", err)
	}

	repos := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			// Remove the /.git suffix to get the repo path
			repoPath := strings.TrimSuffix(line, "/.git")
			repos = append(repos, repoPath)
		}
	}
	return repos, nil
}

func (m *Manager) ScanWorktrees(ctx context.Context, repoPath string) ([]string, error) {
	// git -C <repoPath> worktree list --porcelain
	cmd := fmt.Sprintf("git -C %s worktree list", repoPath)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to scan worktrees for %s: %w", repoPath, err)
	}

	worktrees := []string{}
	cleanRepoPath := filepath.Clean(repoPath)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		worktreePath := ""
		if strings.HasPrefix(line, "worktree ") {
			// Porcelain format
			worktreePath = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		} else {
			// Standard format (usually path is the first field)
			worktreePath = strings.Fields(line)[0]
		}

		if worktreePath != "" && filepath.Clean(worktreePath) != cleanRepoPath {
			worktrees = append(worktrees, worktreePath)
		}
	}
	return worktrees, nil
}

func (m *Manager) GetGitRoot(ctx context.Context, path string) (string, error) {
	// git -C <path> rev-parse --show-toplevel
	cmd := fmt.Sprintf("git -C %q rev-parse --show-toplevel", path)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get git root for %s: %w", path, err)
	}
	return strings.TrimSpace(output), nil
}

func (m *Manager) GetTmuxSessionCWD(ctx context.Context, sessionName string) (string, error) {
	// tmux display-message -p -F "#{pane_current_path}" -t <sessionName>
	cmd := fmt.Sprintf("tmux display-message -p -F '#{pane_current_path}' -t %q", sessionName)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get tmux session CWD: %w", err)
	}
	return strings.TrimSpace(output), nil
}

func (m *Manager) GetTmuxSessionEnv(ctx context.Context, sessionName, envVar string) (string, error) {
	// tmux show-environment -t <sessionName> <envVar>
	// Output is usually "VAR=value", so we need to strip the prefix
	cmd := fmt.Sprintf("tmux show-environment -t %q %s", sessionName, envVar)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get tmux session env: %w", err)
	}
	prefix := envVar + "="
	if strings.HasPrefix(output, prefix) {
		return strings.TrimPrefix(output, prefix), nil
	}
	return strings.TrimSpace(output), nil
}

func (m *Manager) CaptureTmuxPane(ctx context.Context, sessionName string) (string, error) {
	if sessionName == "" {
		return "", fmt.Errorf("capture pane: session name is empty")
	}
	// Capture the full scrollback history (-S - means from the beginning of history)
	cmdStr := fmt.Sprintf("tmux capture-pane -p -e -S - -t %q", sessionName)

	var output string
	var err error

	isTransient := func(out, errStr string) bool {
		combined := out + errStr
		return strings.Contains(combined, "can't find pane") ||
			strings.Contains(combined, "failed to connect to server") ||
			strings.Contains(combined, "no server running") ||
			strings.Contains(combined, "connect to new control master")
	}

	// Retry up to 8 times with increasing delay if session/server is not yet available.
	// This handles the race where the tmux server or pane hasn't fully started yet
	// (e.g. immediately after a session restart).
	for i := 0; i < 8; i++ {
		output, err = m.Execute(ctx, cmdStr)
		if err == nil {
			break
		}
		errStr := err.Error()
		if !isTransient(output, errStr) {
			return "", fmt.Errorf("failed to capture tmux pane: %w", err)
		}
		// Wait a bit before retry
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Duration(400*(i+1)) * time.Millisecond):
		}
	}

	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane after retries: %w", err)
	}

	// Trim trailing empty lines for cleaner preview
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.Join(lines[:i+1], "\n"), nil
		}
	}

	return output, nil
}

func (m *Manager) AttachTmuxSession(sessionName string) *exec.Cmd {
	target := m.target()
	// Use -t for interactive tty allocation, -A for agent forwarding, and -X for X11 forwarding (clipboard)
	return exec.Command("ssh", "-t", "-A", "-X", "-o", "BatchMode=yes", target, "tmux", "attach", "-t", sessionName)
}

func (m *Manager) StreamTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error) {
	target := m.target()
	// -t for TTY, -A for agent forwarding, -X for X11 forwarding, tmux attach to the session
	cmd := exec.CommandContext(ctx, "ssh", "-t", "-A", "-X", "-o", "BatchMode=yes", target, "tmux", "attach", "-t", sessionName)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &commandStream{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
	}, nil
}

type commandStream struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (s *commandStream) Read(p []byte) (n int, err error) {
	return s.stdout.Read(p)
}

func (s *commandStream) Write(p []byte) (n int, err error) {
	return s.stdin.Write(p)
}

func (s *commandStream) Close() error {
	s.stdin.Close()
	s.stdout.Close()
	if s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}

func (m *Manager) StartTmuxSession(ctx context.Context, name string) error {
	// Step 8 of SPEC: tmux new-session -d -s <branch>
	cmdStr := fmt.Sprintf("tmux new-session -d -s %s", name)
	_, err := m.Execute(ctx, cmdStr)
	return err
}

func (m *Manager) ProvisionRemote(ctx context.Context, steps []domain.ProvisionStep, progress chan<- domain.ProvisionProgress) error {
	for _, step := range steps {
		progress <- domain.ProvisionProgress{
			StepID:  step.ID,
			Status:  "running",
			Message: fmt.Sprintf("Executing: %s", step.Name),
		}

		_, err := m.Execute(ctx, step.Command)
		if err != nil {
			progress <- domain.ProvisionProgress{
				StepID:  step.ID,
				Status:  "error",
				Message: err.Error(),
			}
			return err
		}

		progress <- domain.ProvisionProgress{
			StepID:  step.ID,
			Status:  "success",
			Message: fmt.Sprintf("Completed: %s", step.Name),
		}
	}
	return nil
}

func (m *Manager) StartTunnel(ctx context.Context, localPort, remotePort int) error {
	cp := m.tunnelControlPath(localPort, remotePort)
	if err := os.MkdirAll(filepath.Dir(cp), 0700); err != nil {
		return fmt.Errorf("failed to create tunnel socket directory: %w", err)
	}
	_ = os.Remove(cp)

	target := m.target()
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-M",
		"-S", cp,
		"-f",
		"-N",
		"-L", forward,
		target,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start tunnel %s: %w\nOutput: %s", forward, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) StopTunnel(ctx context.Context, localPort, remotePort int) error {
	cp := m.tunnelControlPath(localPort, remotePort)
	if _, err := os.Stat(cp); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to stat tunnel socket: %w", err)
	}
	target := m.target()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-S", cp,
		"-O", "exit",
		target,
	)
	output, err := cmd.CombinedOutput()
	_ = os.Remove(cp)
	if err != nil {
		out := strings.ToLower(strings.TrimSpace(string(output)))
		if strings.Contains(out, "no such file") || strings.Contains(out, "does not exist") || strings.Contains(out, "control socket connect") {
			return nil
		}
		return fmt.Errorf("failed to stop tunnel L%d->R%d: %w\nOutput: %s", localPort, remotePort, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *Manager) IsTunnelRunning(ctx context.Context, localPort, remotePort int) bool {
	cp := m.tunnelControlPath(localPort, remotePort)
	if _, err := os.Stat(cp); err != nil {
		return false
	}
	target := m.target()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-S", cp,
		"-O", "check",
		target,
	)
	return cmd.Run() == nil
}

func (m *Manager) ScanDirectories(ctx context.Context, rootPath string, maxDepth int) ([]string, error) {
	// Use find to list directories up to maxDepth
	// Exclude .git directories and common non-code directories
	excludeDirs := []string{".git", "node_modules", "vendor", ".next", "dist", "build", "target"}

	pruneExpr := ""
	for _, dir := range excludeDirs {
		if pruneExpr != "" {
			pruneExpr += " -o "
		}
		pruneExpr += fmt.Sprintf("-name %q -prune", dir)
	}

	var cmdStr string
	if pruneExpr != "" {
		cmdStr = fmt.Sprintf("find %q -maxdepth %d -type d \\( %s \\) -o -type d -print", rootPath, maxDepth, pruneExpr)
	} else {
		cmdStr = fmt.Sprintf("find %q -maxdepth %d -type d", rootPath, maxDepth)
	}

	output, err := m.Execute(ctx, cmdStr)
	if err != nil {
		return nil, fmt.Errorf("failed to scan directories: %w", err)
	}

	// Ensure rootPath doesn't have trailing slash for consistent prefix trimming
	rootPathClean := strings.TrimRight(rootPath, "/")

	dirs := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lineClean := strings.TrimRight(line, "/")
			if lineClean != rootPathClean {
				// Return relative paths (remove the rootPath prefix)
				relPath := strings.TrimPrefix(lineClean, rootPathClean)
				relPath = strings.TrimPrefix(relPath, "/")
				if relPath != "" {
					dirs = append(dirs, relPath)
				}
			}
		}
	}
	return dirs, nil
}

func (m *Manager) Close() error {
	// Stop SSH multiplexing
	target := m.target()
	cmd := exec.CommandContext(context.Background(), "ssh", "-o", "BatchMode=yes", "-O", "exit", target)
	return cmd.Run()
}
