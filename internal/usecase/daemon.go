package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/robfig/cron/v3"
)

// SendMode selects how serve delivers a prompt to an agent.
type SendMode string

const (
	// SendNote queues the text behind serve's delivery gate.
	SendNote SendMode = "note"
	// SendAnswer types the text at once, but only while the agent shows a dialog.
	SendAnswer SendMode = "answer"
)

// PromptSender hands text to serve, which owns typing into terminals.
type PromptSender interface {
	// SendPrompt returns delivered=false without error when an answer found no dialog to answer.
	SendPrompt(ctx context.Context, sessionID, text string, mode SendMode) (delivered bool, err error)
}

// defaultTriggerPollSecs applies when a rule has no PollFrequencySecs; it
// matches the wizard default. The 30 s daemon tick is the floor.
const defaultTriggerPollSecs = 300

const unblockPrompt = "Please carry on autonomously as far as you can until it is absolutely impossible."

type Daemon struct {
	cfg         *config.Config
	db          domain.SessionRepository
	flowManager *FlowManager
	sender      PromptSender
	capture     func(ctx context.Context, s domain.Session) (string, error)
	// lastPolled is keyed by rule session id. Run is single-goroutine, so no lock.
	lastPolled map[string]time.Time
}

func NewDaemon(cfg *config.Config, db domain.SessionRepository, fm *FlowManager, sender PromptSender) *Daemon {
	d := &Daemon{
		cfg:         cfg,
		db:          db,
		flowManager: fm,
		sender:      sender,
		lastPolled:  make(map[string]time.Time),
	}
	d.capture = d.capturePane
	return d
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
			d.poll(ctx)
		}
	}
}

// poll runs one cycle of every polling duty. Each step logs and swallows its own
// failure so one bad source cannot stop the others, which is why nothing is returned.
func (d *Daemon) poll(ctx context.Context) {
	// 1. Poll GitHub Issues
	if err := d.pollGitHub(ctx); err != nil {
		log.Printf("GitHub polling error: %v", err)
	}

	// 2. Monitor tmux panes for blocked agents
	if err := d.monitorPanes(ctx); err != nil {
		log.Printf("Pane monitoring error: %v", err)
	}

	// 3. Trigger Scheduled Prompts
	if err := d.pollScheduledPrompts(ctx); err != nil {
		log.Printf("Scheduled prompt polling error: %v", err)
	}
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

		if !d.dueForPoll(s.ID, s.AutonomousConfig.PollFrequencySecs, time.Now()) {
			continue
		}

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
					ReuseWorkspace: s.AutonomousConfig.ReuseWorkspace,
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

// dueForPoll reports whether a rule's poll interval has elapsed and, when it
// has, records now as the rule's last poll.
func (d *Daemon) dueForPoll(ruleID string, freqSecs int, now time.Time) bool {
	if freqSecs <= 0 {
		freqSecs = defaultTriggerPollSecs
	}
	last, polled := d.lastPolled[ruleID]
	if polled && now.Sub(last) < time.Duration(freqSecs)*time.Second {
		return false
	}
	d.lastPolled[ruleID] = now
	return true
}

func (d *Daemon) capturePane(ctx context.Context, s domain.Session) (string, error) {
	var remoteRoot string
	for _, r := range d.cfg.Remotes {
		if r.Host == s.RemoteHost {
			remoteRoot = r.Root
			break
		}
	}
	return CaptureSessionPane(ctx, local.NewExecutor(remoteRoot), s)
}

func (d *Daemon) monitorPanes(ctx context.Context) error {
	sessions, err := d.db.List(ctx)
	if err != nil {
		return err
	}

	for _, s := range sessions {
		if s.Mode == domain.SessionModeAutonomous && s.Status == domain.SessionStatusActive {
			out, err := d.capture(ctx, s)
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
				log.Printf("Detected hung input in autonomous session %s", s.ID)
				// Answer mode types only while serve also sees a dialog; otherwise
				// nothing is sent and the next tick re-checks.
				if _, err := d.sender.SendPrompt(ctx, s.ID, unblockPrompt, SendAnswer); err != nil {
					log.Printf("Failed to unblock session %s: %v", s.ID, err)
				}
			}
		}
	}
	return nil
}

func (d *Daemon) pollScheduledPrompts(ctx context.Context) error {
	prompts, err := d.db.ListScheduledPrompts(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

	for _, p := range prompts {
		schedule, err := parser.Parse(p.CronExpr)
		if err != nil {
			log.Printf("Invalid cron expr for prompt %s: %v", p.ID, err)
			continue
		}

		lastRun := p.LastRunAt
		if lastRun.IsZero() {
			lastRun = p.CreatedAt
		}

		nextRun := schedule.Next(lastRun)
		if !now.Before(nextRun) {
			log.Printf("Triggering scheduled prompt %s", p.ID)
			// LastRunAt advances only once every target has the prompt, so a
			// prompt fired while serve is down retries on the next tick.
			if err := d.triggerPrompt(ctx, &p); err != nil {
				log.Printf("Scheduled prompt %s not delivered, retrying next tick: %v", p.ID, err)
				continue
			}

			p.LastRunAt = now
			if err := d.db.SaveScheduledPrompt(ctx, &p); err != nil {
				// Not fatal, but the prompt will fire again next tick without this.
				log.Printf("Failed to record run time for scheduled prompt %s: %v", p.ID, err)
			}
		}
	}
	return nil
}

// triggerPrompt queues the prompt for every target session that still exists.
func (d *Daemon) triggerPrompt(ctx context.Context, p *domain.ScheduledPrompt) error {
	var errs []error
	for _, sessID := range p.SessionIDs {
		if _, err := d.db.Get(ctx, sessID); err != nil {
			continue
		}
		if _, err := d.sender.SendPrompt(ctx, sessID, p.Prompt, SendNote); err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", sessID, err))
		}
	}
	return errors.Join(errs...)
}
