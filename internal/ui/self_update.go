package ui

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// Staged is a verified release binary waiting beside the installed one.
type Staged interface {
	// Target is the installed binary Apply replaces.
	Target() string
	// Apply renames the staged binary over the target. It is not cancellable.
	Apply() error
	// Discard removes the staged file.
	Discard()
}

// SelfUpdater downloads and verifies one release for the TUI. main provides it.
type SelfUpdater interface {
	// Stage fetches tag (the latest release when empty), reporting each step.
	// It must write nothing to stdout or stderr.
	Stage(ctx context.Context, tag string, progress func(step string)) (Staged, error)
}

// Restarter is implemented by the final model of a TUI run that wants main to
// exec the updated binary once the terminal is restored.
type Restarter interface{ RestartRequested() bool }

var selfUpdater SelfUpdater

// SetSelfUpdater installs the updater; nil clears it.
func SetSelfUpdater(u SelfUpdater) { selfUpdater = u }

// RestartRequested reports a finished self-update.
func (m *Model) RestartRequested() bool { return m.restartAfterQuit }

// RestartRequested is false: the splash never carries an update.
func (m StartupModel) RestartRequested() bool { return false }

type selfUpdateState int

const (
	suConfirm selfUpdateState = iota
	suBlocked
	suRunning
	suFailed
	suInfo
)

type selfUpdatePage struct {
	target string // release tag; empty means the latest release
	state  selfUpdateState
	steps  []string
	err    string
	busy   []string
	acked  map[string]bool
	staged Staged
	cancel context.CancelFunc
	// path is the binary the update replaces, once staged.
	path string
}

// selfUpdateStageMsg is the result of Stage.
type selfUpdateStageMsg struct {
	page   *selfUpdatePage
	steps  []string
	staged Staged
	err    error
}

// selfUpdateStepMsg reports one live progress step.
type selfUpdateStepMsg struct {
	page *selfUpdatePage
	step string
}

// selfUpdateBusy lists in-process work a restart would cut, one line each.
func (m *Model) selfUpdateBusy() []string {
	var out []string
	for _, cs := range m.creatingSessions {
		verb := "session create"
		if cs.restart {
			verb = "session restart"
		}
		out = append(out, fmt.Sprintf("%s %q", verb, cs.placeholder.Name))
	}
	if m.worktreeExistsID != "" {
		out = append(out, "a session create waiting on an existing worktree")
	}
	if m.state == viewStateProvisioningProgress {
		out = append(out, "provisioning a remote")
	}
	if m.restartingSession != nil {
		out = append(out, fmt.Sprintf("restart of %q", m.restartingSession.Name))
	}
	for _, ts := range m.terminatingSessions {
		out = append(out, fmt.Sprintf("terminating %q", ts.session.Name))
	}
	if m.state == viewStateArchiveProgress {
		out = append(out, "archiving a session")
	}
	if m.changingDirSession != nil {
		out = append(out, fmt.Sprintf("directory change of %q", m.changingDirSession.Name))
	}
	if m.awsCredRefreshing || m.awsCredentials.Busy() {
		out = append(out, "AWS credential renewal")
	}
	if m.authChecking {
		out = append(out, "AWS auth check")
	}
	for key, at := range m.serveUpdateAt {
		if time.Since(at) < remotesvc.OpTimeout {
			host, _, _ := strings.Cut(key, "\x00")
			out = append(out, "agent API update on "+host)
		}
	}
	if len(m.promptDraft) > 0 {
		out = append(out, "a reply being sent")
	}
	if m.memoryCandidates.busy {
		out = append(out, "memory candidate proposals")
	}
	if m.aiLoading {
		out = append(out, "an AI summary")
	}
	return out
}

// enterSelfUpdate opens the update page. tag is the hint's release, or empty
// for the latest one.
func (m *Model) enterSelfUpdate(tag string) {
	p := &selfUpdatePage{target: tag, acked: map[string]bool{}}
	m.selfUpdate = p
	m.state = viewStateSelfUpdate
	switch {
	case runtime.GOOS == "windows":
		p.state, p.err = suInfo, "quit, then run aiman update"
	case selfUpdater == nil:
		p.state, p.err = suInfo, "self-update is not available in this build"
	case tag == "" && m.githubLatest != "":
		if v, ok := remotesvc.ParseVersion(m.version); ok {
			if l, lok := remotesvc.ParseVersion(m.githubLatest); lok && !v.Less(l) {
				p.state, p.err = suInfo, "aiman "+m.version+" is up to date"
			}
		}
		p.target = m.githubLatest
	}
	if p.state == suConfirm && p.target != "" {
		if v, ok := remotesvc.ParseVersion(m.version); ok {
			if t, tok := remotesvc.ParseVersion(p.target); tok && !v.Less(t) {
				p.state, p.err = suInfo, "aiman "+m.version+" is up to date"
			}
		}
	}
}

func (m *Model) selfUpdateStage(p *selfUpdatePage) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.state = suRunning
	p.err = ""
	p.steps = nil
	u, tag, prog := selfUpdater, p.target, m.Program
	return func() tea.Msg {
		var steps []string
		staged, err := u.Stage(ctx, tag, func(s string) {
			steps = append(steps, s)
			if prog != nil {
				prog.Send(selfUpdateStepMsg{page: p, step: s})
			}
		})
		return selfUpdateStageMsg{page: p, steps: steps, staged: staged, err: err}
	}
}

