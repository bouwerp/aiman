package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	infraAgent "github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/pane"
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

// PaneShellCommand is the tmux pane command that runs the agent. The trailing
// exec bash lives inside the same bash -c as the agent: tmux execs one command,
// so a sibling `cmd; exec bash` never runs and remain-on-exit shows a dead pane.
func PaneShellCommand(bootstrap string) string {
	return fmt.Sprintf("bash -l -c '%s; exec bash -i'", bootstrap)
}

// ApplyKiloAllowEnv writes the auto-allow config and sets the env keys
// Kilo CLI reads for it. Create and restart both have to do this: a restart
// that only set tmux -e flags left PTY Kilo sessions prompting or exiting.
func ApplyKiloAllowEnv(ctx context.Context, remote TerminalExecutor, agentCmd string, env map[string]string) {
	lower := strings.ToLower(agentCmd)
	if remote == nil || env == nil || !strings.Contains(lower, "kilo") {
		return
	}
	_ = remote.WriteFile(ctx, "/tmp/kilo-aiman.json", []byte(`{"permission":"allow"}`))
	env["KILO_CONFIG"] = "/tmp/kilo-aiman.json"
	env["KILO_CONFIG_CONTENT"] = `{"permission":"allow"}`
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

// Terminal size floors. A session narrower or shorter than a classic terminal
// leaves agent TUIs unusable — they assume 80x24 and start dropping columns of
// their own — so fitting a session to a small panel stops here and lets the
// panel scroll instead.
const (
	MinTerminalCols = 80
	MinTerminalRows = 24
)

// ClampTerminalSize applies the floors, reporting whether the request was
// usable at all.
func ClampTerminalSize(cols, rows int) (int, int, bool) {
	if cols <= 0 || rows <= 0 {
		return 0, 0, false
	}
	return max(cols, MinTerminalCols), max(rows, MinTerminalRows), true
}

// fitMarker prefixes the outcome of a tmux fit so the caller can tell a resize
// that happened from one that was deliberately declined.
const fitMarker = "AIMAN_FIT="

// ResizeSessionTerminal re-sizes whichever backend hosts the session, so the
// agent repaints its UI at the given width instead of at whatever size the
// terminal that last attached happened to be. It reports whether the resize was
// actually applied.
//
// tmux normally fits a window to its smallest attached client and would undo
// this on the next attach, so the window is switched to manual sizing; the
// attach path sets it back to `latest` to hand control to the attaching client
// (see ssh.Manager.AttachTmuxSession). A PTY session needs no such restore:
// attaching sends the client's own size as part of the handshake.
//
// A tmux session with a client attached is left alone — someone is watching it,
// and shrinking the window would resize their view out from under them. The
// check and the resize are one remote command so nothing can attach in between,
// and so this costs a single round trip.
func ResizeSessionTerminal(ctx context.Context, remote TerminalExecutor, s domain.Session, cols, rows int) (bool, error) {
	cols, rows, ok := ClampTerminalSize(cols, rows)
	if !ok {
		return false, fmt.Errorf("resize: cols and rows must both be positive")
	}
	if s.IsPTY() {
		return ResizePTYSession(ctx, remote, terminalID(s), cols, rows)
	}
	if s.TmuxSession == "" {
		return false, fmt.Errorf("resize: session has no tmux session name")
	}
	// window-size is set every time: an attach resets it to `latest`, and
	// resize-window against an automatically sized window is silently undone.
	out, err := remote.Execute(ctx, fmt.Sprintf(
		`if [ "$(tmux display-message -p -t %[1]q '#{session_attached}' 2>/dev/null || echo 0)" != "0" ]; then `+
			`echo %[2]sattached; `+
			`else tmux set-option -t %[1]q window-size manual 2>/dev/null; `+
			`tmux resize-window -t %[1]q -x %[3]d -y %[4]d 2>/dev/null && echo %[2]sapplied || echo %[2]sfailed; fi`,
		s.TmuxSession, fitMarker, cols, rows))
	if err != nil {
		return false, err
	}
	return ParseFitOutcome(out)
}

// ParseFitOutcome reads the marker emitted by the tmux fit command.
func ParseFitOutcome(out string) (bool, error) {
	switch {
	case strings.Contains(out, fitMarker+"applied"):
		return true, nil
	case strings.Contains(out, fitMarker+"attached"):
		return false, nil // a client is watching; deliberately left alone
	case strings.Contains(out, fitMarker+"failed"):
		return false, fmt.Errorf("resize: tmux declined the resize")
	default:
		return false, fmt.Errorf("resize: unexpected output %q", strings.TrimSpace(out))
	}
}

// ResizePTYSession sets a remote PTY session's window size.
//
// applied is false when a client is attached: shrinking the session to the
// dashboard preview must not resize a fullscreen attach out from under the
// operator. That matches the tmux attached-client guard.
func ResizePTYSession(ctx context.Context, remote TerminalExecutor, id string, cols, rows int) (bool, error) {
	out, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"aiman pty resize %q --cols %d --rows %d", id, cols, rows))
	if err != nil {
		return false, fmt.Errorf("pty resize: %s: %w", strings.TrimSpace(out), err)
	}
	return parsePTYResizeApplied(out), nil
}

