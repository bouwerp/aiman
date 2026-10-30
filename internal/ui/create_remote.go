package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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
		Name:       cfg.Name,
		Group:      cfg.Group,
		Review:     cfg.ReviewPR,
		PromptFree: cfg.PromptFree,
		Tier:       cfg.Tier,
	}
	if cfg.ReviewPR == "" {
		spec.Repo = cfg.Repo.Name
		spec.Branch = cfg.Branch
		spec.Dir = cfg.Directory
		spec.Prompt = cfg.InitialPrompt
		spec.Issue = cfg.IssueKey
		spec.BaseBranch = cfg.BaseBranch
		spec.Quick = cfg.Quick
		spec.ExistingBranch = cfg.ExistingBranch
	} else {
		spec.PromptFree = true
	}
	if cfg.Agent != nil && !isPreferredAgentChoice(cfg.Agent) {
		spec.Agent = cfg.Agent.Command
		spec.AgentName = cfg.Agent.Name
		spec.Model = cfg.Agent.LaunchModel
		spec.Effort = cfg.Agent.LaunchEffort
	}
	return spec
}

// errPreferredNeedsServe is the create error when no agent was chosen and the
// remote's agent API, the only side that reads agents.preferred, cannot take
// the create.
var errPreferredNeedsServe = errors.New("no agent chosen: only the remote agent API can pick from agents.preferred; select an agent or start the agent API")

// canCreateRemotely reports whether this session can be handed to the remote.
//
// Everything the remote flow needs has to be expressible as create parameters.
// A session carrying local-only state — per-session AWS overrides, secrets from
// this machine, an autonomous-trigger config, a prior snapshot to resume from —
// is created locally instead, because handing it over would silently drop that
// state.
// mustCreateRemotely reports a create that has no laptop fallback. A
// review's PR worktree, scratch directory and to-dos live in serve; the
// local FlowManager has no branch and would name an empty worktree.
func mustCreateRemotely(cfg domain.SessionConfig) bool {
	return strings.TrimSpace(cfg.ReviewPR) != ""
}

func canCreateRemotely(cfg domain.SessionConfig) (bool, string) {
	if mustCreateRemotely(cfg) {
		return true, ""
	}
	switch {
	case cfg.Agent == nil:
		return false, "no agent chosen"
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
		session.Backend = domain.BackendPTY
	}
	return session, nil
}

// createReviewRemotely asks serve to start the review. There is no laptop
// fallback: local CreateSession has no branch and cannot make the PR worktree.
func (m *Model) createReviewRemotely(ctx context.Context, sessionCfg domain.SessionConfig, placeholderID string, sendStatus func(string)) sessionCreateMsg {
	mgr, ok := sessionCfg.SSHManager.(*ssh.Manager)
	if !ok {
		return sessionCreateMsg{err: errors.New("review needs a remote host"), placeholderID: placeholderID}
	}
	if !usecase.ServeAvailable(ctx, mgr) {
		return sessionCreateMsg{err: fmt.Errorf("review needs the agent API on %s", sessionCfg.RemoteHost), placeholderID: placeholderID}
	}
	sendStatus("Handing creation to the remote...")
	session, err := createSessionRemotely(ctx, mgr, sessionCfg, sessionCfg.RemoteHost)
	if err != nil {
		m.logPersistent("remote review create failed for %q: %v", sessionCfg.ReviewPR, err)
		return sessionCreateMsg{err: err, placeholderID: placeholderID}
	}
	m.logPersistent("remote created session %s (%s) on %s; sync and credentials reconcile locally",
		session.TerminalName, session.ID, sessionCfg.RemoteHost)
	if m.db != nil {
		session.UpdatedAt = time.Now()
		if serr := m.db.Save(ctx, session); serr != nil {
			m.logPersistent("failed to save remotely-created session %s: %v", session.ID, serr)
		}
	}
	return sessionCreateMsg{session: *session, placeholderID: placeholderID}
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
