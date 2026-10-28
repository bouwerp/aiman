package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// legacyTmuxBadge is the session-row label for a legacy row. aiman cannot
// attach, revive or stop it, so the label names the way out.
const legacyTmuxBadge = " | tmux (legacy) · stop it on the host, then s"

// legacyTmuxTerminateSkip is the terminate step note for a legacy row: the
// confirm dialog showed the stop command and aiman never runs tmux.
const legacyTmuxTerminateSkip = "tmux session left to the user"

// legacyTmuxError is the stop instruction for a legacy row: the host and the
// exact command the user runs there.
func legacyTmuxError(s domain.Session) *domain.LegacyTmuxError {
	var lt *domain.LegacyTmuxError
	if errors.As(domain.NewLegacyTmuxError(s), &lt) {
		return lt
	}
	return &domain.LegacyTmuxError{Host: s.RemoteHost, Name: s.TmuxSession}
}

// legacyTmuxToast refuses an action on a legacy row with the move hint.
func (m *Model) legacyTmuxToast(s domain.Session) tea.Cmd {
	return m.showToast("This session runs in tmux, which aiman no longer runs: "+legacyTmuxError(s).Hint(), true, 15*time.Second)
}

// legacyStopInstructions tells the user to stop the tmux session on its host
// before aiman touches the session. aiman never runs tmux itself.
func legacyStopInstructions(s domain.Session) string {
	lt := legacyTmuxError(s)
	host := lt.Host
	if host == "" {
		host = "the host"
	}
	var b strings.Builder
	b.WriteString(failStyle.Render("This session still runs in tmux on "+host+".") + "\n")
	b.WriteString("aiman no longer runs tmux. Stop it yourself first, on " + host + ":\n\n")
	b.WriteString("  " + activeStyle.Render(lt.StopCommand()) + "\n\n")
	b.WriteString("Confirm only once it is stopped: two agents on one conversation fight.")
	return b.String()
}

// noteLegacyStopped records the user's confirmation that the legacy session
// being restarted was stopped on its host.
func (m *Model) noteLegacyStopped() {
	if s := m.restartingSession; s != nil && s.IsLegacyTmux() {
		m.legacyStoppedID = s.ID
	}
}

// renderLegacyRestartConfirm is the restart confirm for a legacy row: the
// restart moves the session to pty once the user has stopped tmux.
func (m *Model) renderLegacyRestartConfirm(s domain.Session) string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Move Session to pty") + "\n\n")
	b.WriteString(fmt.Sprintf("Session %q\n\n", s.TmuxSession))
	b.WriteString(legacyStopInstructions(s) + "\n\n")
	b.WriteString("The restart then resumes the agent in pty.\n\n")
	b.WriteString(activeStyle.Render("[y]") + " I stopped it, same agent  " +
		activeStyle.Render("[S]") + " I stopped it, switch agent  " +
		activeStyle.Render("[n]") + " Cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Width(72)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

// startupNotice is the one toast shown at startup: legacy tmux sessions that
// still need moving, then config keys that are ignored.
func startupNotice(cfg *config.Config, sessions []domain.Session) string {
	var lines []string
	legacy := 0
	for _, s := range sessions {
		if s.IsLegacyTmux() {
			legacy++
		}
	}
	if legacy > 0 {
		noun := "sessions still use"
		if legacy == 1 {
			noun = "session still uses"
		}
		stop := (&domain.LegacyTmuxError{Name: "<name>"}).StopCommand()
		lines = append(lines, fmt.Sprintf(
			`%d %s tmux. aiman no longer runs tmux. Run "%s" on the host, then press s to move it to pty.`,
			legacy, noun, stop))
	}
	if cfg != nil {
		lines = append(lines, cfg.Warnings...)
	}
	return strings.Join(lines, "\n")
}
