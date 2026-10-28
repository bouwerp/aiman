package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/quality"
	"github.com/bouwerp/aiman/internal/usecase"
)

// adoptRestartAgent points a session at the agent it is about to be restarted
// with, dropping the previous agent's identity when they differ. It returns
// true when the agent changed (caller must also clear the remote sidecar and
// skip native --resume).
//
// A session carries the running agent's native session id, transcript path,
// title, hook state and launch model. None of that describes a different agent, and a latched
// AgentEnded would render the freshly started session as already exited. A
// restart with the same agent is a resume, so its transcript and state are still
// its own and are kept.
func adoptRestartAgent(s *domain.Session, agentName string) bool {
	if s == nil {
		return false
	}
	switched := s.AgentName != "" && !strings.EqualFold(s.AgentName, agentName)
	if switched {
		s.AgentSessionID = ""
		s.AgentSessionPath = ""
		s.AgentTitle = ""
		s.AgentEnded = false
		s.HookState = ""
		s.HookStateMessage = ""
		s.HookStateSource = ""
		s.HookStateSeq = 0
		s.LaunchModel = ""
		s.LaunchEffort = ""
	}
	s.AgentName = agentName
	return switched
}

// restartPrep is what a restart's PrepareSession call needs, captured at
// dispatch time.
type restartPrep struct {
	Cfg        *config.Config
	Session    domain.Session
	WorkingDir string
	SessionCfg domain.SessionConfig
	Snapshot   *domain.SessionSnapshot
	// ChecklistDir is the laptop's user checklist directory pushed before
	// the kit is built; empty skips the push.
	ChecklistDir string
}

// prepareRestartSession refreshes the session's checklists and prepares the
// agent launch with the session's stored launch model and effort, which
// adoptRestartAgent has already cleared on an agent switch. The kit's
// DoneWhen is nil, so the skill engine rewrites only the checklist block and
// keeps the Done-when block from create: that text stays fixed for the
// session's life, and the Done-when to-do is not touched. Kit problems come
// back as notes and never fail the restart.
func prepareRestartSession(ctx context.Context, remote domain.RemoteExecutor, engine domain.SkillEngine, p restartPrep) (domain.PreparedSession, []string, error) {
	kit, notes := restartKit(ctx, remote, p)
	agent := p.SessionCfg.Agent.WithSessionLaunch(p.Session)
	prepared, err := engine.PrepareSession(ctx, remote, p.WorkingDir, agent, p.SessionCfg.Skills, p.SessionCfg.PromptFree, nil, p.Snapshot, kit)
	return prepared, notes, err
}

// restartKit pushes the user checklists and rebuilds the checklist half of
// the kit. Only repository sessions get one; a hidden helper (reviewer,
// handover) keeps Repo and Role, which pick its launch flags.
func restartKit(ctx context.Context, remote domain.RemoteExecutor, p restartPrep) (domain.QualityKit, []string) {
	s := p.Session
	kit := domain.QualityKit{Repo: s.RepoName, Role: s.Role}
	if s.RepoName == "" || s.RepoName == "No Repository" || s.Role.Hidden() {
		return kit, nil
	}
	var notes []string
	if p.ChecklistDir != "" {
		if _, err := usecase.PushUserChecklists(ctx, remote, p.ChecklistDir); err != nil {
			notes = append(notes, fmt.Sprintf("Checklists not pushed: %v", err))
		}
	}
	var repoCfg config.RepoBootstrap
	if p.Cfg != nil {
		repoCfg, _ = config.LookupRepo(p.Cfg.Repos, s.RepoName)
	}
	built, buildNotes, err := usecase.BuildQualityKit(ctx, remote, usecase.KitInput{
		Repo:     s.RepoName,
		Worktree: s.WorktreePath,
		Role:     s.Role,
		RepoCfg:  repoCfg,
		Restart:  true,
	})
	notes = append(notes, buildNotes...)
	if err != nil {
		return kit, append(notes, fmt.Sprintf("Quality kit not built: %v", err))
	}
	return built, notes
}

// laptopChecklistDir is the local user checklist directory pushed to the
// remote, or "" when the home directory is unknown.
func laptopChecklistDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, quality.UserChecklistDir)
}
