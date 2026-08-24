package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/pane"
)

// classifyProbeMsg carries the result of an on-demand classification comparison.
type classifyProbeMsg struct {
	session string

	local       domain.AgentState
	localReason string
	localConf   pane.Confidence

	model       domain.AgentState
	modelReason string
	modelMS     int64
	modelName   string
	modelErr    error

	err error
}

// classifyProbeTimeout bounds the model call. The deterministic answer is
// already in hand by then, so a slow model costs nothing but its own result.
const classifyProbeTimeout = 20 * time.Second

// classifySessionCmd classifies a session two ways and reports both.
//
// This is the manual escalation path for the LLM tier. Against the seventeen
// live sessions this was built on, the deterministic classifier resolved every
// one at high confidence and the model was never needed — so rather than run it
// automatically on a hot path, it is bound to a key and shown side by side.
// Where the two disagree is the evidence for whether the tier earns its place.
func classifySessionCmd(cfg *config.Config, intel domain.IntelligenceProvider, session domain.Session) tea.Cmd {
	return func() tea.Msg {
		out := classifyProbeMsg{session: session.TmuxSession}

		remote, ok := resolveRemote(cfg, session)
		if !ok {
			out.err = fmt.Errorf("no remote configured")
			return out
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		ctx, cancel := context.WithTimeout(context.Background(), classifyProbeTimeout+10*time.Second)
		defer cancel()

		paneOut, err := mgr.CaptureTmuxPane(ctx, session.TmuxSession)
		if err != nil {
			out.err = fmt.Errorf("capture pane: %w", err)
			return out
		}

		// tmux's own last-output timestamp, the cheapest signal there is.
		since := time.Duration(-1)
		if ages, aerr := mgr.SessionActivityAges(ctx); aerr == nil {
			if age, found := ages[session.TmuxSession]; found {
				since = age
			}
		}

		res := pane.Classify(pane.Observation{Pane: paneOut, SinceOutput: since})
		out.local, out.localReason, out.localConf = res.State, res.Reason, res.Confidence

		if intel == nil {
			out.modelErr = fmt.Errorf("no intelligence provider")
			return out
		}
		if namer, okName := intel.(interface{ ClassifyModel() string }); okName {
			out.modelName = namer.ClassifyModel()
		}

		mctx, mcancel := context.WithTimeout(ctx, classifyProbeTimeout)
		defer mcancel()
		start := time.Now()
		// StatusLines, not TailLines: the narrower window cuts the spinner off
		// above the agent's own chrome, and feeding the model a truncated tail is
		// how both tiers came to agree that a working session was idle.
		state, reason, cerr := intel.ClassifyActivity(mctx, pane.Tail(paneOut, pane.StatusLines))
		out.modelMS = time.Since(start).Milliseconds()
		out.model, out.modelReason, out.modelErr = state, reason, cerr
		return out
	}
}

// summary renders the comparison as a single toast line.
func (m classifyProbeMsg) summary() string {
	conf := "low"
	if m.localConf == pane.High {
		conf = "high"
	}
	local := fmt.Sprintf("rules: %s (%s) — %s", stateLabel(m.local), conf, m.localReason)

	switch {
	case m.modelErr != nil:
		return local + "  |  model: unavailable"
	case m.model == domain.AgentStateUnknown:
		return local + fmt.Sprintf("  |  model(%s): no verdict in %dms", m.modelName, m.modelMS)
	default:
		agree := "≠"
		if m.model == m.local {
			agree = "="
		}
		reason := m.modelReason
		if reason != "" {
			reason = " — " + reason
		}
		return fmt.Sprintf("%s  %s  model(%s): %s in %dms%s",
			local, agree, m.modelName, stateLabel(m.model), m.modelMS, reason)
	}
}

func stateLabel(s domain.AgentState) string {
	switch s {
	case domain.AgentStateWorking:
		return "working"
	case domain.AgentStateWaitingInput:
		return "needs input"
	case domain.AgentStateWaitingBackground:
		return "waiting on background"
	case domain.AgentStateIdle:
		return "idle"
	default:
		return "unknown"
	}
}
