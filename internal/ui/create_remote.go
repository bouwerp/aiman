package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
)

// remoteCreateSpecFrom turns the wizard's decisions into what serve's
// session.create needs.
//
// The agent travels as its command, because that is what the remote has to run;
// its display name goes separately so a session created remotely still reads as
// "Claude Code" rather than "claude".
func remoteCreateSpecFrom(cfg domain.SessionConfig) usecase.RemoteCreateSpec {
	spec := usecase.RemoteCreateSpec{
		Name:           cfg.Name,
		Group:          cfg.Group,
		Repo:           cfg.Repo.Name,
		Branch:         cfg.Branch,
		Dir:            cfg.Directory,
		Prompt:         cfg.InitialPrompt,
		Issue:          cfg.IssueKey,
		BaseBranch:     cfg.BaseBranch,
		Backend:        domain.ResolveSessionBackend(cfg.SessionBackend, ""),
		Quick:          cfg.Quick,
		ExistingBranch: cfg.ExistingBranch,
		PromptFree:     cfg.PromptFree,
	}
	if cfg.Agent != nil {
		spec.Agent = cfg.Agent.Command
		spec.AgentName = cfg.Agent.Name
	}
	return spec
}

// canCreateRemotely reports whether this session can be handed to the remote.
//
// Everything the remote flow needs has to be expressible as create parameters.
// A session carrying local-only state — per-session AWS overrides, secrets from
// this machine, an autonomous-trigger config, a prior snapshot to resume from —
// is created locally instead, because handing it over would silently drop that
// state.
func canCreateRemotely(cfg domain.SessionConfig) (bool, string) {
	switch {
	case cfg.Agent == nil || strings.TrimSpace(cfg.Agent.Command) == "":
		return false, "no agent command"
	case cfg.AWSConfig != nil:
		return false, "per-session AWS credentials are minted on this machine"
	case len(cfg.EnvSecrets) > 0:
		return false, "session secrets come from this machine"
	case cfg.AutonomousConfig != nil:
		return false, "autonomous sessions are configured locally"
	case cfg.PriorSnapshot != nil:
		return false, "resuming from a snapshot is a local flow"
	case cfg.AttachExisting:
		return false, "adopting an existing worktree is a local flow"
	case cfg.ReuseWorkspace:
		return false, "reusing the main clone is a local flow"
	case len(cfg.Skills) > 0:
		return false, "selected skills are prepared locally"
	}
	return true, ""
}

// createSessionRemotely hands creation to the remote's serve and returns the
// session it made.
//
// The session comes back without a mutagen sync or delegated credentials: both
// are made on this machine and cannot be created by the remote. The dashboard
// reconciles them when it next sees the session, which is what makes the create
// itself something the user can walk away from.
func createSessionRemotely(ctx context.Context, mgr *ssh.Manager, cfg domain.SessionConfig, host string) (*domain.Session, error) {
	session, err := usecase.CreateSessionOnRemote(ctx, mgr, remoteCreateSpecFrom(cfg))
	if err != nil {
		return nil, err
	}
	session.RemoteHost = host
	if session.Backend == "" {
		session.Backend = cfg.SessionBackend
	}
	return session, nil
}

// reconcileMissingSyncs is a no-op: mutagen sync is created only by ctrl+y.
func (m *Model) reconcileMissingSyncs() tea.Cmd {
	return nil
}

// needsSyncReconcile is always false: mutagen sync is created only when the
// operator presses ctrl+y.
func (m *Model) needsSyncReconcile(_ domain.Session) bool {
	return false
}
