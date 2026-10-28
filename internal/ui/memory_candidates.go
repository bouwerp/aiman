package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/usecase"
)

type memoryJobsMsg struct {
	host string
	jobs []memoryjob.Job
	err  error
}

type memoryProposalMsg struct {
	host       string
	job        memoryjob.Job
	candidates []domain.MemoryCandidate
	err        error
}

type memorySavedMsg struct {
	host string
	job  string
	err  error
}

type memoryJobDoneMsg struct {
	host string
	job  string
	err  error
}

type memoryProposal struct {
	host       string
	job        memoryjob.Job
	candidates []domain.MemoryCandidate
	decided    []bool
}

type memoryEditForm struct {
	title    textinput.Model
	abstract textinput.Model
	body     textarea.Model
	typ      domain.ContextType
}

// MemoryCandidatesModel reviews memory notes proposed from handovers.
type MemoryCandidatesModel struct {
	cfg       *config.Config
	intel     domain.IntelligenceProvider
	proposals []memoryProposal
	cursor    int
	editing   *memoryEditForm
	busy      bool
	proposed  map[string]bool
	lastError string
}

func NewMemoryCandidatesModel(cfg *config.Config, intel domain.IntelligenceProvider) MemoryCandidatesModel {
	return MemoryCandidatesModel{cfg: cfg, intel: intel, proposed: map[string]bool{}}
}

// Pending counts candidates that still need an accept or drop.
func (m MemoryCandidatesModel) Pending() int {
	n := 0
	for _, p := range m.proposals {
		for _, done := range p.decided {
			if !done {
				n++
			}
		}
	}
	return n
}

func (m MemoryCandidatesModel) Init() tea.Cmd { return nil }

func (m MemoryCandidatesModel) Update(msg tea.Msg) (MemoryCandidatesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case memoryJobsMsg:
		if msg.err != nil {
			m.busy = false
			m.lastError = msg.err.Error()
			return m, nil
		}
		var cmds []tea.Cmd
		proposing := false
		for _, j := range msg.jobs {
			if m.proposed[j.ID] {
				continue
			}
			m.proposed[j.ID] = true
			switch {
			case len(j.Candidates) > 0:
				m.addProposal(msg.host, j, j.Candidates)
			case m.aiEnabled():
				proposing = true
				cmds = append(cmds, proposeMemoriesCmd(m.intel, msg.host, j))
			default:
				// Nothing can propose for this job; clear it from the queue.
				cmds = append(cmds, markMemoryJobDoneCmd(m.cfg, msg.host, j.ID))
			}
		}
		if proposing {
			m.busy = true
		}
		return m, tea.Batch(cmds...)
	case memoryProposalMsg:
		m.busy = false
		if msg.err != nil {
			m.lastError = msg.err.Error()
			return m, nil
		}
		if len(msg.candidates) == 0 {
			if cmd := markMemoryJobDoneCmd(m.cfg, msg.host, msg.job.ID); cmd != nil {
				return m, cmd
			}
			return m, nil
		}
		m.addProposal(msg.host, msg.job, msg.candidates)
		return m, nil
	case memorySavedMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
		return m, nil
	case memoryJobDoneMsg:
		if msg.err != nil {
			m.lastError = msg.err.Error()
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.editing != nil {
			return m.updateEdit(msg)
		}
		return m.updateList(msg)
	default:
		if m.editing != nil {
			return m.updateEdit(msg)
		}
		return m, nil
	}
}

func (m *MemoryCandidatesModel) addProposal(host string, j memoryjob.Job, candidates []domain.MemoryCandidate) {
	m.proposals = append(m.proposals, memoryProposal{
		host: host, job: j, candidates: candidates,
		decided: make([]bool, len(candidates)),
	})
}

// aiEnabled reports whether the laptop model may propose for jobs that
// arrive without candidates.
func (m MemoryCandidatesModel) aiEnabled() bool {
	return m.cfg != nil && m.cfg.AI.Enabled
}

func (m MemoryCandidatesModel) updateList(msg tea.KeyPressMsg) (MemoryCandidatesModel, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor+1 < len(m.refs()) {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "a":
		return m.decide(true)
	case "d":
		return m.decide(false)
	case "e":
		m.startEdit()
	}
	return m, nil
}

