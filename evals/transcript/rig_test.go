//go:build acceptance

// Package transcripteval is the P6 eval for the phone transcript
// (docs/specs/phone-transcript.md): an in-process serve wired the way
// cmd/aiman/serve.go wires it, with server.NewTranscriptSource locating real
// (redacted) Claude transcript files under a temp HOME.
package transcripteval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/evals/harness"
	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/server"
)

// The fixture is one redacted Claude parent transcript. Its own session id
// and the cwd its background output paths were recorded under:
// /home/code/repos/redacted/project, slug -home-code-repos-redacted-project.
const (
	fixturePath = "../../internal/transcript/testdata/claude/session.jsonl"
	fixtureID   = "e4e4b2f0-736d-e1ee-9f27-ac1d8bb6ea90"
	fixtureCwd  = "/home/code/repos/redacted/project"
	fixtureSlug = "-home-code-repos-redacted-project"
	fixtureDir  = "/tmp/claude-1004/" + fixtureSlug + "/" + fixtureID + "/tasks/"

	// fixtureHead ends after line 34: two Bash calls whose results (lines 35
	// and 36) are not yet written.
	fixtureHead = 34
	// fixtureNextCall is the tool_use on line 51, the first new item after
	// the head; its result is line 52.
	fixtureNextCall     = "toolu_43a794fbe9e8125595099173"
	fixtureNextCallLine = 51
	// fixtureCallID is line 33's tool_use, answered by line 35.
	fixtureCallID = "toolu_7079e64514f65b386d320177"
	// fixtureBgCall and fixtureBgResult are the 0-based indexes of a
	// background Bash call and its result naming task bb0868cae.
	fixtureBgCall   = 56
	fixtureBgResult = 57
	fixtureBgTool   = "toolu_2229e1da175e13b10286b015"
	fixtureBgTask   = "bb0868cae"
)

type rig struct {
	t      *testing.T
	home   string
	socket string
	repo   *sqlite.Repository
	pty    *ptyfake.FakePTY
}

// newRig starts serve under a temp HOME with the production transcript source.
func newRig(t *testing.T) *rig {
	t.Helper()
	home, err := os.MkdirTemp("", "aimantr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	aimanDir := filepath.Join(home, config.DirName)
	if err := os.MkdirAll(aimanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := server.Listen(aimanDir)
	if err != nil {
		t.Skipf("cannot create a unix socket: %v", err)
	}
	repo, err := sqlite.NewRepository(filepath.Join(aimanDir, "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	pty := ptyfake.New()
	srv := server.New(ln, repo, harness.NewFakeRemote(home, pty), harness.FakeCreator{Dir: home, PTY: pty},
		contextstore.NewFiles(filepath.Join(aimanDir, "context")), pty, "eval")
	srv.SetTranscripts(server.NewTranscriptSource(home))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		_ = repo.Close()
	})
	return &rig{t: t, home: home, socket: server.SocketPath(aimanDir), repo: repo, pty: pty}
}

// addSession saves an active pty row and gives it a fake terminal.
func (r *rig) addSession(s domain.Session) {
	r.t.Helper()
	s.Name, s.TerminalName = s.ID, "term-"+s.ID
	s.Backend, s.Status, s.CreatedAt = domain.BackendPTY, domain.SessionStatusActive, time.Now().UTC()
	if err := r.repo.Save(context.Background(), &s); err != nil {
		r.t.Fatal(err)
	}
	r.pty.Add(ptyruntime.SessionInfo{ID: s.ID, Name: s.TerminalName, Dir: s.WorkingDirectory, Command: s.AgentName})
}

// call sends one request as caller; empty is the operator.
func (r *rig) call(caller, method string, params any) server.Response {
	r.t.Helper()
	old, had := os.LookupEnv("AIMAN_ID")
	if caller != "" {
		_ = os.Setenv("AIMAN_ID", caller)
	}
	resp, err := server.Call(r.socket, method, params)
	if had {
		_ = os.Setenv("AIMAN_ID", old)
	} else {
		_ = os.Unsetenv("AIMAN_ID")
	}
	if err != nil {
		r.t.Fatalf("%s: %v", method, err)
	}
	return resp
}

func decode(t *testing.T, resp server.Response, out any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error %s: %s", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func wantCode(t *testing.T, resp server.Response, code string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("want %s, got result %+v", code, resp.Result)
	}
	if resp.Error.Code != code {
		t.Fatalf("code = %s (%q), want %s", resp.Error.Code, resp.Error.Message, code)
	}
}

// fixtureLines returns the fixture's lines, each with its newline.
func fixtureLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func uuidOf(t *testing.T, line string) (uuid, parent string) {
	t.Helper()
	var head struct {
		UUID   string `json:"uuid"`
		Parent string `json:"parentUuid"`
	}
	if err := json.Unmarshal([]byte(line), &head); err != nil {
		t.Fatal(err)
	}
	return head.UUID, head.Parent
}

// lastUUID is the uuid of the last line that has one.
func lastUUID(t *testing.T, lines []string) string {
	t.Helper()
	for i := len(lines) - 1; i >= 0; i-- {
		if u, _ := uuidOf(t, lines[i]); u != "" {
			return u
		}
	}
	t.Fatal("fixture has no uuid")
	return ""
}

// backgroundPair rewrites the fixture's background Bash call and its result
// into a new pair on the active branch after parent: new uuids, tool id and
// task id, and outPath as the output file the result names. The text stays
// the fixture's redacted text.
func backgroundPair(t *testing.T, lines []string, parent, task, outPath string) (pair []string, last string) {
	t.Helper()
	call, result := lines[fixtureBgCall], lines[fixtureBgResult]
	callUUID, callParent := uuidOf(t, call)
	resultUUID, _ := uuidOf(t, result)
	newCall, newResult := "eval-"+task+"-call", "eval-"+task+"-result"
	swap := strings.NewReplacer(
		callUUID, newCall, resultUUID, newResult,
		fixtureBgTool, "toolu_eval_"+task, fixtureBgTask+".output", filepath.Base(outPath),
		fixtureBgTask, task, fixtureDir, filepath.Dir(outPath)+"/",
	)
	call = strings.Replace(swap.Replace(call), `"parentUuid":"`+callParent+`"`, `"parentUuid":"`+parent+`"`, 1)
	return []string{call, swap.Replace(result)}, newResult
}
