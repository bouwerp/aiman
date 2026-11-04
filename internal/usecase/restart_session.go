package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// AdoptRestartAgent points a session at the agent it is about to be restarted
// with, dropping the previous agent's identity when they differ. It returns
// true when the agent changed (caller must also clear the remote sidecar and
// skip native --resume).
//
// A session carries the running agent's native session id, transcript path,
// title, hook state and launch model. None of that describes a different agent, and a latched
// AgentEnded would render the freshly started session as already exited. A
// restart with the same agent is a resume, so its transcript and state are still
// its own and are kept.
func AdoptRestartAgent(s *domain.Session, agentName string) bool {
	if s == nil {
		return false
	}
	switched := s.AgentName != "" && !sameAgent(s.AgentName, agentName)
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
		s.Tier = ""
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
// AdoptRestartAgent has already cleared on an agent switch. The kit's
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
		if _, err := PushUserChecklists(ctx, remote, p.ChecklistDir); err != nil {
			notes = append(notes, fmt.Sprintf("Checklists not pushed: %v", err))
		}
	}
	var repoCfg config.RepoBootstrap
	if p.Cfg != nil {
		repoCfg, _ = config.LookupRepo(p.Cfg.Repos, s.RepoName)
	}
	built, buildNotes, err := BuildQualityKit(ctx, remote, KitInput{
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

// sameAgent reports whether two agent names mean one agent: a display name
// and its binary ("Claude Code", "claude") do.
func sameAgent(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	ba, bb := reviveAgentBinary(a), reviveAgentBinary(b)
	return ba != "" && ba == bb
}

// defaultRestartHandoffTimeout bounds the handover write and the fallback
// summary together when RestartRequest leaves it unset.
const defaultRestartHandoffTimeout = 90 * time.Second

// RestartRequest is one restart or agent switch. Session is the session to
// restart, already passed through AdoptRestartAgent; on success it is the row
// to save. SessionCfg.Agent is the agent to launch, and SessionCfg.FreshAgent
// says the agent changed.
type RestartRequest struct {
	Cfg        *config.Config
	Session    *domain.Session
	SessionCfg domain.SessionConfig
	// ChecklistDir is the laptop's checklist directory; empty pushes nothing.
	ChecklistDir  string
	LegacyStopped bool
	NoUpdateGuard bool
	// HandoffTimeout bounds the handover write and the fallback summary.
	HandoffTimeout time.Duration
	// WriteHandover writes the handover on the session's host. Nil asks serve
	// over the remote; serve itself passes its in-process write.
	WriteHandover func(ctx context.Context, reason, agent string) (HandoverResult, error)
	// EnsureIgnored adds aiman's ignore rule to the worktree's .gitignore.
	EnsureIgnored func(ctx context.Context, root string) error
	// ClearIdentity wipes the stored vendor identity on an agent switch, which
	// a Save cannot do.
	ClearIdentity func(ctx context.Context, id string) error
	// Status reports progress worth showing; Log records detail. Either may be nil.
	Status func(msg string)
	Log    func(format string, args ...any)
}

func (r RestartRequest) status(msg string) {
	if r.Status != nil {
		r.Status(msg)
	}
}

func (r RestartRequest) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// RestartWorkingDir is the directory a session's agent restarts in.
func RestartWorkingDir(s domain.Session) string {
	if s.WorkingDirectory != "" {
		return s.WorkingDirectory
	}
	return s.WorktreePath
}

// RestartSession replaces a session's agent process under the same session ID:
// handover, prepared launch, new process, handoff prompt. It never touches the
// worktree's contents beyond the ignore rule and the handover file. The TUI and
// serve's session.restart both run it.
func RestartSession(ctx context.Context, remote domain.RemoteExecutor, engine domain.SkillEngine, req RestartRequest) error {
	s, cfg := req.Session, req.SessionCfg
	if s == nil {
		return fmt.Errorf("no session to restart")
	}
	if err := legacyRestartBlocked(*s, req.LegacyStopped); err != nil {
		return err
	}
	if cfg.Agent == nil {
		return fmt.Errorf("no agent selected for restart")
	}
	s.AgentName = cfg.Agent.Name
	workingDir := RestartWorkingDir(*s)
	if workingDir == "" {
		return fmt.Errorf("session has no working directory")
	}

	gitignoreRoot := restartRoot(*s, workingDir)
	if req.EnsureIgnored != nil {
		if err := req.EnsureIgnored(ctx, gitignoreRoot); err != nil {
			req.logf("gitignore update skipped: %v", err)
		}
	}

	// A missing handover falls back to the agent-written summary; either
	// failure still lets the restart continue.
	req.status("Writing handover...")
	mode, note := restartHandoff(ctx, remote, *s, workingDir, req)
	if note != "" {
		req.logf("handover: %s", note)
		req.status(note)
	}

	req.status(fmt.Sprintf("Preparing %s command...", cfg.Agent.Name))
	agentCmd := cfg.Agent.Command
	var prompt string
	resumeSnapshot := cfg.PriorSnapshot
	if mode != HandoffNone {
		resumeSnapshot = nil
	}
	if engine != nil {
		prepared, notes, err := prepareRestartSession(ctx, remote, engine, restartPrep{
			Cfg:          req.Cfg,
			Session:      *s,
			WorkingDir:   workingDir,
			SessionCfg:   cfg,
			Snapshot:     resumeSnapshot,
			ChecklistDir: req.ChecklistDir,
		})
		for _, n := range notes {
			req.logf("prepare: %s", n)
			req.status(n)
		}
		if err == nil {
			agentCmd = prepared.Command
			prompt = prepared.InitialPrompt
		} else {
			req.logf("prepare failed, launching %s bare: %v", agentCmd, err)
		}
	}
	prompt = restartPrompt(prompt, mode)
	prompt = InjectSharedContext(ctx, remote, workingDir, s.Group, s.RepoName, prompt)
	WriteMuseSessionID(ctx, remote, workingDir, agentCmd, s.ID)
	if cfg.FreshAgent {
		clearRemoteNativeSidecar(ctx, remote, s.ID)
		if req.ClearIdentity != nil {
			if err := req.ClearIdentity(ctx, s.ID); err != nil {
				req.logf("clear agent identity: %v", err)
			}
		}
	}
	agentCmd = withRemoteNativeResume(ctx, remote, s, agentCmd, cfg.FreshAgent)

	env := map[string]string{}
	awsCfg := s.AWSConfig
	if cfg.AWSConfig != nil {
		awsCfg = cfg.AWSConfig
	}
	if awsCfg != nil {
		env = SharedSessionAWSEnv(awsCfg.SourceProfile, awsCfg.Region)
		s.AWSConfig = awsCfg
	}
	for k, v := range SessionRuntimeEnv(s) {
		env[k] = v
	}

	req.status(fmt.Sprintf("Restarting agent in %s...", s.TerminalName))
	return restartTerminal(ctx, remote, s, restartTerminalSpec{
		WorkingDir:    workingDir,
		WorktreeRoot:  gitignoreRoot,
		AgentCmd:      agentCmd,
		Env:           env,
		Secrets:       cfg.EnvSecrets,
		Prompt:        prompt,
		NoUpdateGuard: req.NoUpdateGuard,
		LegacyStopped: req.LegacyStopped,
	})
}

func restartHandoff(ctx context.Context, remote domain.RemoteExecutor, s domain.Session, workingDir string, req RestartRequest) (HandoffMode, string) {
	timeout := req.HandoffTimeout
	if timeout <= 0 {
		timeout = defaultRestartHandoffTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reason, agent := restartHandoverReason(req.SessionCfg), restartHandoverAgent(req.SessionCfg)
	write := req.WriteHandover
	if write == nil {
		write = func(ctx context.Context, reason, agent string) (HandoverResult, error) {
			return WriteHandoverOnRemote(ctx, remote, s.ID, reason, agent)
		}
	}
	return prepareRestartHandoff(ctx, remote, s, workingDir, timeout, func(ctx context.Context) (HandoverResult, error) {
		return write(ctx, reason, agent)
	})
}
