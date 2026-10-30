package usecase

import (
	"context"
	"errors"
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
		PromptFree: false,
	}
	sess, err := CreateSessionOnRemote(context.Background(), r, spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.ID != "abc-123" || sess.TerminalName != "WTB-1-thing" {
		t.Errorf("session not parsed: %+v", sess)
	}
	if len(r.written) != 1 {
		t.Fatalf("expected one params file, got %d", len(r.written))
	}
	var body []byte
	for _, v := range r.written {
		body = v
	}
	for _, want := range []string{`"agent":"claude"`, `"agent_name":"Claude Code"`, `"prompt_free":false`} {
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

// A tier travels without an agent, so serve picks the tier's first installed
// entry, and a model travels only with an explicit agent.
func TestCreateSessionOnRemoteSendsTier(t *testing.T) {
	r := &createRemote{out: okCreateResponse}
	_, err := CreateSessionOnRemote(context.Background(), r,
		RemoteCreateSpec{Repo: "r", Branch: "b", Tier: "smart"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body string
	for _, v := range r.written {
		body = string(v)
	}
	if !strings.Contains(body, `"tier":"smart"`) {
		t.Errorf("tier not sent: %s", body)
	}
	if strings.Contains(body, `"agent"`) {
		t.Errorf("a tier must not send an agent: %s", body)
	}

	r = &createRemote{out: okCreateResponse}
	if _, err := CreateSessionOnRemote(context.Background(), r,
		RemoteCreateSpec{Repo: "r", Branch: "b", Agent: "pi", Model: "kimi-k3", Effort: "high"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, v := range r.written {
		body = string(v)
	}
	for _, want := range []string{`"model":"kimi-k3"`, `"effort":"high"`} {
		if !strings.Contains(body, want) {
			t.Errorf("params missing %s: %s", want, body)
		}
	}
}

func TestParseRemoteCreateResultCarriesTier(t *testing.T) {
	sess, err := parseRemoteCreateResult(`{"result":{"session":{"id":"a1","tier":"smart","launch_model":"kimi-k3"}}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sess.Tier != "smart" || sess.LaunchModel != "kimi-k3" {
		t.Fatalf("session = %+v", sess)
	}
}

// An empty agent is left off the params so serve picks from the model tiers.
func TestCreateSessionOnRemoteLeavesEmptyAgentToServe(t *testing.T) {
	r := &createRemote{out: okCreateResponse}
	if _, err := CreateSessionOnRemote(context.Background(), r, RemoteCreateSpec{Repo: "r", Branch: "b"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, v := range r.written {
		if strings.Contains(string(v), `"agent"`) {
			t.Errorf("empty agent was sent: %s", v)
		}
	}
	if len(r.written) != 1 {
		t.Fatalf("written %d params files, want 1", len(r.written))
	}
}

func TestCreateSessionOnRemoteSendsReview(t *testing.T) {
	r := &createRemote{out: okCreateResponse}
	_, err := CreateSessionOnRemote(context.Background(), r,
		RemoteCreateSpec{Agent: "claude", Review: "o/r#7", PromptFree: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var body string
	for _, v := range r.written {
		body = string(v)
	}
	if !strings.Contains(body, `"review":"o/r#7"`) {
		t.Errorf("review not sent: %s", body)
	}
	for _, ban := range []string{`"repo":`, `"branch":`, `"dir":`, `"prompt":`, `"issue":`} {
		if strings.Contains(body, ban) {
			t.Errorf("review params still contain %s: %s", ban, body)
		}
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

// Serve sends backend so an absent value is not read as pty. The laptop must
// keep a present pty, and must not invent one when the field is absent: Save
// keeps an empty backend as "no terminal recorded".
func TestParseRemoteCreateKeepsBackend(t *testing.T) {
	withPty := strings.Replace(okCreateResponse, `"agent_name":"Claude Code"`, `"agent_name":"Claude Code","backend":"pty"`, 1)
	sess, err := parseRemoteCreateResult(withPty)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Backend != domain.BackendPTY {
		t.Fatalf("Backend = %q, want pty", sess.Backend)
	}
	absent, err := parseRemoteCreateResult(okCreateResponse)
	if err != nil {
		t.Fatal(err)
	}
	if absent.Backend != "" {
		t.Fatalf("absent backend = %q, want empty, not an invented backend", absent.Backend)
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

func TestRegisterSessionsOnRemoteToleratesOldServe(t *testing.T) {
	rec := &rpcRecorder{out: "unknown command \"rpc\" for \"aiman\"", err: errors.New("exit status 1")}
	if err := RegisterSessionsOnRemote(context.Background(), rec, []domain.Session{{ID: "a"}}); err != nil {
		t.Fatalf("old serve must be tolerated: %v", err)
	}
	if !strings.Contains(rec.cmd, "aiman rpc session.register") {
		t.Fatalf("cmd = %q", rec.cmd)
	}
	rec = &rpcRecorder{out: `{"result":{"type":"sessions_registered","ids":["a"]}}`}
	if err := RegisterSessionsOnRemote(context.Background(), rec, []domain.Session{{ID: "a"}}); err != nil {
		t.Fatal(err)
	}
}
