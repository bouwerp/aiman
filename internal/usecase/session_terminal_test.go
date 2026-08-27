package usecase

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// terminalRemote records Execute commands and hands back canned output.
type terminalRemote struct {
	commands []string
	output   map[string]string
	execErr  error

	written map[string][]byte
}

func (r *terminalRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.commands = append(r.commands, cmd)
	if r.execErr != nil {
		return "", r.execErr
	}
	for frag, out := range r.output {
		if strings.Contains(cmd, frag) {
			return out, nil
		}
	}
	return "", nil
}

func (r *terminalRemote) CaptureTmuxPane(context.Context, string) (string, error) { return "", nil }

func (r *terminalRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if r.written == nil {
		r.written = map[string][]byte{}
	}
	r.written[path] = content
	return nil
}

func TestScanPTYSessionsParsesRuntimeList(t *testing.T) {
	r := &terminalRemote{output: map[string]string{
		"aiman pty list": `{
  "type": "pty_list",
  "sessions": [
    {"id": "abc", "name": "feat-x", "dir": "/srv/app@feat-x", "status": "running"}
  ]
}`,
	}}
	got, err := ScanPTYSessions(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 session, got %d", len(got))
	}
	if got[0].ID != "abc" || got[0].Status != "running" || got[0].Dir != "/srv/app@feat-x" {
		t.Fatalf("unexpected record: %+v", got[0])
	}
}

// A remote with no runtime answers with an empty list and exit 0 — the scan
// command guards on `command -v aiman` for exactly this.
func TestScanPTYSessionsEmptyOnMissingRuntime(t *testing.T) {
	r := &terminalRemote{output: map[string]string{"aiman pty list": `{"sessions":[]}`}}
	got, err := ScanPTYSessions(context.Background(), r)
	if err != nil {
		t.Fatalf("a remote without the runtime is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no sessions, got %d", len(got))
	}
}

// "Could not ask" must never look like "there are none": discovery marks the
// host scanned, and the merge step reads a missing session on a scanned host
// as dead. Silently returning nil here dropped live PTY sessions from the
// dashboard on any transient SSH failure.
func TestScanPTYSessionsFailsRatherThanReportingNone(t *testing.T) {
	r := &terminalRemote{execErr: fmt.Errorf("ssh: connection lost")}
	if _, err := ScanPTYSessions(context.Background(), r); err == nil {
		t.Fatal("expected a transport failure to propagate")
	}

	// Unparseable output is equally "unanswered", not "empty".
	garbled := &terminalRemote{output: map[string]string{"aiman pty list": "not json"}}
	if _, err := ScanPTYSessions(context.Background(), garbled); err == nil {
		t.Fatal("expected unparseable output to propagate")
	}

	// A call that succeeded but said nothing means no runtime, hence no
	// sessions — only a failed call is ambiguous.
	silent := &terminalRemote{output: map[string]string{"aiman pty list": ""}}
	got, err := ScanPTYSessions(context.Background(), silent)
	if err != nil || len(got) != 0 {
		t.Fatalf("silent success should be an empty list, got %v / %v", got, err)
	}
}

// The scan must survive a remote that has no aiman binary at all without
// reporting failure, or discovery would break for every tmux-only remote.
func TestScanPTYSessionsCmdGuardsOnBinaryPresence(t *testing.T) {
	if !strings.Contains(scanPTYSessionsCmd, "command -v aiman") {
		t.Error("scan command must check for the binary before invoking it")
	}
	if !strings.Contains(scanPTYSessionsCmd, `{"sessions":[]}`) {
		t.Error("scan command must fall back to a valid empty list, not a non-zero exit")
	}
}

