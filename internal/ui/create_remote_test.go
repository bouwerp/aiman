package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// The remote has to run the agent's command, but the session should still read
// as its display name.
func TestRemoteCreateSpecCarriesCommandAndName(t *testing.T) {
	spec := remoteCreateSpecFrom(domain.SessionConfig{
		Agent:          &domain.Agent{Name: "Claude Code", Command: "claude"},
		Repo:           domain.Repo{Name: "realfi"},
		Branch:         "WTB-1-thing",
		IssueKey:       "WTB-1",
		SessionBackend: domain.BackendPTY,
		InitialPrompt:  "do the thing",
		PromptFree:     false,
	})
	if spec.Agent != "claude" {
		t.Errorf("the remote must be given the command to run, got %q", spec.Agent)
	}
	if spec.AgentName != "Claude Code" {
		t.Errorf("display name lost: %q", spec.AgentName)
	}
	for _, pair := range [][2]string{
		{spec.Repo, "realfi"}, {spec.Branch, "WTB-1-thing"},
		{spec.Issue, "WTB-1"}, {spec.Backend, domain.BackendPTY},
		{spec.Prompt, "do the thing"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("got %q, want %q", pair[0], pair[1])
		}
	}
}

// Handing over a session whose state only exists on this machine would silently
// drop that state, so those are created locally.
func TestCanCreateRemotelyRefusesLocalOnlyState(t *testing.T) {
	base := func() domain.SessionConfig {
		return domain.SessionConfig{Agent: &domain.Agent{Name: "Claude Code", Command: "claude"}}
	}
	if ok, why := canCreateRemotely(base()); !ok {
		t.Fatalf("a plain session should be handed over, refused because: %s", why)
	}

	cases := map[string]func(*domain.SessionConfig){
		"aws credentials": func(c *domain.SessionConfig) { c.AWSConfig = &domain.AWSConfig{} },
		"env secrets":     func(c *domain.SessionConfig) { c.EnvSecrets = []domain.Secret{{Key: "K"}} },
		"openrouter key":  func(c *domain.SessionConfig) { c.OpenRouterAPIKey = "sk-x" },
		"autonomous":      func(c *domain.SessionConfig) { c.AutonomousConfig = &domain.AutonomousConfig{} },
		"prior snapshot":  func(c *domain.SessionConfig) { c.PriorSnapshot = &domain.SessionSnapshot{} },
		"attach existing": func(c *domain.SessionConfig) { c.AttachExisting = true },
		"reuse workspace": func(c *domain.SessionConfig) { c.ReuseWorkspace = true },
		"skills":          func(c *domain.SessionConfig) { c.Skills = []domain.Skill{{Name: "s"}} },
	}
	for name, mutate := range cases {
		cfg := base()
		mutate(&cfg)
		ok, why := canCreateRemotely(cfg)
		if ok {
			t.Errorf("%s: should be created locally", name)
		}
		if why == "" {
			t.Errorf("%s: refusal should say why", name)
		}
	}

	// No agent command: nothing to run remotely.
	noAgent := base()
	noAgent.Agent = nil
	if ok, _ := canCreateRemotely(noAgent); ok {
		t.Error("a session with no agent cannot be created remotely")
	}
}

func reconcileModel(sessions ...domain.Session) *Model {
	return &Model{
		cfg:            &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}},
		allSessions:    sessions,
		syncReconciled: map[string]bool{},
	}
}

// A remotely-created session has no sync, and sync-health never looks at it
// because it only inspects sessions that already have one.
func TestNeedsSyncReconcile(t *testing.T) {
	remoteMade := domain.Session{
		ID: "a", RemoteHost: "regent0", WorktreePath: "/home/code/repos/x",
		Status: domain.SessionStatusActive,
	}
	m := reconcileModel(remoteMade)
	if !m.needsSyncReconcile(remoteMade) {
		t.Error("a session with a worktree and no sync needs one")
	}

	withSync := remoteMade
	withSync.MutagenSyncID = "aiman-sync-a"
	if m.needsSyncReconcile(withSync) {
		t.Error("a session that already has a sync must be left to sync-health")
	}

	withLocal := remoteMade
	withLocal.LocalPath = "/home/me/.aiman/work/a"
	if m.needsSyncReconcile(withLocal) {
		t.Error("a session with a local path already has a mirror")
	}

	noWorktree := remoteMade
	noWorktree.WorktreePath = ""
	if m.needsSyncReconcile(noWorktree) {
		t.Error("nothing on the remote to mirror")
	}

	unknownRemote := remoteMade
	unknownRemote.RemoteHost = "somewhere-else"
	if m.needsSyncReconcile(unknownRemote) {
		t.Error("an unresolvable remote cannot be synced")
	}
}

// One attempt per run: a failing sync retried on every tick would be worse than
// no sync at all.
func TestReconcileMissingSyncsTriesEachSessionOnce(t *testing.T) {
	m := reconcileModel(
		domain.Session{ID: "a", RemoteHost: "regent0", WorktreePath: "/x", Status: domain.SessionStatusActive},
		domain.Session{ID: "b", RemoteHost: "regent0", WorktreePath: "/y", Status: domain.SessionStatusActive},
	)
	if cmd := m.reconcileMissingSyncs(); cmd == nil {
		t.Fatal("expected a sync to be started")
	}
	// One per pass: creating a sync waits for the initial mirror.
	if len(m.syncReconciled) != 1 {
		t.Errorf("expected one attempt per pass, got %d", len(m.syncReconciled))
	}
	if cmd := m.reconcileMissingSyncs(); cmd == nil {
		t.Fatal("the second session should be picked up on the next pass")
	}
	if len(m.syncReconciled) != 2 {
		t.Errorf("expected both attempted after two passes, got %d", len(m.syncReconciled))
	}
	if cmd := m.reconcileMissingSyncs(); cmd != nil {
		t.Error("nothing left to do, but work was scheduled again")
	}
}
