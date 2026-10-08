package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/usecase"
)

// previewRefreshMsg is one preview poll. session is the tmux name the viewport
// is keyed by, matching tmuxOutputMsg. id is the aiman session id the cache uses.
type previewRefreshMsg struct {
	session    string
	id         string
	text       string
	unchanged  bool
	err        error
	cache      usecase.PaneCache
	needsInput bool
	activity   string
}

// queuePreview starts one preview poll, or reclassifies from the cache when a
// PTY event says the screen has not moved. A poll already on the wire is left
// alone so captures cannot stack on a slow link.
func (m *Model) queuePreview(s domain.Session) tea.Cmd {
	if m.previewBusy {
		return nil
	}
	if !m.paneChangedSinceCache(s) {
		m.noteCachedActivity(s)
		return nil
	}
	m.previewBusy = true
	return m.refreshPreview(s)
}

// paneChangedSinceCache reports whether a preview poll has to leave the machine.
// tmux has no event stream, so it always probes. The probe itself is a short
// header when the remote hash still matches. A PTY with the same last_output
// as the cache stays local.
func (m *Model) paneChangedSinceCache(s domain.Session) bool {
	if !s.IsPTY() {
		return true
	}
	cache := m.paneCache[s.ID]
	if cache.Text == "" {
		return true
	}
	ev, ok := m.eventSeen[s.ID]
	if !ok || ev.lastOutput.IsZero() {
		return true
	}
	return !ev.lastOutput.Equal(cache.OutputAt)
}

func (m *Model) noteCachedActivity(s domain.Session) {
	if m.cfg == nil || !m.cfg.Features.InputPromptDetection {
		return
	}
	cache := m.paneCache[s.ID]
	sinceOut := time.Duration(-1)
	sinceTitle := time.Duration(-1)
	if ev, ok := m.eventSeen[s.ID]; ok {
		if !ev.lastOutput.IsZero() {
			sinceOut = time.Since(ev.lastOutput)
		}
		if !ev.titleChanged.IsZero() {
			sinceTitle = time.Since(ev.titleChanged)
		}
	}
	activity, needs := classifyPreview(m.cfg, s, cache.Text, sinceOut, sinceTitle)
	_, _ = m.applyInputHint(inputHintMsg{session: s.TmuxSession, needsInput: needs, activity: activity}, nil)
}

func (m *Model) refreshPreview(session domain.Session) tea.Cmd {
	cache := usecase.PaneCache{}
	if m.paneCache != nil {
		cache = m.paneCache[session.ID]
	}
	cfg := m.cfg
	return func() tea.Msg {
		msg := previewRefreshMsg{session: session.TmuxSession, id: session.ID}
		remote, ok := resolveRemote(cfg, session)
		if !ok {
			msg.err = fmt.Errorf("no remote configured")
			return msg
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		upd, err := usecase.RefreshPreview(ctx, mgr, session, cache)
		if err != nil {
			msg.err = err
			return msg
		}
		msg.text = upd.Text
		msg.unchanged = upd.Unchanged
		msg.cache = upd.Cache
		msg.activity, msg.needsInput = classifyPreview(cfg, session, upd.Text, upd.SinceOutput, upd.SinceTitle)
		return msg
	}
}

func classifyPreview(cfg *config.Config, session domain.Session, text string, sinceOut, sinceTitle time.Duration) (string, bool) {
	if cfg == nil || !cfg.Features.InputPromptDetection {
		return "", false
	}
	if st, ok := agenthook.ResolveHookState(session, time.Now()); ok {
		return activityFromHook(st, session.AgentEnded)
	}
	return detectSessionActivityFrom(pane.Observation{
		Pane:             text,
		SinceOutput:      sinceOut,
		SinceTitleChange: sinceTitle,
	})
}

func (m *Model) applyPreviewRefresh(msg previewRefreshMsg) tea.Cmd {
	m.previewBusy = false
	if m.paneCache == nil {
		m.paneCache = map[string]usecase.PaneCache{}
	}
	if msg.err != nil {
		cached := m.paneCache[msg.id]
		cached.Hash = ""
		m.paneCache[msg.id] = cached
		if msg.session != m.activeSession {
			return nil
		}
		_, cmd := m.applyTmuxOutput(tmuxOutputMsg{session: msg.session, err: msg.err}, nil)
		return cmd
	}
	m.storePaneCache(msg)
	var cmds []tea.Cmd
	if msg.session == m.activeSession && !msg.unchanged {
		_, cmd := m.applyTmuxOutput(tmuxOutputMsg{session: msg.session, output: msg.text}, nil)
		cmds = append(cmds, cmd)
	}
	_, cmd := m.applyInputHint(inputHintMsg{session: msg.session, needsInput: msg.needsInput, activity: msg.activity}, nil)
	cmds = append(cmds, cmd)
	return tea.Batch(cmds...)
}

func (m *Model) storePaneCache(msg previewRefreshMsg) {
	if !msg.unchanged {
		m.paneCache[msg.id] = msg.cache
		return
	}
	prev := m.paneCache[msg.id]
	if msg.cache.Hash != "" {
		prev.Hash = msg.cache.Hash
	}
	prev.HistorySize = msg.cache.HistorySize
	if !msg.cache.OutputAt.IsZero() {
		prev.OutputAt = msg.cache.OutputAt
	}
	if !msg.cache.TitleAt.IsZero() {
		prev.TitleAt = msg.cache.TitleAt
	}
	m.paneCache[msg.id] = prev
}