func (m MemoryCandidatesModel) decide(save bool) (MemoryCandidatesModel, tea.Cmd) {
	pi, ci, ok := m.current()
	if !ok {
		return m, nil
	}
	if save {
		candidate := m.proposals[pi].candidates[ci]
		m.proposals[pi].decided[ci] = true
		return m, tea.Batch(
			saveMemoryCmd(m.cfg, m.proposals[pi].host, m.proposals[pi].job, candidate),
			m.doneCmdIfFinished(pi),
		)
	}
	m.proposals[pi].decided[ci] = true
	return m, m.doneCmdIfFinished(pi)
}

func (m *MemoryCandidatesModel) doneCmdIfFinished(pi int) tea.Cmd {
	if pi < 0 || pi >= len(m.proposals) {
		return nil
	}
	for _, done := range m.proposals[pi].decided {
		if !done {
			return nil
		}
	}
	return markMemoryJobDoneCmd(m.cfg, m.proposals[pi].host, m.proposals[pi].job.ID)
}

func (m *MemoryCandidatesModel) startEdit() {
	pi, ci, ok := m.current()
	if !ok {
		return
	}
	c := m.proposals[pi].candidates[ci]
	form := &memoryEditForm{typ: c.Type}
	form.title = textinput.New()
	form.title.SetValue(c.Title)
	form.title.Focus()
	form.abstract = textinput.New()
	form.abstract.SetValue(c.Abstract)
	form.body = textarea.New()
	form.body.SetValue(c.Body)
	m.editing = form
}

func (m MemoryCandidatesModel) updateEdit(msg tea.Msg) (MemoryCandidatesModel, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		if m.editing == nil {
			return m, nil
		}
		m.editing.title, cmd = m.editing.title.Update(msg)
		return m, cmd
	}
	switch km.String() {
	case "esc":
		m.editing = nil
		return m, nil
	case "tab":
		m.editing.typ = nextMemoryType(m.editing.typ)
		return m, nil
	case "ctrl+s":
		pi, ci, found := m.current()
		if !found {
			m.editing = nil
			return m, nil
		}
		c := domain.MemoryCandidate{
			Type: m.editing.typ, Title: strings.TrimSpace(m.editing.title.Value()),
			Abstract: strings.TrimSpace(m.editing.abstract.Value()), Body: m.editing.body.Value(),
		}
		if c.Title == "" {
			return m, nil
		}
		m.proposals[pi].candidates[ci] = c
		m.proposals[pi].decided[ci] = true
		m.editing = nil
		return m, tea.Batch(
			saveMemoryCmd(m.cfg, m.proposals[pi].host, m.proposals[pi].job, c),
			m.doneCmdIfFinished(pi),
		)
	}
	var cmd tea.Cmd
	m.editing.title, cmd = m.editing.title.Update(msg)
	m.editing.abstract, _ = m.editing.abstract.Update(msg)
	m.editing.body, _ = m.editing.body.Update(msg)
	return m, cmd
}

func (m MemoryCandidatesModel) refs() []struct{ p, c int } {
	var out []struct{ p, c int }
	for pi, p := range m.proposals {
		for ci, done := range p.decided {
			if !done {
				out = append(out, struct{ p, c int }{pi, ci})
			}
		}
	}
	return out
}

func (m MemoryCandidatesModel) current() (int, int, bool) {
	refs := m.refs()
	if len(refs) == 0 {
		return 0, 0, false
	}
	if m.cursor >= len(refs) {
		m.cursor = len(refs) - 1
	}
	return refs[m.cursor].p, refs[m.cursor].c, true
}

func nextMemoryType(t domain.ContextType) domain.ContextType {
	switch t {
	case domain.ContextTypeDecision:
		return domain.ContextTypePitfall
	case domain.ContextTypePitfall:
		return domain.ContextTypeConvention
	case domain.ContextTypeConvention:
		return domain.ContextTypeFact
	default:
		return domain.ContextTypeDecision
	}
}

