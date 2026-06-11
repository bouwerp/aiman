package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// creatingSession tracks a session being created in the background. The
// placeholder appears in the session list immediately after the summary is
// confirmed, so the user can keep using other sessions while creation runs.
type creatingSession struct {
	placeholder domain.Session
	cfg         domain.SessionConfig // captured config, used for worktree-exists retry
	remote      config.Remote        // remote captured at confirmation time
	steps       []string             // verbose progress steps, shown in the preview panel
	failed      bool
	errMsg      string
}

// newCreatingPlaceholder builds the placeholder session shown in the list
// while creation runs in the background.
func newCreatingPlaceholder(cfg domain.SessionConfig, remote config.Remote) domain.Session {
	label := cfg.Branch
	if label == "" {
		label = cfg.IssueKey
	}
	if label == "" {
		label = "new session"
	}
	agentName := ""
	if cfg.Agent != nil {
		agentName = cfg.Agent.Name
	}
	now := time.Now()
	return domain.Session{
		ID:          fmt.Sprintf("pending-%d", now.UnixNano()),
		IssueKey:    cfg.IssueKey,
		Branch:      cfg.Branch,
		RepoName:    cfg.Repo.Name,
		RemoteHost:  remote.Host,
		TmuxSession: label,
		AgentName:   agentName,
		Status:      domain.SessionStatusProvisioning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// addStep appends a progress step, deduplicating immediate repeats.
func (cs *creatingSession) addStep(step string) {
	step = strings.TrimSpace(step)
	if step == "" {
		return
	}
	if n := len(cs.steps); n > 0 && cs.steps[n-1] == step {
		return
	}
	cs.steps = append(cs.steps, step)
}

// renderCreatingPanel renders the preview panel for a session that is being
// created in the background: header, metadata, the verbose step log, and the
// failure message when creation failed.
func (m *Model) renderCreatingPanel(cs *creatingSession, contentW int) string {
	s := cs.placeholder
	var b strings.Builder

	if cs.failed {
		b.WriteString(activeStyle.Render("Session") + "  " + failStyle.Render("creation failed") + "\n")
	} else {
		b.WriteString(activeStyle.Render("Session") + "  creating in background…\n")
	}

	var meta []string
	if s.RepoName != "" {
		meta = append(meta, s.RepoName)
	}
	if s.RemoteHost != "" {
		meta = append(meta, s.RemoteHost)
	}
	if s.AgentName != "" {
		meta = append(meta, "agent:"+s.AgentName)
	}
	if s.IssueKey != "" {
		meta = append(meta, s.IssueKey)
	}
	if len(meta) > 0 {
		b.WriteString(truncateRunes(strings.Join(meta, " · "), contentW) + "\n")
	}

	b.WriteString(statusStyle.Render(strings.Repeat("─", max(1, contentW))) + "\n")

	// Show the most recent steps that fit the panel; the latest step carries
	// the in-progress marker.
	steps := cs.steps
	maxSteps := max(5, m.height-16)
	if len(steps) > maxSteps {
		steps = steps[len(steps)-maxSteps:]
	}
	for i, step := range steps {
		marker := successStyle.Render("✓")
		if i == len(steps)-1 && !cs.failed {
			marker = "…"
		} else if cs.failed && i == len(steps)-1 {
			marker = failStyle.Render("✗")
		}
		b.WriteString(fmt.Sprintf("%s %s\n", marker, truncateRunes(step, max(10, contentW-2))))
	}
	if len(steps) == 0 && !cs.failed {
		b.WriteString("… Starting…\n")
	}

	if cs.failed {
		b.WriteString("\n" + failStyle.Render("Error: "+cs.errMsg) + "\n")
		b.WriteString(statusStyle.Render("Press ctrl+k to dismiss this entry.") + "\n")
	} else {
		b.WriteString("\n" + statusStyle.Render("You can switch to and use other sessions while this one is created.") + "\n")
	}

	return b.String()
}
