package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
)

type Daemon struct {
	cfg         *config.Config
	db          domain.SessionRepository
	flowManager *FlowManager
}

func NewDaemon(cfg *config.Config, db domain.SessionRepository, fm *FlowManager) *Daemon {
	return &Daemon{
		cfg:         cfg,
		db:          db,
		flowManager: fm,
	}
}

func (d *Daemon) Run(ctx context.Context) error {
	log.Println("Aiman Daemon started. Polling for autonomous tasks...")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Daemon shutting down...")
			return nil
		case <-ticker.C:
			if err := d.poll(ctx); err != nil {
				log.Printf("Error during poll cycle: %v", err)
			}
		}
	}
}

func (d *Daemon) poll(ctx context.Context) error {
	// 1. Poll GitHub Issues
	if err := d.pollGitHub(ctx); err != nil {
		log.Printf("GitHub polling error: %v", err)
	}

	// 2. Monitor tmux panes for blocked agents
	if err := d.monitorPanes(ctx); err != nil {
		log.Printf("Pane monitoring error: %v", err)
	}

	return nil
}

func (d *Daemon) pollGitHub(ctx context.Context) error {
	// 1. Fetch all Trigger Rules (Sessions with Mode=AUTONOMOUS and TriggerType=github)
	// We'll list all sessions and filter in memory for now.
	sessions, err := d.db.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list sessions for polling: %w", err)
	}

	for _, s := range sessions {
		if s.Mode != domain.SessionModeAutonomous || s.AutonomousConfig == nil || s.AutonomousConfig.TriggerType != "github" {
			continue
		}

		// TODO: Throttle polling per rule based on s.AutonomousConfig.PollFrequencySecs

		// Calculate active concurrency for this repo trigger
		activeCount := 0
		for _, activeSess := range sessions {
			if activeSess.Mode == domain.SessionModeAutonomous &&
				activeSess.TriggerSource == "github" &&
				activeSess.RepoName == s.AutonomousConfig.GitHubRepo &&
				activeSess.TriggerEventID != "" &&
				(activeSess.Status == domain.SessionStatusProvisioning || activeSess.Status == domain.SessionStatusActive || activeSess.Status == domain.SessionStatusReview) {
				activeCount++
			}
		}

		// 2. Poll the GitHub API via `gh` CLI
		log.Printf("Polling GitHub repo %s for new issues...", s.AutonomousConfig.GitHubRepo)
		args := []string{"issue", "list", "--repo", s.AutonomousConfig.GitHubRepo, "--state", "open", "--json", "number,title,body"}
		labels := strings.Split(s.AutonomousConfig.FilterLabels, ",")
		for _, lbl := range labels {
			lbl = strings.TrimSpace(lbl)
			if lbl != "" {
				args = append(args, "--label", lbl)
			}
		}

		cmd := exec.CommandContext(ctx, "gh", args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			log.Printf("Failed to poll GitHub for repo %s: %v", s.AutonomousConfig.GitHubRepo, err)
			continue
		}

		var issues []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal(out.Bytes(), &issues); err != nil {
			log.Printf("Failed to parse GitHub issues for repo %s: %v", s.AutonomousConfig.GitHubRepo, err)
			continue
		}

		// 3. For each issue, check if an ephemeral agent is already handling it
		for _, issue := range issues {
			if s.AutonomousConfig.MaxConcurrency > 0 && activeCount >= s.AutonomousConfig.MaxConcurrency {
				log.Printf("Max concurrency %d reached for repo %s. Skipping remaining issues.", s.AutonomousConfig.MaxConcurrency, s.AutonomousConfig.GitHubRepo)
				break
			}

			eventID := fmt.Sprintf("%d", issue.Number)
			active, err := d.db.HasActiveSessionForEvent(ctx, "github", eventID)
			if err != nil {
				log.Printf("Error checking active sessions for issue %s: %v", eventID, err)
				continue
			}

			if !active {
				log.Printf("Found new issue %d in %s! Spawning ephemeral agent...", issue.Number, s.AutonomousConfig.GitHubRepo)

				prompt := fmt.Sprintf("You are an autonomous AI agent handling GitHub issue #%d - %s\n\nIssue Description:\n%s\n\n", issue.Number, issue.Title, issue.Body)
				prompt += "Your task is to implement the fix or feature requested in this issue.\n"
				prompt += "When you are finished:\n"
				prompt += "1. Create a Pull Request (PR) with your changes.\n"
				prompt += fmt.Sprintf("2. Summarise your session in detail and post it as a comment on issue #%d, including the PR link.\n", issue.Number)
				prompt += fmt.Sprintf("3. If you need more information or clarification from the user, post a comment on issue #%d and wait for a reply. You must periodically check the issue for new comments using the gh cli.\n", issue.Number)
				prompt += "4. Once the PR is open and you have commented on the issue, you may terminate your session."

				cfg := domain.SessionConfig{
					AdHoc:          false,
					Repo:           domain.Repo{Name: s.AutonomousConfig.GitHubRepo},
					Branch:         fmt.Sprintf("auto-fix-issue-%d", issue.Number),
					Mode:           domain.SessionModeAutonomous,
					TriggerSource:  "github",
					TriggerEventID: eventID,
					AWSConfig:      s.AWSConfig,
					InitialPrompt:  prompt,
					// TODO: Add agent mapping or default agent
				}

				// Ensure the daemon can execute commands locally on the remote host in the correct root
				if s.RemoteHost != "" {
					var remote config.Remote
					for _, r := range d.cfg.Remotes {
						if r.Host == s.RemoteHost {
							remote = r
							break
						}
					}
					cfg.SSHManager = local.NewExecutor(remote.Root)
					cfg.RemoteHost = remote.Host
				}
				if s.AgentName != "" {
					cfg.Agent = &domain.Agent{Name: s.AgentName}
				}

				// Create the ephemeral session synchronously so we can log any immediate failure
				session, err := d.flowManager.CreateSession(ctx, cfg)
				if err != nil {
					log.Printf("Failed to provision ephemeral session for issue %s: %v", eventID, err)
					continue
				}

				// The session starts in PROVISIONING. We should transition it and start it properly.
				// For the daemon, we need a lightweight way to start the session, or we can use
				// a daemon-specific launcher if FlowManager doesn't do the full launch synchronously.
				// Wait, FlowManager.CreateSession handles git worktree setup and db save.
				// To actually launch the agent in tmux, we need to call `LaunchSession` or similar?
				// FlowManager's CreateSession only *sets up* the worktree.
				// Wait, Aiman's FlowManager was decoupled from launching? No, `CreateSession` creates the worktree,
				// but the caller usually calls `m.startBackgroundSession(session, cfg)` in dashboard.go.
				// We need the daemon to execute the tmux launch via SSH.
				log.Printf("Provisioned worktree for ephemeral session %s (ID: %s)", session.Branch, session.ID)
			}
		}
	}

	return nil
}

