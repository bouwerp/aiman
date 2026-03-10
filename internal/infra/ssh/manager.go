package ssh

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// Use a hash of the target to keep path length reasonable and unique
	target := m.target()
	return filepath.Join(home, ".aiman", "sockets", strings.ReplaceAll(target, "@", "-")+".sock")
}

func (m *Manager) Connect(ctx context.Context) error {
	// Simple connectivity and directory validation if Root is set
	_, err := m.Execute(ctx, "true")
	if err != nil {
		return err
	}
	return nil
}

func (m *Manager) Execute(ctx context.Context, cmdStr string) (string, error) {
	target := m.target()
	cp := m.controlPath()

	// Ensure sockets directory exists
	if err := os.MkdirAll(filepath.Dir(cp), 0700); err != nil {
		return "", fmt.Errorf("failed to create sockets directory: %w", err)
	}

	// We use ControlMaster=auto and ControlPersist to handle multiplexing automatically.
	// This is more robust than manual management with -f.
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=10m",
		"-S", cp,
		target, cmdStr)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("remote command failed on %s: %w\nOutput: %s", target, err, string(output))
	}
	return string(output), nil
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

	// find <root> -maxdepth 2 -name .git -type d -prune
	cmd := fmt.Sprintf("find %q -maxdepth 2 -name .git -type d -prune", m.config.Root)
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
	cmd := fmt.Sprintf("git -C %s worktree list --porcelain", repoPath)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to scan worktrees for %s: %w", repoPath, err)
	}

	worktrees := []string{}
	cleanRepoPath := filepath.Clean(repoPath)

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			worktreePath := strings.TrimPrefix(line, "worktree ")
			worktreePath = strings.TrimSpace(worktreePath)
			if filepath.Clean(worktreePath) != cleanRepoPath {
				worktrees = append(worktrees, worktreePath)
			}
		}
	}
	return worktrees, nil
}

func (m *Manager) GetTmuxSessionCWD(ctx context.Context, sessionName string) (string, error) {
	// tmux display-message -p -F "#{pane_current_path}" -t <sessionName>
	cmd := fmt.Sprintf("tmux display-message -p -F '#{pane_current_path}' -t %s", sessionName)
	output, err := m.Execute(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to get tmux session CWD: %w", err)
	}
	return strings.TrimSpace(output), nil
}

func (m *Manager) CaptureTmuxPane(ctx context.Context, sessionName string) (string, error) {
	// Capture the visible pane and scrollback buffer (-S -)
	cmdStr := fmt.Sprintf("tmux capture-pane -p -e -S - -t %s", sessionName)
	output, err := m.Execute(ctx, cmdStr)
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane: %w", err)
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
	// Use -t for interactive tty allocation
	return exec.Command("ssh", "-t", "-o", "BatchMode=yes", target, "tmux", "attach", "-t", sessionName)
}

func (m *Manager) StreamTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error) {
	target := m.target()
	// -t for TTY, tmux attach to the session
	cmd := exec.CommandContext(ctx, "ssh", "-t", "-o", "BatchMode=yes", target, "tmux", "attach", "-t", sessionName)

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

	dirs := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != rootPath {
			// Return relative paths (remove the rootPath prefix)
			relPath := strings.TrimPrefix(line, rootPath)
			relPath = strings.TrimPrefix(relPath, "/")
			if relPath != "" {
				dirs = append(dirs, relPath)
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
