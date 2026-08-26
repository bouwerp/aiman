package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// createRemote records what was written and executed.
type createRemote struct {
	cmds     []string
	written  map[string][]byte
	out      string
	writeErr error
}

func (c *createRemote) Execute(_ context.Context, cmd string) (string, error) {
	c.cmds = append(c.cmds, cmd)
	return c.out, nil
}
func (c *createRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.written == nil {
		c.written = map[string][]byte{}
	}
	c.written[path] = content
	return nil
}

const okCreateResponse = `{"result":{"type":"session","session":{"id":"abc-123","name":"impl","group":"WTB-1","branch":"WTB-1-thing","repo_name":"realfi","tmux_session":"WTB-1-thing","worktree_path":"/home/code/repos/x","working_directory":"/home/code/repos/x","agent_name":"Claude Code"}}}`

// The params must never reach a shell: a prompt can contain anything.
func TestCreateSessionOnRemoteSendsParamsAsAFile(t *testing.T) {
	r := &createRemote{out: okCreateResponse}
	spec := RemoteCreateSpec{
		Agent: "claude", AgentName: "Claude Code", Repo: "realfi", Branch: "WTB-1-thing",
		Prompt: `weird "quotes" and $(rm -rf /) and 'ticks'`, Issue: "WTB-1",
		Backend: "pty", PromptFree: false,
	}
	sess, err := CreateSessionOnRemote(context.Background(), r, spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.ID != "abc-123" || sess.TmuxSession != "WTB-1-thing" {
		t.Errorf("session not parsed: %+v", sess)
	}
	if len(r.written) != 1 {
		t.Fatalf("expected one params file, got %d", len(r.written))
	}
	var body []byte
	for _, v := range r.written {
		body = v
	}
	for _, want := range []string{`"agent":"claude"`, `"agent_name":"Claude Code"`, `"backend":"pty"`, `"prompt_free":false`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("params missing %s: %s", want, body)
		}
	}
	// The dangerous text is in the file, not in the command.
	cmd := strings.Join(r.cmds, "\n")
	if strings.Contains(cmd, "rm -rf /") {
		t.Errorf("prompt text reached the command line: %s", cmd)
	}
	if !strings.Contains(cmd, "--params-file") {
		t.Errorf("expected --params-file, got %s", cmd)
	}
}

// prompt_free is always sent: absent means "true" on the wire, and a
// task-driven session needs it false.
func TestCreateSessionOnRemoteAlwaysSendsPromptFree(t *testing.T) {
	for _, want := range []bool{true, false} {
		r := &createRemote{out: okCreateResponse}
		_, err := CreateSessionOnRemote(context.Background(), r,
			RemoteCreateSpec{Agent: "claude", Repo: "r", Branch: "b", PromptFree: want})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		var body string
		for _, v := range r.written {
			body = string(v)
		}
		marker := `"prompt_free":false`
		if want {
			marker = `"prompt_free":true`
		}
		if !strings.Contains(body, marker) {
			t.Errorf("PromptFree=%v not sent: %s", want, body)
		}
	}
}

func TestCreateSessionOnRemoteRequiresAnAgent(t *testing.T) {
	r := &createRemote{out: okCreateResponse}
	if _, err := CreateSessionOnRemote(context.Background(), r, RemoteCreateSpec{Repo: "r"}); err == nil {
		t.Error("an agent command is required")
	}
	if len(r.cmds) != 0 {
		t.Error("nothing should be sent without an agent")
	}
}

func TestParseRemoteCreateResult(t *testing.T) {
	if _, err := parseRemoteCreateResult(""); err == nil {
		t.Error("empty output is an error")
	}
	if _, err := parseRemoteCreateResult("not json at all"); err == nil {
		t.Error("unparseable output is an error")
	}
	// A serve error must surface as one, not as a session with no id.
	if _, err := parseRemoteCreateResult(`{"error":{"code":"create_failed","message":"branch exists"}}`); err == nil {
		t.Error("a serve error must surface")
	} else if !strings.Contains(err.Error(), "branch exists") {
		t.Errorf("error should carry the reason: %v", err)
	}
	// A success with no id is not a success.
	if _, err := parseRemoteCreateResult(`{"result":{"session":{"name":"x"}}}`); err == nil {
		t.Error("a response with no session id is an error")
	}
	sess, err := parseRemoteCreateResult(okCreateResponse)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sess.Status != domain.SessionStatusActive {
		t.Errorf("status = %q", sess.Status)
	}
	if sess.AgentName != "Claude Code" {
		t.Errorf("agent name = %q", sess.AgentName)
	}
}

func TestSanitiseTempName(t *testing.T) {
	cases := map[string]string{
		"WTB-123-thing":    "WTB-123-thing",
		"../../etc/passwd": "------etc-passwd", // 6 leading non-alphanumerics
		"":                 "session",
	}
	for in, want := range cases {
		if got := sanitiseTempName(in); got != want {
			t.Errorf("sanitiseTempName(%q) = %q, want %q", in, got, want)
		}
	}
	// Length is bounded, and nothing that needs quoting survives.
	long := sanitiseTempName(strings.Repeat("a/b ", 100))
	if len(long) > 64 {
		t.Errorf("name should be bounded, got %d chars", len(long))
	}
	for _, bad := range []string{"/", " ", "'", `"`, "$", ";"} {
		if strings.Contains(long, bad) {
			t.Errorf("name contains %q: %q", bad, long)
		}
	}
}