// Recreate must not call create while get still reports running: that leaves
// the old holder in place and the dashboard attaches to a dead pane.
func TestRecreatePTYSessionWaitsUntilGoneThenCreates(t *testing.T) {
	r := &killThenGoneRemote{running: true}
	err := RecreatePTYSession(context.Background(), r, PTYSpec{
		ID: "sid", Name: "feat", Dir: "/d", Command: "claude",
	})
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	joined := strings.Join(r.commands, "\n")
	if !strings.Contains(joined, "aiman pty kill") {
		t.Fatalf("missing kill: %s", joined)
	}
	if !strings.Contains(joined, "aiman pty create") {
		t.Fatalf("missing create: %s", joined)
	}
	createAt := -1
	for i, c := range r.commands {
		if strings.Contains(c, "aiman pty create") {
			createAt = i
		}
	}
	if createAt >= 0 && r.runningAt[createAt] {
		t.Fatal("create ran while the previous holder was still running")
	}
}

func TestRecreatePTYSessionRetriesWhenCreateSeesAlreadyExists(t *testing.T) {
	r := &staleExistsRemote{creates: 0}
	err := RecreatePTYSession(context.Background(), r, PTYSpec{
		ID: "sid", Name: "feat", Dir: "/d", Command: "claude",
	})
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if r.creates < 2 {
		t.Fatalf("stale already-exists must be killed and created again, creates=%d", r.creates)
	}
}

type staleExistsRemote struct {
	commands []string
	creates  int
	written  map[string][]byte
}

func (r *staleExistsRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.commands = append(r.commands, cmd)
	switch {
	case strings.Contains(cmd, "pty get"):
		return `{"error":{"code":"not_found"}}`, fmt.Errorf("not_found")
	case strings.Contains(cmd, "pty create"):
		r.creates++
		if r.creates == 1 {
			return `{"error":{"message":"already exists"}}`, fmt.Errorf("pty: session sid already exists")
		}
		return `{"session":{"id":"sid","status":"running"}}`, nil
	}
	return "", nil
}

func (r *staleExistsRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if r.written == nil {
		r.written = map[string][]byte{}
	}
	r.written[path] = content
	return nil
}

type killThenGoneRemote struct {
	commands  []string
	running   bool
	runningAt []bool
	written   map[string][]byte
}

func (r *killThenGoneRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.runningAt = append(r.runningAt, r.running)
	r.commands = append(r.commands, cmd)
	switch {
	case strings.Contains(cmd, "pty kill"), strings.Contains(cmd, "pty forget"):
		r.running = false
		return "", nil
	case strings.Contains(cmd, "pty get"):
		if r.running {
			return `{"session":{"id":"sid","status":"running"}}`, nil
		}
		return `{"error":{"code":"not_found"}}`, fmt.Errorf("not_found")
	case strings.Contains(cmd, "pty create"):
		if r.running {
			return `{"error":{"message":"already exists"}}`, fmt.Errorf("pty: session sid already exists")
		}
		return `{"session":{"id":"sid","status":"running"}}`, nil
	}
	return "", nil
}

func (r *killThenGoneRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if r.written == nil {
		r.written = map[string][]byte{}
	}
	r.written[path] = content
	return nil
}

func TestPaneShellCommandKeepsShellInsideLoginCommand(t *testing.T) {
	got := PaneShellCommand("export PATH=foo; grok --always-approve")
	if !strings.HasPrefix(got, "bash -l -c '") {
		t.Fatalf("want a login-shell wrapper, got %q", got)
	}
	if strings.Contains(got, "'; exec bash") {
		t.Fatal("tmux execs one pane command; a sibling exec bash never runs and remain-on-exit shows a dead pane")
	}
	if !strings.Contains(got, "; exec bash -i'") {
		t.Fatalf("agent exit must exec bash inside the same -c, got %q", got)
	}
}

func TestApplyOpenCodeAllowEnv(t *testing.T) {
	r := &terminalRemote{}
	env := map[string]string{}
	ApplyOpenCodeAllowEnv(context.Background(), r, "opencode --foo", env)
	if env["OPENCODE_CONFIG"] == "" || env["OPENCODE_CONFIG_CONTENT"] == "" {
		t.Fatalf("opencode restart/create must inject allow-env, got %v", env)
	}
	env = map[string]string{}
	ApplyOpenCodeAllowEnv(context.Background(), r, "claude", env)
	if len(env) != 0 {
		t.Fatalf("non-opencode must not get OPENCODE_*: %v", env)
	}
}

