package usecase

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
)

// kitRecordingEngine records the kit each PrepareSession call receives.
type kitRecordingEngine struct{ kits []domain.QualityKit }

func (e *kitRecordingEngine) Sync(context.Context) error          { return nil }
func (e *kitRecordingEngine) ListSkills() ([]domain.Skill, error) { return nil, nil }
func (e *kitRecordingEngine) PrepareSession(_ context.Context, _ domain.RemoteExecutor, _ string, a domain.Agent, _ []domain.Skill, _ bool, _ *domain.Issue, _ *domain.SessionSnapshot, kit domain.QualityKit) (domain.PreparedSession, error) {
	e.kits = append(e.kits, kit)
	return domain.PreparedSession{Command: a.Command}, nil
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// kitWorktree makes a git repository holding a.go and a repo checklist for
// *.go, under a temp $HOME.
func kitWorktree(t *testing.T) (wt, home string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	wt = filepath.Join(t.TempDir(), "repo")
	writeTestFile(t, filepath.Join(wt, "a.go"), "package a\n")
	writeTestFile(t, filepath.Join(wt, ".aiman/checklists/go.md"), "---\napplies_to: \"*.go\"\n---\n- vet\n")
	if out, err := exec.Command("git", "-C", wt, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return wt, home
}

func TestRestartPassesKitWithoutDoneWhen(t *testing.T) {
	wt, home := kitWorktree(t)
	eng := &kitRecordingEngine{}
	cfg := &config.Config{Repos: map[string]config.RepoBootstrap{"o/r": {DoneWhen: []string{"PR open"}}}}
	_, _, err := prepareRestartSession(context.Background(), local.NewExecutor(""), eng, restartPrep{
		Cfg:          cfg,
		Session:      domain.Session{ID: "s1", RepoName: "o/r", WorktreePath: wt},
		WorkingDir:   wt,
		SessionCfg:   domain.SessionConfig{Agent: &domain.Agent{Name: "Claude", Command: "claude"}},
		ChecklistDir: filepath.Join(home, "laptop-checklists"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.kits) != 1 {
		t.Fatalf("PrepareSession calls %d, want 1", len(eng.kits))
	}
	kit := eng.kits[0]
	if kit.DoneWhen != nil {
		t.Fatalf("DoneWhen %v, want nil so the Done-when block is kept", kit.DoneWhen)
	}
	if kit.Repo != "o/r" {
		t.Fatalf("Repo %q", kit.Repo)
	}
	var names []string
	for _, c := range kit.Checklists {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"go"}) {
		t.Fatalf("checklists %v, want [go]", names)
	}
}

func TestRestartKitSkipsReviewerSessions(t *testing.T) {
	wt, _ := kitWorktree(t)
	eng := &kitRecordingEngine{}
	_, _, err := prepareRestartSession(context.Background(), local.NewExecutor(""), eng, restartPrep{
		Session:    domain.Session{ID: "r1", RepoName: "o/r", WorktreePath: wt, Role: domain.SessionRoleReviewer},
		WorkingDir: wt,
		SessionCfg: domain.SessionConfig{Agent: &domain.Agent{Name: "Codex", Command: "codex"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if kit := eng.kits[0]; len(kit.Checklists) != 0 || kit.Role != domain.SessionRoleReviewer || kit.Repo != "o/r" {
		t.Fatalf("reviewer kit %+v", kit)
	}
}

func dirtySession(agent string) *domain.Session {
	return &domain.Session{
		ID: "s1", AgentName: agent,
		AgentSessionID: "old-id", AgentSessionPath: "/old/path.jsonl",
		AgentTitle: "old title", AgentEnded: true,
		HookState: domain.AgentStateIdle, HookStateMessage: "old message",
		HookStateSource: "session_end", HookStateSeq: 7,
	}
}

// Switching agent leaves the previous one's identity on the record. None of it
// describes the agent about to start, and a latched AgentEnded would render the
// fresh session as already exited.
func TestRestartWithADifferentAgentClearsTheOldIdentity(t *testing.T) {
	s := dirtySession("codex")
	if !AdoptRestartAgent(s, "claude") {
		t.Fatal("expected switched=true when the agent changes")
	}

	if s.AgentName != "claude" {
		t.Errorf("agent not switched: %q", s.AgentName)
	}
	if s.AgentSessionID != "" || s.AgentSessionPath != "" || s.AgentTitle != "" {
		t.Errorf("previous agent's identity survived: %+v", s)
	}
	if s.AgentEnded {
		t.Error("a fresh agent must not start out marked as ended")
	}
	if s.HookState != "" || s.HookStateSource != "" || s.HookStateSeq != 0 {
		t.Errorf("previous agent's hook state survived: %+v", s)
	}
}

// Restarting with the same agent is a resume: its transcript and state are still
// its own and must be kept.
func TestRestartWithTheSameAgentKeepsItsIdentity(t *testing.T) {
	s := dirtySession("codex")
	if AdoptRestartAgent(s, "Codex") {
		t.Fatal("expected switched=false for a same-agent resume")
	}

	if s.AgentSessionID != "old-id" || s.AgentSessionPath != "/old/path.jsonl" {
		t.Errorf("a same-agent restart must keep the transcript: %+v", s)
	}
	if s.HookStateSeq != 7 {
		t.Errorf("hook state should survive a resume: %+v", s)
	}
}

func TestAdoptRestartAgentTreatsDisplayNameAsTheSameAgent(t *testing.T) {
	s := dirtySession("Claude Code")
	if AdoptRestartAgent(s, "claude") {
		t.Fatal("display name and binary counted as a switch")
	}
	if s.AgentSessionID == "" {
		t.Fatal("same-agent restart lost the vendor id")
	}
	if !AdoptRestartAgent(s, "codex") {
		t.Fatal("a different agent did not count as a switch")
	}
}

// restartSessionRemote records commands and written files. pty get reports nothing
// running, so a recreate goes straight to create.
type restartSessionRemote struct {
	domain.RemoteExecutor
	recordingTerminal
	files map[string]string
}

func (r *restartSessionRemote) Execute(ctx context.Context, cmd string) (string, error) {
	return r.recordingTerminal.Execute(ctx, cmd)
}

func (r *restartSessionRemote) WriteFile(_ context.Context, path string, b []byte) error {
	if r.files == nil {
		r.files = map[string]string{}
	}
	r.files[path] = string(b)
	return nil
}

func TestRestartSessionSwitchOrder(t *testing.T) {
	remote := &restartSessionRemote{}
	s := dirtySession("claude")
	s.WorkingDirectory = t.TempDir()
	AdoptRestartAgent(s, "codex")
	var calls []string
	err := RestartSession(context.Background(), remote, &agentRecordingEngine{}, RestartRequest{
		Session: s,
		SessionCfg: domain.SessionConfig{
			Agent: &domain.Agent{Name: "codex", Command: "codex"}, FreshAgent: true,
			EnvSecrets: []domain.Secret{{Key: "K", Value: "v"}},
		},
		WriteHandover: func(_ context.Context, reason, agent string) (HandoverResult, error) {
			calls = append(calls, "handover:"+reason+":"+agent)
			return HandoverResult{Exists: true}, nil
		},
		ClearIdentity: func(context.Context, string) error {
			calls = append(calls, "clear")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"handover:agent_switch:codex", "clear"}) {
		t.Fatalf("calls %v", calls)
	}
	if s.Backend != domain.BackendPTY || s.Status != domain.SessionStatusActive {
		t.Fatalf("session %+v", s)
	}
	params := remote.files["$HOME/.aiman/tmp/aiman-pty-"+s.ID+".json"]
	for _, want := range []string{`"command":"codex"`, `"K":"v"`} {
		if !strings.Contains(params, want) {
			t.Fatalf("pty params %s lack %s", params, want)
		}
	}
	if !strings.Contains(remote.files["/tmp/aiman-prompt-"+s.ID], HandoverPrompt) {
		t.Fatalf("prompt not delivered: %v", remote.files)
	}
}

func TestRestartSessionRefusesLegacyBeforeTouchingAnything(t *testing.T) {
	remote := &restartSessionRemote{}
	s := legacyRestartSession()
	err := RestartSession(context.Background(), remote, nil, RestartRequest{
		Session:    s,
		SessionCfg: domain.SessionConfig{Agent: &domain.Agent{Name: "claude", Command: "claude"}},
		WriteHandover: func(context.Context, string, string) (HandoverResult, error) {
			t.Fatal("handover written for a legacy row")
			return HandoverResult{}, nil
		},
	})
	if !errors.Is(err, domain.ErrLegacyTmuxSession) {
		t.Fatalf("err = %v", err)
	}
	if remote.all() != "" {
		t.Fatalf("remote touched: %s", remote.all())
	}
}