func (p *selfUpdatePage) discard() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.staged != nil {
		p.staged.Discard()
		p.staged = nil
	}
}

// newBusy returns the busy lines the operator has not yet accepted.
func (p *selfUpdatePage) newBusy(busy []string) []string {
	var fresh []string
	for _, b := range busy {
		if !p.acked[b] {
			fresh = append(fresh, b)
		}
	}
	return fresh
}

func (m *Model) handleSelfUpdateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.selfUpdate
	if p == nil {
		m.state = viewStateMain
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		p.discard()
		m.selfUpdate = nil
		m.state = viewStateMain
	case "enter":
		switch p.state {
		case suConfirm:
			if fresh := p.newBusy(m.selfUpdateBusy()); len(fresh) > 0 {
				p.busy, p.state = fresh, suBlocked
				for _, b := range fresh {
					p.acked[b] = true
				}
				return m, nil
			}
			return m, m.selfUpdateStage(p)
		case suBlocked:
			if p.staged != nil {
				return m.selfUpdateApply(p)
			}
			return m, m.selfUpdateStage(p)
		case suFailed:
			return m, m.selfUpdateStage(p)
		}
	}
	return m, nil
}

func (m *Model) applySelfUpdateStep(msg selfUpdateStepMsg) {
	if msg.page == m.selfUpdate && msg.page.state == suRunning {
		msg.page.steps = append(msg.page.steps, msg.step)
	}
}

func (m *Model) applySelfUpdateStage(msg selfUpdateStageMsg) (tea.Model, tea.Cmd) {
	p := msg.page
	if p != m.selfUpdate {
		// Left the page while staging: nothing is installed.
		if msg.staged != nil {
			msg.staged.Discard()
		}
		return m, nil
	}
	if msg.err != nil {
		p.state, p.err, p.steps = suFailed, msg.err.Error(), msg.steps
		return m, nil
	}
	p.steps = msg.steps
	p.staged = msg.staged
	p.path = msg.staged.Target()
	// A download can take minutes: look again before replacing anything.
	if fresh := p.newBusy(m.selfUpdateBusy()); len(fresh) > 0 {
		p.busy, p.state = fresh, suBlocked
		for _, b := range fresh {
			p.acked[b] = true
		}
		return m, nil
	}
	return m.selfUpdateApply(p)
}

func (m *Model) selfUpdateApply(p *selfUpdatePage) (tea.Model, tea.Cmd) {
	staged := p.staged
	p.staged = nil
	if err := staged.Apply(); err != nil {
		staged.Discard()
		p.state, p.err = suFailed, err.Error()
		return m, nil
	}
	for host, st := range m.streams {
		st.cancel()
		delete(m.streams, host)
	}
	m.restartAfterQuit = true
	return m, tea.Quit
}

// updatedFromEnv is set by the old binary when it execs the new one.
const updatedFromEnv = "AIMAN_UPDATED_FROM"

// takeUpdatedToast reads and clears AIMAN_UPDATED_FROM, returning the toast.
func (m *Model) takeUpdatedToast() tea.Cmd {
	from := os.Getenv(updatedFromEnv)
	if from == "" {
		return nil
	}
	_ = os.Unsetenv(updatedFromEnv)
	return m.showToast("updated "+from+" → "+m.version, false, 8*time.Second)
}

func (m *Model) renderSelfUpdateView() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	bad := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	p := m.selfUpdate
	var b strings.Builder
	if p == nil {
		return docStyle.Render("\n  No update in progress.\n")
	}
	target := p.target
	if target == "" {
		target = "the latest release"
	}
	b.WriteString("\n  " + title.Render("Update aiman "+m.version+" → "+target) + "\n\n")
	if p.path != "" {
		b.WriteString("  " + dim.Render(p.path) + "\n\n")
	}
	for i, s := range p.steps {
		mark := "✓ "
		if i == len(p.steps)-1 && p.state == suRunning {
			mark = "▸ "
		}
		b.WriteString("  " + mark + s + "\n")
	}
	if len(p.busy) > 0 && p.state == suBlocked {
		b.WriteString("  " + warn.Render("These will be cut by the restart:") + "\n")
		for _, l := range p.busy {
			b.WriteString("    - " + l + "\n")
		}
		b.WriteString("\n")
	}
	if p.err != "" {
		b.WriteString("  " + bad.Render(p.err) + "\n\n")
	}
	switch p.state {
	case suConfirm:
		b.WriteString("  " + dim.Render("enter to install, esc to cancel") + "\n")
	case suBlocked:
		b.WriteString("  " + dim.Render("enter to go ahead anyway, esc to cancel") + "\n")
	case suRunning:
		b.WriteString("  " + dim.Render("esc to cancel") + "\n")
	case suFailed:
		b.WriteString("  " + dim.Render("enter to retry, esc to go back") + "\n")
	default:
		b.WriteString("  " + dim.Render("esc to go back") + "\n")
	}
	return docStyle.Render(b.String())
}
