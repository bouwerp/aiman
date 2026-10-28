package usecase

import (
	"context"
	"encoding/json"
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

func TestWriteMuseSessionIDOnlyForMuse(t *testing.T) {
	r := &terminalRemote{}
	WriteMuseSessionID(context.Background(), r, "/wt", "claude", "sess-1")
	if len(r.written) != 0 {
		t.Fatalf("claude wrote %v", r.written)
	}
	WriteMuseSessionID(context.Background(), r, "/wt", "muse --trust-workspace", "sess-1")
	got := string(r.written["/wt/"+domain.AimanSessionIDFileName])
	if got != "sess-1\n" {
		t.Fatalf("%q", got)
	}
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
// reporting failure: such a host simply has no sessions.
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

func TestApplyKiloAllowEnv(t *testing.T) {
	r := &terminalRemote{}
	env := map[string]string{}
	ApplyKiloAllowEnv(context.Background(), r, "kilo --foo", env)
	if env["KILO_CONFIG"] == "" || env["KILO_CONFIG_CONTENT"] == "" {
		t.Fatalf("kilo restart/create must inject allow-env, got %v", env)
	}
	env = map[string]string{}
	ApplyKiloAllowEnv(context.Background(), r, "claude", env)
	if len(env) != 0 {
		t.Fatalf("non-kilo must not get KILO_*: %v", env)
	}
}

// ptyCreateEnv returns the env CreatePTYSession sent in its params file.
func ptyCreateEnv(t *testing.T, spec PTYSpec) map[string]string {
	t.Helper()
	r := &terminalRemote{}
	if err := CreatePTYSession(context.Background(), r, spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	for path, body := range r.written {
		if !strings.HasPrefix(path, "/tmp/aiman-pty-") {
			continue
		}
		var params struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(body, &params); err != nil {
			t.Fatal(err)
		}
		return params.Env
	}
	t.Fatalf("no params file written: %v", r.written)
	return nil
}

func TestCreatePTYSessionAddsUpdateGuardEnv(t *testing.T) {
	env := ptyCreateEnv(t, PTYSpec{ID: "sid", Command: "claude --resume x", Env: map[string]string{"AWS_PROFILE": "prod"}})
	if env["DISABLE_AUTOUPDATER"] != "1" || env["AWS_PROFILE"] != "prod" {
		t.Fatalf("claude env = %v", env)
	}
	env = ptyCreateEnv(t, PTYSpec{ID: "sid", Command: "MUSE_X=1 /home/u/.local/bin/muse --yolo"})
	if env["MUSE_NO_AUTO_UPDATE"] != "1" {
		t.Fatalf("muse env = %v", env)
	}
	if env := ptyCreateEnv(t, PTYSpec{ID: "sid", Command: "bash"}); len(env) != 0 {
		t.Fatalf("an unknown command got guard env: %v", env)
	}
}

func TestCreatePTYSessionUserEnvWinsOverGuard(t *testing.T) {
	env := ptyCreateEnv(t, PTYSpec{ID: "sid", Command: "pi", Env: map[string]string{"PI_SKIP_VERSION_CHECK": "0"}})
	if env["PI_SKIP_VERSION_CHECK"] != "0" {
		t.Fatalf("guard overwrote the caller's value: %v", env)
	}
}

func TestCreatePTYSessionNoUpdateGuard(t *testing.T) {
	env := ptyCreateEnv(t, PTYSpec{ID: "sid", Command: "claude", NoUpdateGuard: true, Env: map[string]string{"A": "b"}})
	if _, ok := env["DISABLE_AUTOUPDATER"]; ok {
		t.Fatalf("mode off still added guard env: %v", env)
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

// Terminating a discovered session with no id must not shell out to the PTY
// runtime with an empty handle.
func TestTerminateSessionTerminalSkipsPTYWithoutAnID(t *testing.T) {
	r := &terminalRemote{}
	if err := TerminateSessionTerminal(context.Background(), r, domain.Session{TmuxSession: "WTB-1"}); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if strings.Contains(strings.Join(r.commands, "\n"), "aiman pty") {
		t.Errorf("no id should mean no pty call: %s", r.commands)
	}
}

func TestReviveIfNeededRoutesAndRefuses(t *testing.T) {
	ctx := context.Background()

	// live pty session: no-op
	r := &terminalRemote{output: map[string]string{
		"aiman pty get": `{"session":{"id":"pid","status":"running"}}`,
	}}
	revived, err := ReviveIfNeeded(ctx, r, &domain.Session{ID: "pid", Backend: domain.BackendPTY}, ReviveOptions{})
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
	revived, err = ReviveIfNeeded(ctx, r, &s, ReviveOptions{})
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
	if _, err := ReviveIfNeeded(ctx, r, &codex, ReviveOptions{}); err != nil {
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
	if _, err := ReviveIfNeeded(ctx, r, &s2, ReviveOptions{}); err == nil {
		t.Fatal("revival without an agent session id must fail")
	}
}

// TestReviveHonoursUpdateGuardSetting checks that agents.update.mode off
// reaches revive: no guard env and no guard flags, while the default adds
// both.
func TestReviveHonoursUpdateGuardSetting(t *testing.T) {
	revive := func(t *testing.T, agentName string, opts ReviveOptions) string {
		t.Helper()
		r := &terminalRemote{output: map[string]string{
			"aiman pty get":   `{"error":{"code":"not_found","message":"pty session not found"}}`,
			"native-sessions": "",
		}}
		s := domain.Session{ID: "g", Backend: domain.BackendPTY, AgentName: agentName,
			AgentSessionID: "conv-1", WorkingDirectory: "/w"}
		if _, err := ReviveIfNeeded(context.Background(), r, &s, opts); err != nil {
			t.Fatalf("%s revive: %v", agentName, err)
		}
		var payload string
		for path, body := range r.written {
			if strings.HasPrefix(path, "/tmp/aiman-pty-g") {
				payload += string(body)
			}
		}
		if payload == "" {
			t.Fatalf("no create payload: %v", r.written)
		}
		return payload
	}

	if got := revive(t, "claude", ReviveOptions{}); !strings.Contains(got, `"DISABLE_AUTOUPDATER":"1"`) {
		t.Errorf("default claude revive lacks the guard env: %s", got)
	}
	if got := revive(t, "claude", ReviveOptions{NoUpdateGuard: true}); strings.Contains(got, "DISABLE_AUTOUPDATER") {
		t.Errorf("mode off claude revive has the guard env: %s", got)
	}
	if got := revive(t, "codex", ReviveOptions{}); !strings.Contains(got, "check_for_update_on_startup=false") {
		t.Errorf("default codex revive lacks the guard flag: %s", got)
	}
	got := revive(t, "codex", ReviveOptions{NoUpdateGuard: true})
	if strings.Contains(got, "check_for_update_on_startup") {
		t.Errorf("mode off codex revive has the guard flag: %s", got)
	}
	if !strings.Contains(got, "--disable in_app_updates") {
		t.Errorf("codex revive must keep --disable in_app_updates, its update dialog exits the holder: %s", got)
	}
}

func TestNativeSessionIDPrefersSidecar(t *testing.T) {
	r := &terminalRemote{output: map[string]string{"native-sessions": `{"id":"sidecar-id"}`}}
	s := domain.Session{ID: "sess", Backend: domain.BackendPTY, AgentName: "claude", AgentSessionID: "stored-id"}
	got := NativeSessionID(context.Background(), r, &s)
	if got != "sidecar-id" {
		t.Fatalf("NativeSessionID = %q", got)
	}

	r = &terminalRemote{}
	s = domain.Session{ID: "sess", Backend: domain.BackendPTY, AgentName: "claude", AgentSessionID: "stored-id"}
	if got := NativeSessionID(context.Background(), r, &s); got != "stored-id" {
		t.Fatalf("fallback to stored id failed: %q", got)
	}
}

func TestNativeSessionIDRejectsCrossAgentSidecar(t *testing.T) {
	r := &terminalRemote{output: map[string]string{
		"native-sessions": `{"id":"00ca0f57","path":"/home/code/.claude/projects/x/00ca0f57.jsonl"}`,
	}}
	s := domain.Session{
		ID: "sess", Backend: domain.BackendPTY, AgentName: "Grok Build CLI",
		AgentSessionID: "stored-grok", AgentSessionPath: "/home/code/.grok/sessions/stored-grok",
	}
	if got := NativeSessionID(context.Background(), r, &s); got != "stored-grok" {
		t.Fatalf("claude sidecar into grok must fall back to stored grok id, got %q", got)
	}
}
