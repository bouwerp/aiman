package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
)

// reviveWorktreeScanTimeout bounds one on-demand scan: connect, walk the
// whole repo root for worktrees, then one batched git-log call across
// whatever was found. Generous because it only runs when the user explicitly
// opens the revive screen, never in the background.
const reviveWorktreeScanTimeout = 2 * time.Minute

// reviveItem is one abandoned worktree found under a remote's repo root,
// with the agent(s) that plausibly worked in it (see
// usecase.ResolveWorktreeAgentCandidates). Implements list.Item.
type reviveItem struct {
	session    domain.Session
	candidates []string
}

func (i reviveItem) Title() string {
	label := i.session.RepoName
	switch {
	case i.session.Branch != "":
		label += " @ " + i.session.Branch
	case i.session.TmuxSession != "":
		label += " @ " + i.session.TmuxSession
	}
	return label
}

func (i reviveItem) Description() string {
	agentPart := "agent unknown"
	switch len(i.candidates) {
	case 0:
	case 1:
		agentPart = i.candidates[0]
	default:
		agentPart = strings.Join(i.candidates, " or ")
	}
	return fmt.Sprintf("%s | %s", i.session.WorktreePath, agentPart)
}

func (i reviveItem) FilterValue() string {
	return i.session.RepoName + " " + i.session.Branch + " " + i.session.WorktreePath
}

// ReviveWorktreeModel lists the abandoned worktrees found by one on-demand
// scan of a remote's repo root.
type ReviveWorktreeModel struct {
	list   list.Model
	remote config.Remote
}

// NewReviveWorktreeModel builds the list screen from a completed scan.
func NewReviveWorktreeModel(width, height int, remote config.Remote, entries []reviveItem) ReviveWorktreeModel {
	items := make([]list.Item, len(entries))
	for i, e := range entries {
		items[i] = e
	}
	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = fmt.Sprintf("Abandoned worktrees on %s", remote.Host)
	return ReviveWorktreeModel{list: l, remote: remote}
}

// reviveScanResultMsg is the outcome of one on-demand abandoned-worktree scan.
type reviveScanResultMsg struct {
	remote  config.Remote
	entries []reviveItem
	err     error
}

// startAbandonedWorktreeScan transitions to the loading screen and kicks off
// the scan for remote. Shared by the menu's single-remote shortcut and the
// remote-picker's selection.
func (m *Model) startAbandonedWorktreeScan(remote config.Remote) (tea.Model, tea.Cmd) {
	m.loadingMsg = fmt.Sprintf("Scanning %s for abandoned worktrees...", remote.Host)
	m.loadingNext = viewStateReviveList
	m.state = viewStateLoading
	return m, m.scanAbandonedWorktreesCmd(remote)
}

// scanAbandonedWorktreesCmd walks remote's whole repo root for every
// worktree not already visible in the dashboard (m.allSessions), then
// resolves candidate agent(s) for each from a single batched git-log call.
func (m *Model) scanAbandonedWorktreesCmd(remote config.Remote) tea.Cmd {
	knownIDs := make(map[string]bool, len(m.allSessions))
	for _, s := range m.allSessions {
		knownIDs[s.ID] = true
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reviveWorktreeScanTimeout)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return reviveScanResultMsg{remote: remote, err: fmt.Errorf("connecting to %s: %w", remote.Host, err)}
		}

		sessions, err := usecase.NewSessionDiscoverer(mgr, mutagen.NewEngine()).OrphanWorktreeSessions(ctx, remote.Host)
		if err != nil {
			return reviveScanResultMsg{remote: remote, err: err}
		}

		found := unseenWorktreeSessions(sessions, knownIDs)
		paths := make([]string, len(found))
		for i, s := range found {
			paths[i] = s.WorktreePath
		}

		hints, err := mgr.WorktreeCoAuthorHints(ctx, paths)
		if err != nil {
			// A failed hint scan is not fatal to the whole screen: every
			// worktree just falls back to "agent unknown" (manual picker).
			hints = map[string][]string{}
		}

		entries := make([]reviveItem, 0, len(found))
		for _, s := range found {
			entries = append(entries, reviveItem{
				session:    s,
				candidates: usecase.ResolveWorktreeAgentCandidates(s, hints[s.WorktreePath]),
			})
		}
		return reviveScanResultMsg{remote: remote, entries: entries}
	}
}