// parsePTYResizeApplied reads the applied flag from `aiman pty resize` JSON.
// A missing flag is treated as applied so older remotes still fit the preview.
func parsePTYResizeApplied(out string) bool {
	var result struct {
		Applied *bool `json:"applied"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		return true
	}
	if result.Applied != nil && !*result.Applied {
		return false
	}
	return true
}

// SendPTYFile types the contents of a remote file into the session and presses
// Return — the PTY equivalent of tmux send-keys "$(cat file)" + Enter.
//
// The Return is a second write after a pause, not a "\r" appended to the text.
// An agent TUI that receives the text and the Return in one read treats the lot
// as a paste and inserts a newline instead of submitting, which left every
// prompt sitting in the input box unsent.
func SendPTYFile(ctx context.Context, remote TerminalExecutor, id, remotePath string) error {
	return SendPTYFileConfirmed(ctx, remote, id, remotePath, "")
}

// submitAttempts is how many Returns a composer that will not clear may get,
// the first included.
const submitAttempts = 3

// submitSettle is how long to let the agent redraw before deciding whether the
// composer cleared.
var submitSettle = 2 * time.Second

// SendPTYFileConfirmed types a remote file into the session, presses Return, and
// checks that the text actually left the composer — pressing Return again if it
// did not.
//
// One Return is not reliable. Agents drop input while they are mid-render or
// still booting, so a prompt could sit in the input box fully typed and never
// submitted, which looks from the outside like the agent ignoring its
// instructions. marker is a distinctive tail of the prompt: while it is still
// visible at the bottom of the pane, the turn has not started.
//
// An empty marker skips the check — Return is sent once, as before. Confirmation
// is best-effort: a pane that cannot be captured is not a reason to fail a
// delivery that may well have worked.
func SendPTYFileConfirmed(ctx context.Context, remote TerminalExecutor, id, remotePath, marker string) error {
	if _, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"aiman pty input %[1]q --file %[2]q && sleep 1 && aiman pty input %[1]q --key enter",
		id, remotePath)); err != nil {
		return err
	}
	marker = submitMarker(marker)
	if marker == "" {
		return nil
	}
	for attempt := 1; attempt < submitAttempts; attempt++ {
		if err := sleepCtx(ctx, submitSettle); err != nil {
			return nil
		}
		out, err := CapturePTYPane(ctx, remote, id, 0)
		if err != nil {
			return nil
		}
		if !composerStillHolds(out, marker) {
			return nil
		}
		if _, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
			"aiman pty input %q --key enter", id)); err != nil {
			return nil
		}
	}
	return nil
}

// sendTmuxKeysConfirmed types a remote file into a tmux pane, presses Enter,
// and checks that the text actually left the composer — pressing Enter again
// if it did not. The tmux twin of SendPTYFileConfirmed; see its doc for why
// one Enter is not reliable.
func sendTmuxKeysConfirmed(ctx context.Context, remote PaneCapturer, tmuxName, remotePath, marker string) error {
	if _, err := remote.Execute(ctx, fmt.Sprintf(
		"tmux send-keys -t %q -l -- \"$(cat %q)\" && sleep 1 && tmux send-keys -t %q Enter",
		tmuxName, remotePath, tmuxName)); err != nil {
		return err
	}
	marker = submitMarker(marker)
	if marker == "" {
		return nil
	}
	for attempt := 1; attempt < submitAttempts; attempt++ {
		if err := sleepCtx(ctx, submitSettle); err != nil {
			return nil
		}
		out, err := remote.CaptureTmuxPane(ctx, tmuxName)
		if err != nil {
			return nil
		}
		if !composerStillHolds(out, marker) {
			return nil
		}
		if _, err := remote.Execute(ctx, fmt.Sprintf("tmux send-keys -t %q Enter", tmuxName)); err != nil {
			return nil
		}
	}
	return nil
}

// submitMarker is the tail of the prompt to look for in the composer.
//
// The tail rather than the head: agents wrap and scroll a long prompt, so the
// beginning may be off screen while the end sits on the composer line. Very
// short prompts are matched whole; an empty one disables the check.
func submitMarker(prompt string) string {
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\n", " "))
	if prompt == "" {
		return ""
	}
	r := []rune(prompt)
	const markerRunes = 24
	if len(r) > markerRunes {
		r = r[len(r)-markerRunes:]
	}
	return strings.TrimSpace(string(r))
}

// composerStillHolds reports whether marker is still sitting on the composer
// line at the bottom of the pane, not merely present in scrollback.
// Codex/Grok echo the submitted prompt into the transcript above an empty
// input box; a whole-pane match would look stuck forever and keep pressing
// Return into the next turn. The check is the last non-empty line only: that
// is the composer after submit (› / ❯) or the end of a wrapped unsent prompt.
func composerStillHolds(rawPane, marker string) bool {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return false
	}
	text := strings.TrimRight(pane.StripANSI(rawPane), "\n")
	if text == "" {
		return false
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		return strings.Contains(line, marker)
	}
	return false
}

// sleepCtx waits for d, or returns early when the context ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// KillPTYSession stops and removes a remote PTY session. Kill errors when the
// session already exited, which is treated as success.
func KillPTYSession(ctx context.Context, remote TerminalExecutor, id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	_, _ = remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty kill %q", id))
	_, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf("aiman pty forget %q", id))
	if err != nil && !strings.Contains(err.Error(), "not_found") && !strings.Contains(err.Error(), "has exited") {
		return err
	}
	return nil
}

// RecreatePTYSession replaces a holder-backed session: kill, wait until it is
// gone, then create. Creating while the previous holder is still running
// fails with "already exists" and leaves a dead pane. A stale "already exists"
// after get reports gone is killed once more and retried.
func RecreatePTYSession(ctx context.Context, remote TerminalExecutor, spec PTYSpec) error {
	if err := waitPTYGone(ctx, remote, spec.ID); err != nil {
		return err
	}
	err := CreatePTYSession(ctx, remote, spec)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		return err
	}
	_ = KillPTYSession(ctx, remote, spec.ID)
	if err := waitPTYGone(ctx, remote, spec.ID); err != nil {
		return err
	}
	return CreatePTYSession(ctx, remote, spec)
}

func waitPTYGone(ctx context.Context, remote TerminalExecutor, id string) error {
	_ = KillPTYSession(ctx, remote, id)
	deadline := time.Now().Add(8 * time.Second)
	for PTYSessionExists(ctx, remote, id) {
		if time.Now().After(deadline) {
			return fmt.Errorf("pty: session %s did not stop in time", id)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// PTYRuntimeAvailable reports whether the remote can host PTY sessions right
// now: aiman installed, serve running, runtime reachable.
func PTYRuntimeAvailable(ctx context.Context, remote TerminalExecutor) bool {
	out, err := remote.Execute(ctx, remoteAimanPreamble+"aiman pty list >/dev/null 2>&1 && echo YES || echo NO")
	return err == nil && strings.Contains(out, "YES")
}

// ServeAvailable reports whether the remote's agent API is up and answering the
// surface a remote-side create needs.
//
// Probed with session.list rather than a plain connection check: that is the
// same handler path session.create goes through, so a serve that is running but
// cannot serve sessions is reported as unavailable rather than accepted and then
// failing mid-create.
func ServeAvailable(ctx context.Context, remote TerminalExecutor) bool {
	out, err := remote.Execute(ctx, remoteAimanPreamble+"aiman session list >/dev/null 2>&1 && echo YES || echo NO")
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
		if err := SendPTYFileConfirmed(ctx, remote, terminalID(s), path, prompt); err != nil {
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
	// Both runtimes are torn down regardless of the recorded backend, because
	// the record is not reliable: a session created by an in-pane agent runs
	// under the PTY runtime but reaches the dashboard through discovery and the
	// live stream, neither of which carries Backend — so it reads as tmux and
	// only tmux was killed, leaving the holder's directory behind and the
	// session listed forever. Each teardown is a no-op when its runtime holds
	// nothing under this handle.
	if err := KillPTYSession(ctx, remote, terminalID(s)); err != nil {
		return err
	}
	if strings.TrimSpace(s.TmuxSession) == "" {
		return nil
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

// scanPTYSessionsCmd asks the remote for its PTY sessions, answering with an
// empty list — never a non-zero exit — when the runtime simply isn't there.
//
// That distinction is the whole point: a remote with no aiman binary genuinely
// has no PTY sessions, whereas a failed SSH call means we could not ask. If
// both looked alike, a transient failure would report "this host has zero PTY
// sessions" for a host discovery had marked as scanned, and the merge step
// would take that as proof the known ones are dead and drop them from the
// dashboard — the same way swallowed tmux scan errors used to make live
// sessions flicker out of the list.
const scanPTYSessionsCmd = remoteAimanPreamble +
	`if command -v aiman >/dev/null 2>&1; then aiman pty list 2>/dev/null || printf '{"sessions":[]}'; else printf '{"sessions":[]}'; fi`

// ScanPTYSessions asks the remote serve daemon what PTY sessions it currently
// holds. Remotes without the runtime yield an empty list; an error means the
// remote could not be asked, and callers must not read that as "none".
func ScanPTYSessions(ctx context.Context, remote TerminalExecutor) ([]PTYRecord, error) {
	out, err := remote.Execute(ctx, scanPTYSessionsCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to scan pty sessions: %w", err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		// A call that succeeded but said nothing means no runtime and so no
		// sessions. Only a *failed* call is ambiguous, and that is handled above.
		return nil, nil
	}
	var result struct {
		Sessions []PTYRecord `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return nil, fmt.Errorf("parsing pty session list: %w", err)
	}
	return result.Sessions, nil
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
	command := reviveAgentCommand(s.AgentName, s.WorkingDirectory)
	resumed := agenthook.WithResume(command, nativeID)
	if resumed == command && nativeID == "" {
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
		Command: resumed,
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

// reviveAgentCommand maps a persisted AgentName ("Codex CLI") to the binary
// plus interactive flags. WithResume must see the binary, not the display name,
// or it produces `Codex resume <id> CLI` and the holder falls through to bash.
func reviveAgentCommand(agentName, worktree string) string {
	cmd := strings.TrimSpace(agentName)
	if known, ok := infraAgent.FindKnown(agentName); ok {
		cmd = known.Command
	}
	return skills.EnsureInteractiveLaunch(cmd, worktree)
}

// NativeSessionID resolves the vendor conversation id for a session. The
// remote sidecar file the hooks maintain wins: it reflects what the agent is
// actually working on right now, while the stored field may lag behind.
// A sidecar whose transcript path belongs to a different agent is ignored so
// revive cannot --resume a Claude id into grok (or the reverse).
func NativeSessionID(ctx context.Context, remote TerminalExecutor, s *domain.Session) string {
	command := reviveAgentCommand(s.AgentName, s.WorkingDirectory)
	if safe := agenthook.SafeSessionID(s.ID); safe != "" {
		out, err := remote.Execute(ctx, fmt.Sprintf("cat \"$HOME/.aiman/native-sessions/%s\" 2>/dev/null || true", safe))
		if err == nil {
			if n := agenthook.ParseStored([]byte(out)); n.ID != "" {
				if agenthook.NativeIdentityFitsCommand(command, n.Path) {
					agenthook.ApplyReport(s, n, time.Now())
					return n.ID
				}
			}
		}
	}
	if !agenthook.NativeIdentityFitsCommand(command, s.AgentSessionPath) {
		return ""
	}
	return strings.TrimSpace(s.AgentSessionID)
}
