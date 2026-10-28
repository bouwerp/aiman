package ui

import (
	"context"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

// restartTerminalSpec is what the terminal step of a restart needs once the
// handover is written and the agent command is prepared.
type restartTerminalSpec struct {
	WorkingDir   string
	WorktreeRoot string
	AgentCmd     string
	// Env is the session runtime and AWS environment; Secrets are added on top.
	Env           map[string]string
	Secrets       []domain.Secret
	Prompt        string
	NoUpdateGuard bool
	// LegacyStopped is the user's confirmation that a legacy tmux session was
	// stopped on the host. aiman never stops tmux itself, and two agents on one
	// conversation would fight, so a legacy row restarts only with it set.
	LegacyStopped bool
}

// legacyRestartBlocked refuses a restart of a legacy tmux row the user has not
// confirmed as stopped.
func legacyRestartBlocked(s domain.Session, stopped bool) error {
	if s.IsLegacyTmux() && !stopped {
		return domain.NewLegacyTmuxError(s)
	}
	return nil
}

// restartTerminal replaces the session's agent process in the PTY runtime of
// the remote serve daemon and delivers the restart prompt. On success the
// session is a pty session (a confirmed legacy row moves over) and active.
func restartTerminal(ctx context.Context, remote usecase.TerminalExecutor, s *domain.Session, spec restartTerminalSpec) error {
	if err := legacyRestartBlocked(*s, spec.LegacyStopped); err != nil {
		return err
	}
	usecase.TrustWorkspacePaths(ctx, remote, spec.WorkingDir, spec.WorktreeRoot)

	env := make(map[string]string, len(spec.Env)+len(spec.Secrets))
	for k, v := range spec.Env {
		env[k] = v
	}
	for _, secret := range spec.Secrets {
		env[secret.Key] = secret.Value
	}
	usecase.ApplyKiloAllowEnv(ctx, remote, spec.AgentCmd, env)
	if err := usecase.RecreatePTYSession(ctx, remote, usecase.PTYSpec{
		ID:            s.ID,
		Name:          s.TmuxSession,
		Dir:           spec.WorkingDir,
		Command:       spec.AgentCmd,
		Env:           env,
		NoUpdateGuard: spec.NoUpdateGuard,
	}); err != nil {
		return fmt.Errorf("failed to restart PTY session: %w", err)
	}
	usecase.DeliverInitialPromptPTY(ctx, remote, s.ID, spec.Prompt)

	s.Backend = domain.BackendPTY
	s.Status = domain.SessionStatusActive
	return nil
}