func TestCreatePTYSessionWritesParamsFile(t *testing.T) {
	r := &terminalRemote{}
	err := CreatePTYSession(context.Background(), r, PTYSpec{
		ID: "sid", Name: "feat", Dir: "/d", Command: "claude",
		Env: map[string]string{"AWS_PROFILE": "prod"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	found := false
	for path, body := range r.written {
		if strings.HasPrefix(path, "/tmp/aiman-pty-") && strings.Contains(string(body), `"AWS_PROFILE":"prod"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("params file with env not written: %v", r.written)
	}
	last := r.commands[len(r.commands)-1]
	if !strings.Contains(last, "aiman pty create --params-file") || !strings.Contains(last, "rm -f") {
		t.Fatalf("create command malformed: %s", last)
	}
}

func TestSendSessionPromptRoutesByBackend(t *testing.T) {
	ctx := context.Background()

	// tmux session keeps the legacy path
	r := &terminalRemote{}
	tmux := domain.Session{TmuxSession: "WTB-1", ID: "tid"}
	if err := SendSessionPrompt(ctx, r, tmux, "hello"); err != nil {
		t.Fatalf("tmux prompt: %v", err)
	}
	if len(r.commands) == 0 || !strings.Contains(r.commands[0], "tmux send-keys") {
		t.Fatalf("tmux prompt did not use send-keys: %v", r.commands)
	}

	// pty session goes through input --file plus an Enter press
	r = &terminalRemote{}
	ptySess := domain.Session{ID: "pid", Backend: domain.BackendPTY}
	if err := SendSessionPrompt(ctx, r, ptySess, "do things"); err != nil {
		t.Fatalf("pty prompt: %v", err)
	}
	joined := strings.Join(r.commands, "\n")
	if !strings.Contains(joined, "aiman pty input \"pid\" --file") {
		t.Fatalf("pty prompt missing file input: %s", joined)
	}
	var promptBody []byte
	for _, body := range r.written {
		promptBody = body
	}
	if string(promptBody) != "do things" {
		t.Fatalf("prompt file content wrong: %q", promptBody)
	}
}

func TestTerminateSessionTerminalRoutesByBackend(t *testing.T) {
	ctx := context.Background()

	r := &terminalRemote{}
	ptySess := domain.Session{ID: "pid", Backend: domain.BackendPTY}
	if err := TerminateSessionTerminal(ctx, r, ptySess); err != nil {
		t.Fatalf("pty terminate: %v", err)
	}
	joined := strings.Join(r.commands, "\n")
	if !strings.Contains(joined, "aiman pty kill") || !strings.Contains(joined, "aiman pty forget") {
		t.Fatalf("pty terminate commands wrong: %s", joined)
	}

	r = &terminalRemote{}
	tmux := domain.Session{TmuxSession: "WTB-1"}
	if err := TerminateSessionTerminal(ctx, r, tmux); err != nil {
		t.Fatalf("tmux terminate: %v", err)
	}
	if !strings.Contains(r.commands[0], "tmux kill-session") {
		t.Fatalf("tmux terminate wrong: %s", r.commands[0])
	}
}

func TestCaptureSessionPaneRoutesByBackend(t *testing.T) {
	ctx := context.Background()
	r := &terminalRemote{output: map[string]string{
		"aiman pty capture": `{"type":"pane_read","text":"pane-bytes"}`,
	}}
	out, err := CaptureSessionPane(ctx, r, domain.Session{ID: "pid", Backend: domain.BackendPTY})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if out != "pane-bytes" {
		t.Fatalf("capture text = %q", out)
	}
}

func TestReviveIfNeededRoutesAndRefuses(t *testing.T) {
	ctx := context.Background()

	// tmux sessions never revive
	r := &terminalRemote{}
	revived, err := ReviveIfNeeded(ctx, r, &domain.Session{ID: "x"})
	if err != nil || revived {
		t.Fatalf("tmux session must not revive: %v / %v", revived, err)
	}

	// live pty session: no-op
	r = &terminalRemote{output: map[string]string{
		"aiman pty get": `{"session":{"id":"pid","status":"running"}}`,
	}}
	revived, err = ReviveIfNeeded(ctx, r, &domain.Session{ID: "pid", Backend: domain.BackendPTY})
	if err != nil || revived {
		t.Fatalf("live session must not revive: %v / %v", revived, err)
	}

	// dead session with a vendor conversation id: relaunches with --resume
	r = &terminalRemote{output: map[string]string{
		"aiman pty get":   `{"error":{"code":"not_found","message":"pty session not found"}}`,
		"native-sessions": "",
	}}
	s := domain.Session{ID: "dead", Backend: domain.BackendPTY, AgentName: "claude",
		AgentSessionID: "conv-42", WorkingDirectory: "/w", TmuxSession: "feat"}
	revived, err = ReviveIfNeeded(ctx, r, &s)
	if err != nil || !revived {
		t.Fatalf("expected revival: %v / %v", revived, err)
	}
	var created bool
	for path, body := range r.written {
		if strings.HasPrefix(path, "/tmp/aiman-pty-dead") && strings.Contains(string(body), `"claude --resume conv-42"`) {
			created = true
		}
	}
	if !created {
		t.Fatalf("resume command not in create payload: %v", r.written)
	}

	// Display names are not binaries. Codex CLI must revive as `codex resume`,
	// with the same trust/update flags a fresh create uses, or it exits and
	// the holder drops to a shell.
	r = &terminalRemote{output: map[string]string{
		"aiman pty get":   `{"error":{"code":"not_found","message":"pty session not found"}}`,
		"native-sessions": "",
	}}
	codex := domain.Session{ID: "cx", Backend: domain.BackendPTY, AgentName: "Codex CLI",
		AgentSessionID: "thread-9", WorkingDirectory: "/wt"}
	if _, err := ReviveIfNeeded(ctx, r, &codex); err != nil {
		t.Fatalf("codex revival: %v", err)
	}
	var got string
	for _, body := range r.written {
		got += string(body)
	}
	for _, want := range []string{
		`"codex resume thread-9`,
		"--dangerously-bypass-approvals-and-sandbox",
		"--dangerously-bypass-hook-trust",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("codex revive missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, `"Codex resume`) || strings.Contains(got, `"Codex CLI resume`) {
		t.Errorf("revive used the display name as a binary: %s", got)
	}

	// dead session without any vendor id: refuses rather than silently forking
	// a fresh conversation
	r = &terminalRemote{output: map[string]string{"aiman pty get": "not_found"}}
	s2 := domain.Session{ID: "lost", Backend: domain.BackendPTY, AgentName: "claude"}
	if _, err := ReviveIfNeeded(ctx, r, &s2); err == nil {
		t.Fatal("revival without an agent session id must fail")
	}
}

func TestNativeSessionIDPrefersSidecar(t *testing.T) {
	r := &terminalRemote{output: map[string]string{"native-sessions": `{"id":"sidecar-id"}`}}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY, AgentSessionID: "stored-id"}
	got := NativeSessionID(context.Background(), r, &s)
	if got != "sidecar-id" {
		t.Fatalf("NativeSessionID = %q", got)
	}

	r = &terminalRemote{}
	s = domain.Session{ID: "sess", Backend: domain.BackendPTY, AgentSessionID: "stored-id"}
	if got := NativeSessionID(context.Background(), r, &s); got != "stored-id" {
		t.Fatalf("fallback to stored id failed: %q", got)
	}
}