func (d *Daemon) monitorPanes(ctx context.Context) error {
	sessions, err := d.db.List(ctx)
	if err != nil {
		return err
	}

	for _, s := range sessions {
		// Only monitor active autonomous sessions that have a tmux pane
		if s.Mode == domain.SessionModeAutonomous && s.Status == domain.SessionStatusActive && s.TmuxSession != "" {
			var remoteRoot string
			for _, r := range d.cfg.Remotes {
				if r.Host == s.RemoteHost {
					remoteRoot = r.Root
					break
				}
			}
			executor := local.NewExecutor(remoteRoot)

			out, err := executor.CaptureTmuxPane(ctx, s.TmuxSession)
			if err != nil || out == "" {
				continue
			}

			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) == 0 {
				continue
			}

			// Check the last few lines for a prompt
			checkLines := lines
			if len(checkLines) > 3 {
				checkLines = checkLines[len(checkLines)-3:]
			}

			needsUnblock := false
			for _, line := range checkLines {
				lower := strings.ToLower(strings.TrimSpace(line))
				if strings.Contains(lower, "y/n") ||
					strings.Contains(lower, "yes/no") ||
					strings.Contains(lower, "press enter") ||
					strings.Contains(lower, "allow execution") ||
					strings.Contains(lower, "action required") ||
					strings.Contains(lower, "allow once") {
					needsUnblock = true
					break
				}
			}

			if needsUnblock {
				log.Printf("Detected hung input in autonomous session %s", s.TmuxSession)
				unblockCmd := fmt.Sprintf("tmux send-keys -t %q %q Enter", s.TmuxSession, "Please carry on autonomously as far as you can until it is absolutely impossible.")
				_, _ = executor.Execute(ctx, unblockCmd)
			}
		}
	}
	return nil
}
