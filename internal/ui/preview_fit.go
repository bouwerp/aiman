package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Sizing a session to the panel it is shown in. Terminal text cannot be
// scaled. A full-screen agent is captured as it is and the panel pans: asking
// it to match the panel clears the screen. Muse and Codex lay out from the
// PTY size, so they are told the panel size once. Deep Code in the terminal
// panel is sized the same way, because it stays at the 80x24 it was created
// with until something resizes it.
const (
	// previewFitDebounce is how long the desired size has to settle. Dragging a
	// window edge changes it many times a second and every change makes the
	// remote agent repaint, so only the last one is acted on.
	previewFitDebounce = 700 * time.Millisecond

	// previewFitBackoff is how long to wait before retrying a fit that could not
	// be applied — most often because someone is attached, which is deliberately
	// left alone. Without it the poll ticker would retry several times a second.
	previewFitBackoff = 30 * time.Second
)

// previewFitTickMsg fires when the desired size has been stable long enough.
// gen guards against acting on a superseded timer.
type previewFitTickMsg struct{ gen int }

// previewFitDoneMsg reports the outcome of one fit attempt.
type previewFitDoneMsg struct {
	sessionID string
	size      string
	applied   bool
	err       error
}

// previewFit is a pending size for one session.
type previewFit struct {
	session    domain.Session
	cols, rows int
}

func (f previewFit) size() string { return fmt.Sprintf("%dx%d", f.cols, f.rows) }

// sessionWantsPanelSize reports whether this panel should tell the agent its
// size. Muse and Codex need it wherever they are shown. Deep Code needs it
// in the terminal panel.
func sessionWantsPanelSize(mode panelMode, agentName string) bool {
	switch mode {
	case panelModePreview:
		return ptyruntime.SizesWithClient(agentName) && !ptyruntime.FitsClient(agentName)
	case panelModeTerminal:
		return ptyruntime.SizesWithClient(agentName)
	default:
		return false
	}
}

// desiredPreviewFit is the size a session should be told to render at, or ok
// false when the panel must not resize it.
//
// Muse and Codex are sized in either panel. They are created at 80x24 and
// keep drawing there until a resize arrives, so the layout never settles on
// the terminal the user is looking at. The fit is remembered, so a later
// poll does not resize again. A full-screen agent is left alone: a resize
// clears its screen. Deep Code is sized only from the terminal panel, where
// someone is looking at it full size.
func (m *Model) desiredPreviewFit(s domain.Session) (previewFit, bool) {
	if !s.IsPTY() || !sessionWantsPanelSize(m.panelMode, s.AgentName) {
		return previewFit{}, false
	}
	cols, rows, ok := usecase.ClampTerminalSize(m.viewport.Width(), m.viewport.Height())
	if !ok {
		return previewFit{}, false
	}
	return previewFit{session: s, cols: cols, rows: rows}, true
}

// schedulePreviewFit arms the debounce when the previewed session is not already
// rendering at the size the panel wants.
//
// The timer is armed at most once at a time. The poll ticker runs faster than
// the debounce, so re-arming on every tick would cancel the pending timer before
// it ever fired; instead the pending target is updated in place and the armed
// timer picks up whatever the latest value is when it lands.
func (m *Model) schedulePreviewFit(s domain.Session) tea.Cmd {
	want, ok := m.desiredPreviewFit(s)
	if !ok {
		return nil
	}
	if m.fitApplied[s.ID] == want.size() {
		return nil
	}
	if until, backing := m.fitBackoff[s.ID]; backing && time.Now().Before(until) {
		return nil
	}

	m.fitPending = &want
	if m.fitArmed {
		return nil // an armed timer will pick up the updated target
	}
	m.fitArmed = true
	m.fitGen++
	gen := m.fitGen
	return tea.Tick(previewFitDebounce, func(time.Time) tea.Msg {
		return previewFitTickMsg{gen: gen}
	})
}

// applyPreviewFitTick issues the resize once the desired size has settled.
func (m *Model) applyPreviewFitTick(msg previewFitTickMsg) tea.Cmd {
	if msg.gen != m.fitGen {
		return nil // superseded
	}
	m.fitArmed = false
	want := m.fitPending
	m.fitPending = nil
	if want == nil || m.fitApplied[want.session.ID] == want.size() {
		return nil
	}
	return fitSessionCmd(m.cfg, *want)
}

// applyPreviewFitDone records the outcome so the fit is not attempted again for
// a size that is already in place, and is not retried in a tight loop for one
// that could not be applied.
func (m *Model) applyPreviewFitDone(msg previewFitDoneMsg) {
	if m.fitApplied == nil {
		m.fitApplied = map[string]string{}
	}
	if m.fitBackoff == nil {
		m.fitBackoff = map[string]time.Time{}
	}
	switch {
	case msg.err != nil:
		m.fitBackoff[msg.sessionID] = time.Now().Add(previewFitBackoff)
		m.log("fit preview %s to %s failed: %v", msg.sessionID, msg.size, msg.err)
	case !msg.applied:
		// Left alone on purpose (a client is attached). Try again later rather
		// than every tick.
		m.fitBackoff[msg.sessionID] = time.Now().Add(previewFitBackoff)
	default:
		m.fitApplied[msg.sessionID] = msg.size
		delete(m.fitBackoff, msg.sessionID)
	}
}

// fitSessionCmd resizes the session's terminal off the UI loop.
func fitSessionCmd(cfg *config.Config, want previewFit) tea.Cmd {
	return func() tea.Msg {
		done := previewFitDoneMsg{sessionID: want.session.ID, size: want.size()}
		remote, ok := resolveRemote(cfg, want.session)
		if !ok {
			done.err = fmt.Errorf("no remote configured")
			return done
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		// A tmux session with someone attached is left alone; the usecase makes
		// that call and reports it as "not applied" rather than an error.
		applied, err := usecase.ResizeSessionTerminal(ctx, mgr, want.session, want.cols, want.rows)
		done.applied, done.err = applied, err
		return done
	}
}