func (m MemoryCandidatesModel) viewString() string {
	if len(m.proposals) == 0 {
		msg := "No memory proposals."
		if m.busy {
			msg = "Looking for memory proposals…"
		}
		if m.lastError != "" {
			msg += "\n" + failStyle.Render(m.lastError)
		}
		return titleStyle.Render("Memory candidates") + "\n\n" + msg + "\n\n" + "esc back"
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Memory candidates"))
	b.WriteString("\n\n")
	for pi, p := range m.proposals {
		fmt.Fprintf(&b, "%s · %s\n", p.job.Label, p.job.Reason)
		for ci, c := range p.candidates {
			mark := "○"
			if p.decided[ci] {
				mark = "✓"
			}
			fmt.Fprintf(&b, "  %s [%s] %s\n", mark, c.Type, c.Title)
		}
		if pi != len(m.proposals)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	if m.editing != nil {
		fmt.Fprintf(&b, "Edit type: %s\n%s\n%s\n%s\n", m.editing.typ, m.editing.title.View(), m.editing.abstract.View(), m.editing.body.View())
		b.WriteString("tab type · ctrl+s save · esc cancel")
	} else {
		b.WriteString("j/k move · a accept · e edit · d drop · esc back")
	}
	return b.String()
}

func fetchMemoryJobsCmd(cfg *config.Config, host string) tea.Cmd {
	return func() tea.Msg {
		remote, ok := remoteForHost(cfg, host)
		if !ok {
			return memoryJobsMsg{host: host, err: fmt.Errorf("no remote for %s", host)}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		raw, err := usecase.ServeRPC(ctx, mgr, "memory.jobs", struct{}{})
		if err != nil {
			return memoryJobsMsg{host: host, err: err}
		}
		var body struct {
			Jobs []memoryjob.Job `json:"jobs"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return memoryJobsMsg{host: host, err: err}
		}
		return memoryJobsMsg{host: host, jobs: body.Jobs}
	}
}

func proposeMemoriesCmd(intel domain.IntelligenceProvider, host string, j memoryjob.Job) tea.Cmd {
	return func() tea.Msg {
		if intel == nil {
			return memoryProposalMsg{host: host, job: j, err: domain.ErrIntelligenceUnavailable}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		candidates, err := intel.ProposeMemories(ctx, j.Source)
		return memoryProposalMsg{host: host, job: j, candidates: candidates, err: err}
	}
}

type memoryPutParams struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Abstract string `json:"abstract"`
	Body     string `json:"body"`
	Group    string `json:"group,omitempty"`
	Repo     string `json:"repo,omitempty"`
}

func memoryPutParamsFor(j memoryjob.Job, c domain.MemoryCandidate) memoryPutParams {
	params := memoryPutParams{Type: string(c.Type), Title: c.Title, Abstract: c.Abstract, Body: c.Body}
	if j.Group != "" {
		params.Group = j.Group
	} else {
		params.Repo = j.Repo
	}
	return params
}

func saveMemoryCmd(cfg *config.Config, host string, j memoryjob.Job, c domain.MemoryCandidate) tea.Cmd {
	return func() tea.Msg {
		remote, ok := remoteForHost(cfg, host)
		if !ok {
			return memorySavedMsg{host: host, job: j.ID, err: fmt.Errorf("no remote for %s", host)}
		}
		params := memoryPutParamsFor(j, c)
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := usecase.ServeRPC(ctx, mgr, "context.put", params)
		return memorySavedMsg{host: host, job: j.ID, err: err}
	}
}

func markMemoryJobDoneCmd(cfg *config.Config, host, jobID string) tea.Cmd {
	return func() tea.Msg {
		remote, ok := remoteForHost(cfg, host)
		if !ok {
			return memoryJobDoneMsg{host: host, job: jobID, err: fmt.Errorf("no remote for %s", host)}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := usecase.ServeRPC(ctx, mgr, "memory.done", struct {
			ID string `json:"id"`
		}{ID: jobID})
		return memoryJobDoneMsg{host: host, job: jobID, err: err}
	}
}

func memoryMenuDesc(n int) string {
	if n == 0 {
		return "No proposals"
	}
	return fmt.Sprintf("%d proposed notes to review", n)
}

func (m *Model) handleMemoryCandidatesUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "esc" && m.memoryCandidates.editing == nil {
		m.state = viewStateMenu
		return m, nil
	}
	model, cmd := m.memoryCandidates.Update(msg)
	m.memoryCandidates = model
	m.syncMemoryMenuDesc()
	return m, cmd
}

func (m *Model) syncMemoryMenuDesc() {
	items := m.menu.Items()
	for i, it := range items {
		mi, ok := it.(menuItem)
		if !ok || mi.action != viewStateMemoryCandidates {
			continue
		}
		mi.desc = memoryMenuDesc(m.memoryCandidates.Pending())
		items[i] = mi
	}
	m.menu.SetItems(items)
}
