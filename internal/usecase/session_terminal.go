package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
)

// TerminalExecutor is the slice of RemoteExecutor the terminal-routing helpers
// need. ssh.Manager satisfies it; tests use focused mocks.
type TerminalExecutor interface {
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

// PaneCapturer adds tmux pane capture for sessions still hosted by tmux.
type PaneCapturer interface {
	TerminalExecutor
	CaptureTmuxPane(ctx context.Context, sessionName string) (string, error)
}

// remoteAimanPreamble makes the serve-installed binary resolvable over
// non-interactive SSH (it lives in ~/.local/bin).
const remoteAimanPreamble = `export PATH="$HOME/.local/bin:$PATH"; `

// PTYSpec is what flow_manager hands to the remote `aiman pty create`.
type PTYSpec struct {
	ID      string
	Name    string
	Dir     string
	Command string
	Env     map[string]string
}

// CreatePTYSession launches a session inside the remote serve daemon's PTY
// runtime. Params travel as a JSON file to keep secrets out of argv.
func CreatePTYSession(ctx context.Context, remote TerminalExecutor, spec PTYSpec) error {
	params := map[string]any{
		"id":      spec.ID,
		"name":    spec.Name,
		"dir":     spec.Dir,
		"command": spec.Command,
	}
	if len(spec.Env) > 0 {
		env := map[string]string{}
		for k, v := range spec.Env {
			env[k] = v
		}
		params["env"] = env
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("pty params: %w", err)
	}
	path := fmt.Sprintf("/tmp/aiman-pty-%s.json", spec.ID)
	if err := remote.WriteFile(ctx, path, raw); err != nil {
		return fmt.Errorf("write pty params: %w", err)
	}
	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty create --params-file %q; rm -f %q", path, path))
	if err != nil {
		return fmt.Errorf("pty create: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// CapturePTYPane returns recent output from a remote PTY session.
func CapturePTYPane(ctx context.Context, remote TerminalExecutor, id string, lines int) (string, error) {
	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty capture %q --lines %d", id, lines))
	if err != nil {
		return "", fmt.Errorf("pty capture: %w", err)
	}
	return extractPTYText(out), nil
}

// SendPTYFile types the contents of a remote file into the session followed by
// Enter — the PTY equivalent of tmux send-keys "$(cat file)" + Enter.
func SendPTYFile(ctx context.Context, remote TerminalExecutor, id, remotePath string) error {
	_, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty input %q --file %q", id, remotePath))
	return err
}

// KillPTYSession stops and removes a remote PTY session. Kill errors when the
// session already exited, which is treated as success.
func KillPTYSession(ctx context.Context, remote TerminalExecutor, id string) error {
	_, _ = remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty kill %q", id))
	_, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty forget %q", id))
	if err != nil && !strings.Contains(err.Error(), "not_found") && !strings.Contains(err.Error(), "has exited") {
		return err
	}
	return nil
}

// PTYRuntimeAvailable reports whether the remote can host PTY sessions right
// now: aiman installed, serve running, runtime reachable.
func PTYRuntimeAvailable(ctx context.Context, remote TerminalExecutor) bool {
	out, err := remote.Execute(ctx, remoteAimanPreamble+"aiman pty list >/dev/null 2>&1 && echo YES || echo NO")
	return err == nil && strings.Contains(out, "YES")
}

// extractPTYText pulls the pane text out of `aiman pty capture` JSON output.
func extractPTYText(raw string) string {
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &result); err == nil {
		return result.Text
	}
	// Fall back to the raw output so partial/legacy responses still render.
	return raw
}

// CaptureSessionPane captures the live pane for whichever backend hosts the
// session. This is the branch point every preview/classification call site
// should use instead of RemoteExecutor.CaptureTmuxPane directly.
func CaptureSessionPane(ctx context.Context, remote PaneCapturer, s domain.Session) (string, error) {
	if s.IsPTY() {
		return CapturePTYPane(ctx, remote, terminalID(s), 0)
	}
	return remote.CaptureTmuxPane(ctx, s.TmuxSession)
}

// SendSessionPrompt delivers prompt text to whichever backend hosts the
// session. The prompt is written remotely first so arbitrary bytes survive.
func SendSessionPrompt(ctx context.Context, remote TerminalExecutor, s domain.Session, prompt string) error {
	if s.IsPTY() {
		path := fmt.Sprintf("/tmp/aiman-prompt-%s.txt", s.ID)
		if err := remote.WriteFile(ctx, path, []byte(prompt)); err != nil {
			return err
		}
		if err := SendPTYFile(ctx, remote, terminalID(s), path); err != nil {
			return err
		}
		_, _ = remote.Execute(ctx, fmt.Sprintf("rm -f %q", path))
		return nil
	}
	return SendPrompt(ctx, remote, s.TmuxSession, s.ID, prompt)
}

// TerminateSessionTerminal stops the session's terminal process without
// touching its worktree.
func TerminateSessionTerminal(ctx context.Context, remote TerminalExecutor, s domain.Session) error {
	if s.IsPTY() {
		return KillPTYSession(ctx, remote, terminalID(s))
	}
	_, err := remote.Execute(ctx, fmt.Sprintf("tmux kill-session -t %q 2>/dev/null || true", s.TmuxSession))
	return err
}

// terminalID is the handle the PTY runtime knows this session by — the aiman
// session ID, matching what create registered.
func terminalID(s domain.Session) string { return s.ID }

// PTYRecord is one session reported by the remote PTY runtime.
type PTYRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Status  string `json:"status"`
	Command string `json:"command"`
}

// ScanPTYSessions asks the remote serve daemon what PTY sessions it currently
// holds. Remotes without the runtime return an empty list, not an error.
func ScanPTYSessions(ctx context.Context, remote TerminalExecutor) []PTYRecord {
	out, err := remote.Execute(ctx, remoteAimanPreamble+"aiman pty list 2>/dev/null")
	if err != nil {
		return nil
	}
	var result struct {
		Sessions []PTYRecord `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		return nil
	}
	return result.Sessions
}

// PTYSessionExists reports whether the remote runtime currently holds a live
// session with this id.
func PTYSessionExists(ctx context.Context, remote TerminalExecutor, id string) bool {
	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty get %q 2>/dev/null", id))
	if err != nil {
		return false
	}
	return strings.Contains(out, `"running"`)
}

// RevivePTYSession brings back a built-in-PTY session whose process died —
// typically because aiman serve restarted — by relaunching the agent with its
// native resume flag (claude --resume, codex resume, …). The conversation
// continues; only the terminal process was lost.
func RevivePTYSession(ctx context.Context, remote TerminalExecutor, s *domain.Session) error {
	if !s.IsPTY() || s.AgentName == "" {
		return fmt.Errorf("session %s cannot be revived: backend=%s agent=%q", s.ID, s.Backend, s.AgentName)
	}
	nativeID := NativeSessionID(ctx, remote, s)
	command := agenthook.WithResume(s.AgentName, nativeID)
	if command == s.AgentName && nativeID == "" {
		// No vendor conversation id anywhere: relaunching would start a fresh
		// conversation silently. Refuse and let restart handle it explicitly.
		return fmt.Errorf("session %s has no agent session id; use restart to relaunch", s.ID)
	}
	env := map[string]string{}
	for k, v := range aimanRuntimeEnv(s) {
		env[k] = v
	}
	if err := CreatePTYSession(ctx, remote, PTYSpec{
		ID:      s.ID,
		Name:    s.TmuxSession,
		Dir:     s.WorkingDirectory,
		Command: command,
		Env:     env,
	}); err != nil {
		return err
	}
	return nil
}

// ReviveIfNeeded revives the session only when the remote runtime no longer
// holds it. Returns whether a revival happened.
func ReviveIfNeeded(ctx context.Context, remote TerminalExecutor, s *domain.Session) (bool, error) {
	if !s.IsPTY() {
		return false, nil
	}
	if PTYSessionExists(ctx, remote, s.ID) {
		return false, nil
	}
	if err := RevivePTYSession(ctx, remote, s); err != nil {
		return false, err
	}
	return true, nil
}

// NativeSessionID resolves the vendor conversation id for a session. The
// remote sidecar file the hooks maintain wins: it reflects what the agent is
// actually working on right now, while the stored field may lag behind.
func NativeSessionID(ctx context.Context, remote TerminalExecutor, s *domain.Session) string {
	if safe := agenthook.SafeSessionID(s.ID); safe != "" {
		out, err := remote.Execute(ctx, fmt.Sprintf("cat \"$HOME/.aiman/native-sessions/%s\" 2>/dev/null || true", safe))
		if err == nil {
			if n := agenthook.ParseStored([]byte(out)); n.ID != "" {
				agenthook.ApplyReport(s, n, time.Now())
				return n.ID
			}
		}
	}
	return strings.TrimSpace(s.AgentSessionID)
}