// unseenWorktreeSessions drops any session already visible somewhere in the
// dashboard today (knownIDs, built from m.allSessions) — the revive screen
// exists specifically for the worktrees that are otherwise invisible.
func unseenWorktreeSessions(sessions []domain.Session, knownIDs map[string]bool) []domain.Session {
	out := make([]domain.Session, 0, len(sessions))
	for _, s := range sessions {
		if knownIDs[s.ID] {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (m *Model) applyReviveScanResult(msg reviveScanResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.lastError = fmt.Sprintf("Failed to scan %s for abandoned worktrees: %v", msg.remote.Host, msg.err)
		m.state = viewStateError
		return m, nil
	}
	h, v := docStyle.GetFrameSize()
	m.revive = NewReviveWorktreeModel(m.width-h, m.height-v, msg.remote, msg.entries)
	m.state = viewStateReviveList
	return m, nil
}

func (m *Model) handleReviveRemotePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateMenu
			return m, nil
		case "enter", " ":
			if i, ok := m.remotes.list.SelectedItem().(remoteItem); ok && i.isConfig {
				return m.startAbandonedWorktreeScan(config.Remote{Name: i.name, Host: i.host, User: i.user, Root: i.root})
			}
		}
	}
	var cmd tea.Cmd
	m.remotes.list, cmd = m.remotes.list.Update(msg)
	return m, cmd
}

func (m *Model) handleReviveListUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateMenu
			return m, nil
		case "enter":
			if i, ok := m.revive.list.SelectedItem().(reviveItem); ok {
				return m.chooseReviveTarget(i)
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.revive.list, cmd = m.revive.list.Update(msg)
	return m, cmd
}

// chooseReviveTarget decides how much friction reviving i needs: none when
// exactly one agent candidate was found (matching how a known-agent session
// already auto-resumes via "s"), a short pick-list when there's more than
// one, and the full agent catalog when there's no signal to go on at all.
func (m *Model) chooseReviveTarget(i reviveItem) (tea.Model, tea.Cmd) {
	m.prepareRestartTarget(i.session)
	switch len(i.candidates) {
	case 0:
		return m.startAgentPickerRestart()
	case 1:
		return m.reviveWithAgentName(i.candidates[0])
	default:
		m.revivePickCandidates = i.candidates
		m.state = viewStateReviveAgentPick
		return m, nil
	}
}

// reviveWithAgentName resolves name to a runnable agent.KnownAgents entry
// and jumps straight into the existing background-restart machinery — no
// new session-launch logic needed, it already starts a fresh tmux session
// in an existing worktree with no live pane, and the dashboard stays usable
// while it runs. Falls back to the full picker if the candidate name
// somehow doesn't resolve (a hint matched a vendor this aiman build doesn't
// know how to launch).
func (m *Model) reviveWithAgentName(name string) (tea.Model, tea.Cmd) {
	resolved, ok := agent.FindKnown(name)
	if !ok {
		return m.startAgentPickerRestart()
	}
	m.sessionCfg.Agent = &resolved
	return m, m.startBackgroundRestart()
}

func (m *Model) handleReviveAgentPickUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc":
		m.state = viewStateReviveList
		return m, nil
	case "o":
		return m.startAgentPickerRestart()
	default:
		if n, err := strconv.Atoi(km.String()); err == nil && n >= 1 && n <= len(m.revivePickCandidates) {
			return m.reviveWithAgentName(m.revivePickCandidates[n-1])
		}
	}
	return m, nil
}

func (m *Model) renderReviveAgentPick() string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Multiple agents may have worked here") + "\n\n")
	if m.restartingSession != nil {
		b.WriteString(fmt.Sprintf("Worktree: %s\n\n", m.restartingSession.WorktreePath))
	}
	for idx, name := range m.revivePickCandidates {
		b.WriteString(fmt.Sprintf("%s %s\n", activeStyle.Render(fmt.Sprintf("[%d]", idx+1)), name))
	}
	b.WriteString(activeStyle.Render("[o]") + " other agent…\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("esc: back"))

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Width(60)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}
