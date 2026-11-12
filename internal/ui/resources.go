package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// resourcesPollEvery matches the sampler's interval on the host.
const resourcesPollEvery = 10 * time.Second

// resourcesState is what the sessions tab knows about each host's session
// scopes. A host that cannot answer resources.get (an old serve, no user
// systemd, an error) has no entry, and nothing is drawn for it.
type resourcesState struct {
	byHost   map[string]server.ResourcesReport
	inflight map[string]bool
	ticking  bool
}

// resourcesMsg is one host's resources.get answer.
type resourcesMsg struct {
	host   string
	report server.ResourcesReport
	err    error
}

// resourcesTickMsg is due every resourcesPollEvery.
type resourcesTickMsg struct{}

func fetchResources(cfg *config.Config, host string) (server.ResourcesReport, error) {
	remote, ok := remoteForHost(cfg, host)
	if !ok {
		return server.ResourcesReport{}, errors.New("remote not found")
	}
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := usecase.ServeRPC(ctx, mgr, "resources.get", map[string]any{})
	if err != nil {
		return server.ResourcesReport{}, err
	}
	var body struct {
		Resources server.ResourcesReport `json:"resources"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return server.ResourcesReport{}, fmt.Errorf("decode resources: %w", err)
	}
	return body.Resources, nil
}

// resourceHosts are the configured hosts that have a session listed.
func (m *Model) resourceHosts() []string {
	var hosts []string
	seen := map[string]bool{}
	for _, s := range m.allSessions {
		remote, ok := resolveRemote(m.cfg, s)
		if !ok || seen[remote.Host] {
			continue
		}
		seen[remote.Host] = true
		hosts = append(hosts, remote.Host)
	}
	return hosts
}

// fetchResourcesAll asks every host with sessions, skipping one whose last
// request has not answered.
func (m *Model) fetchResourcesAll() tea.Cmd {
	if m.cfg == nil {
		return nil
	}
	if m.res.inflight == nil {
		m.res.inflight = map[string]bool{}
	}
	var cmds []tea.Cmd
	for _, host := range m.resourceHosts() {
		if m.res.inflight[host] {
			continue
		}
		m.res.inflight[host] = true
		cfg := m.cfg
		cmds = append(cmds, func() tea.Msg {
			report, err := fetchResources(cfg, host)
			return resourcesMsg{host: host, report: report, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func resourcesTick() tea.Cmd {
	return tea.Tick(resourcesPollEvery, func(time.Time) tea.Msg { return resourcesTickMsg{} })
}

// resourcesAfterDiscovery fetches once per host and starts the 10s tick the
// first time it runs. The tick is one chain for the life of the program.
func (m *Model) resourcesAfterDiscovery() tea.Cmd {
	fetch := m.fetchResourcesAll()
	if m.res.ticking {
		return fetch
	}
	m.res.ticking = true
	return tea.Batch(fetch, resourcesTick())
}

// applyResourcesMsg handles an answer or a tick. A tick fetches only while
// the sessions tab is on screen.
func (m *Model) applyResourcesMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case resourcesTickMsg:
		if m.state == viewStateMain && m.currentTab == tabSessions {
			return tea.Batch(m.fetchResourcesAll(), resourcesTick())
		}
		return resourcesTick()
	case resourcesMsg:
		delete(m.res.inflight, msg.host)
		if msg.err != nil || !msg.report.Supported {
			delete(m.res.byHost, msg.host)
			return nil
		}
		if m.res.byHost == nil {
			m.res.byHost = map[string]server.ResourcesReport{}
		}
		m.res.byHost[msg.host] = msg.report
	}
	return nil
}

// sessionResources is the session's scope usage, when its host reports one.
func (m *Model) sessionResources(s domain.Session) (cgroup.SessionUsage, bool) {
	remote, ok := resolveRemote(m.cfg, s)
	if !ok {
		return cgroup.SessionUsage{}, false
	}
	for _, u := range m.res.byHost[remote.Host].Sessions {
		if u.ID == s.ID {
			return u, true
		}
	}
	return cgroup.SessionUsage{}, false
}

// memoryNearLimit reports usage at 90% or more of a finite limit.
func memoryNearLimit(cur, high int64) bool {
	return high > 0 && cur*10 >= high*9
}

func resourceColor(warn bool) color.Color {
	if warn {
		return a1Wait
	}
	return a1Dim
}

// sessionResourceText is "5.2G/6G 150%": memory over its soft limit, then
// CPU in percent of one core. A scope with no limit shows the bare size.
func sessionResourceText(u cgroup.Usage) string {
	mem := cgroup.FormatSize(u.MemoryCurrent)
	if u.MemoryHigh > 0 {
		mem += "/" + cgroup.FormatSize(u.MemoryHigh)
	}
	return fmt.Sprintf("%s %.0f%%", mem, u.CPUPercent)
}

// renderSessionResources is the readout for the detail pane's meta line,
// in the warn colour near the limit or under memory pressure.
func renderSessionResources(u cgroup.Usage) string {
	warn := memoryNearLimit(u.MemoryCurrent, u.MemoryHigh) || u.MemoryPressure > cgroup.AlertPressure
	return a1Text(sessionResourceText(u), resourceColor(warn), false, false)
}

// withSessionResources appends the session's readout to its meta line. A
// line too narrow for it stays as it was.
func (m *Model) withSessionResources(meta string, s domain.Session, width int) string {
	u, ok := m.sessionResources(s)
	if !ok {
		return meta
	}
	readout := renderSessionResources(u.Usage)
	if meta == "" {
		if lipgloss.Width(readout) > width {
			return ""
		}
		return readout
	}
	joined := meta + a1Text(" · ", a1Dim, false, false) + readout
	if lipgloss.Width(joined) > width {
		return meta
	}
	return joined
}

// gaugeHost is the host the header gauge speaks for: the selected session's,
// else the only host that answered.
func (m *Model) gaugeHost() string {
	if it, ok := m.selectedSessionItem(); ok {
		if remote, ok := resolveRemote(m.cfg, it.session); ok {
			return remote.Host
		}
	}
	if len(m.res.byHost) == 1 {
		for host := range m.res.byHost {
			return host
		}
	}
	return ""
}

// headerGauge is "mem 18G/24G" for the user slice of the gauge host, or ""
// when that slice has no finite soft limit or the host reports nothing.
func (m *Model) headerGauge() string {
	report, ok := m.res.byHost[m.gaugeHost()]
	if !ok {
		return ""
	}
	return renderHeaderGauge(report.UserSlice)
}

func renderHeaderGauge(u *cgroup.Usage) string {
	if u == nil || u.MemoryHigh <= 0 {
		return ""
	}
	text := "mem " + cgroup.FormatSize(u.MemoryCurrent) + "/" + cgroup.FormatSize(u.MemoryHigh)
	return a1Text(text, resourceColor(memoryNearLimit(u.MemoryCurrent, u.MemoryHigh)), false, false)
}

// renderA1HeaderGauge is renderA1Header with the gauge after the host name.
// The gauge is dropped before anything else when the line is too narrow.
func renderA1HeaderGauge(host, gauge string, sessions, needsYou int, scanning bool, width int) string {
	plain := renderA1Header(host, sessions, needsYou, scanning, width)
	if gauge == "" || width <= 0 {
		return plain
	}
	left := a1Text("aiman", a1BoldText, true, false) + a1Text(" · "+host+" · ", a1Dim, false, false) + gauge
	right := a1HeaderRight(sessions, needsYou, scanning)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return plain
	}
	return ansi.Truncate(left+strings.Repeat(" ", gap)+right, width, "…")
}
