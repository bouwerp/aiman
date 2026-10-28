package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// piModels is a slice of real `pi --list-models` output, parsed.
var piModels = agent.ParsePiModels(`provider    model                              context  max-out  thinking  images
deepseek    deepseek-flash                     1M       384K     yes       yes
openrouter  qwen/qwen3-coder                   262.1K   65.5K    no        no
openrouter  qwen/qwen3-coder-flash             1M       65.5K    no        no
openrouter  deepseek/deepseek-v3.2             163.8K   147.5K   yes       no
`)

func fakeLister(models []agent.ModelOption, err error) modelLister {
	return func(context.Context, string) ([]agent.ModelOption, error) { return models, err }
}

// send feeds msg to the editor and runs any command it returns, feeding the
// result back, so async model listings land like they do in the program.
func send(t *testing.T, m AgentPreferredModel, msg tea.Msg) AgentPreferredModel {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(AgentPreferredModel)
	if cmd != nil {
		if out := cmd(); out != nil {
			if _, quit := out.(tea.QuitMsg); !quit {
				m = send(t, m, out)
			}
		}
	}
	return m
}

func typeText(t *testing.T, m AgentPreferredModel, s string) AgentPreferredModel {
	for _, r := range s {
		m = send(t, m, pressRune(r))
	}
	return m
}

// pickAgentRow moves the agent picker to command and presses enter.
func pickAgentRow(t *testing.T, m AgentPreferredModel, command string) AgentPreferredModel {
	t.Helper()
	i := slices.IndexFunc(m.choices, func(c preferredChoice) bool { return c.command == command })
	if i < 0 {
		t.Fatalf("%s missing from supported agents: %v", command, m.choices)
	}
	for range i {
		m = send(t, m, pressKey("down"))
	}
	return send(t, m, pressKey("enter"))
}

// selectRow moves the current pick list to the row whose label starts with
// prefix and presses enter.
func selectRow(t *testing.T, m AgentPreferredModel, prefix string) AgentPreferredModel {
	t.Helper()
	rows := m.rows()
	i := slices.IndexFunc(rows, func(r pickRow) bool { return strings.HasPrefix(r.label, prefix) })
	if i < 0 {
		t.Fatalf("no row %q in step %d: %+v", prefix, m.step, rows)
	}
	for m.pick < i {
		m = send(t, m, pressKey("down"))
	}
	for m.pick > i {
		m = send(t, m, pressKey("up"))
	}
	return send(t, m, pressKey("enter"))
}

func TestPreferredEditorReordersAndSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{
		AgentDefaults: map[string]config.AgentDefaults{"claude": {Model: "sonnet"}},
		Agents:        config.AgentsConfig{Preferred: config.PreferredFromCommands("claude", "grok")},
	}
	m := NewAgentPreferredModel(cfg, "", nil)

	m = send(t, m, pressKey("down"))
	m = send(t, m, pressKey("J"))
	if len(m.items) != 2 || m.items[0].Agent != "grok" || m.items[1].Agent != "claude" {
		t.Fatalf("reorder = %v", m.items)
	}

	m = send(t, m, pressRune('a'))
	if m.step != stepAgent || len(m.choices) == 0 {
		t.Fatalf("step=%v choices=%v", m.step, m.choices)
	}
	m = pickAgentRow(t, m, "codex")
	if m.step != stepModel {
		t.Fatalf("step = %v, want the model step", m.step)
	}
	m = selectRow(t, m, "Default")
	if m.step != stepEffort {
		t.Fatalf("step = %v, want the effort step", m.step)
	}
	m = selectRow(t, m, "Default")
	if m.inFlow() || len(m.items) != 3 || m.items[2] != (config.PreferredAgent{Agent: "codex"}) {
		t.Fatalf("step=%v append = %v", m.step, m.items)
	}
	m = send(t, m, pressRune('d'))
	if len(m.items) != 2 || slices.Contains(m.items, config.PreferredAgent{Agent: "codex"}) {
		t.Fatalf("delete = %v", m.items)
	}
	m = send(t, m, pressKey("enter"))
	if !m.saved {
		t.Fatalf("not saved: %v", m.err)
	}
	if got := cfg.Agents.PreferredCommands(); !slices.Equal(got, []string{"grok", "claude"}) {
		t.Fatalf("saved = %v", got)
	}
	if cfg.AgentDefaults["claude"].Model != "sonnet" {
		t.Fatal("agent_defaults was modified")
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".aiman", "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "preferred:") {
		t.Fatalf("config missing preferred: %v\n%s", err, raw)
	}
}

func TestPreferredPiProviderFilterModelEffort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: config.PreferredFromCommands("claude")}}
	m := NewAgentPreferredModel(cfg, "regent0", fakeLister(piModels, nil))

	m = send(t, m, pressRune('a'))
	m = pickAgentRow(t, m, "pi")
	if m.step != stepProvider {
		t.Fatalf("step = %v, want providers (err %v)", m.step, m.loadErr)
	}
	view := m.viewString()
	if !strings.Contains(view, "openrouter (3)") || !strings.Contains(view, "deepseek (1)") || !strings.Contains(view, "models from regent0") {
		t.Fatalf("provider view:\n%s", view)
	}
	m = selectRow(t, m, "openrouter")
	if m.step != stepModel || len(m.rows()) != 3 {
		t.Fatalf("step=%v rows=%+v", m.step, m.rows())
	}
	m = typeText(t, m, "qwen3-coder")
	if rows := m.rows(); len(rows) != 2 || rows[0].value != "openrouter/qwen/qwen3-coder" {
		t.Fatalf("filtered rows = %+v", rows)
	}
	m = send(t, m, pressKey("enter"))
	if m.step != stepEffort {
		t.Fatalf("step = %v, want effort", m.step)
	}
	m = selectRow(t, m, "high")
	want := config.PreferredAgent{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"}
	if m.inFlow() || len(m.items) != 2 || m.items[1] != want {
		t.Fatalf("items = %+v", m.items)
	}
	if !strings.Contains(m.viewString(), "pi  openrouter/qwen/qwen3-coder  high") {
		t.Fatalf("row not rendered:\n%s", m.viewString())
	}

	// The same agent again with another model is a second entry.
	m = send(t, m, pressRune('a'))
	m = pickAgentRow(t, m, "pi")
	m = selectRow(t, m, "deepseek")
	m = selectRow(t, m, "deepseek-flash")
	m = selectRow(t, m, "Default")
	if len(m.items) != 3 || m.items[2] != (config.PreferredAgent{Agent: "pi", Model: "deepseek/deepseek-flash"}) {
		t.Fatalf("items = %+v", m.items)
	}

	m = send(t, m, pressKey("enter"))
	if !m.saved {
		t.Fatalf("not saved: %v", m.err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Agents.Preferred) != 3 || loaded.Agents.Preferred[1] != want {
		t.Fatalf("reloaded = %+v", loaded.Agents.Preferred)
	}
}

func TestPreferredEditChangesModelAndEffort(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: []config.PreferredAgent{
		{Agent: "claude"},
		{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"},
	}}}
	m := NewAgentPreferredModel(cfg, "regent0", fakeLister(piModels, nil))
	m = send(t, m, pressKey("down"))
	m = send(t, m, pressRune('e'))
	if m.step != stepProvider {
		t.Fatalf("step = %v", m.step)
	}
	m = selectRow(t, m, "openrouter")
	m = typeText(t, m, "flash")
	m = send(t, m, pressKey("enter"))
	if m.step != stepEffort || m.rows()[m.pick].value != "high" {
		t.Fatalf("effort step must start on the current effort: step=%v pick=%d", m.step, m.pick)
	}
	m = selectRow(t, m, "low")
	want := config.PreferredAgent{Agent: "pi", Model: "openrouter/qwen/qwen3-coder-flash", Effort: "low"}
	if len(m.items) != 2 || m.items[1] != want || m.cursor != 1 {
		t.Fatalf("items = %+v cursor=%d", m.items, m.cursor)
	}

	// e on a static-catalog agent: pick a listed model, default effort.
	m = send(t, m, pressKey("up"))
	m = send(t, m, pressRune('e'))
	if m.step != stepModel || m.dynamic {
		t.Fatalf("step = %v dynamic=%v", m.step, m.dynamic)
	}
	m = selectRow(t, m, "opus")
	m = selectRow(t, m, "Default")
	if m.items[0].Agent != "claude" || m.items[0].Model == "" || m.items[0].Effort != "" {
		t.Fatalf("claude = %+v", m.items[0])
	}
}

func TestPreferredStaticCatalogOtherModel(t *testing.T) {
	m := NewAgentPreferredModel(&config.Config{}, "", nil)
	m = send(t, m, pressRune('a'))
	m = pickAgentRow(t, m, "codex")
	rows := m.rows()
	if len(rows) < 3 || rows[0].kind != rowDefault || rows[len(rows)-1].kind != rowOther {
		t.Fatalf("rows = %+v", rows)
	}
	m = selectRow(t, m, "Other")
	if m.step != stepModelTyping {
		t.Fatalf("step = %v", m.step)
	}
	m = typeText(t, m, "gpt-x")
	m = send(t, m, pressKey("enter"))
	m = selectRow(t, m, "high")
	if len(m.items) != 1 || m.items[0] != (config.PreferredAgent{Agent: "codex", Model: "gpt-x", Effort: "high"}) {
		t.Fatalf("items = %+v", m.items)
	}
}

func TestPreferredRemoteErrorFallsBackToTyping(t *testing.T) {
	m := NewAgentPreferredModel(&config.Config{}, "regent0", fakeLister(nil, errors.New("remote host is unreachable")))
	m = send(t, m, pressRune('a'))
	m = pickAgentRow(t, m, "pi")
	if m.step != stepModelTyping || m.loadErr == nil {
		t.Fatalf("step=%v err=%v", m.step, m.loadErr)
	}
	if !strings.Contains(m.viewString(), "remote host is unreachable") {
		t.Fatalf("error not shown:\n%s", m.viewString())
	}
	m = typeText(t, m, "openrouter/qwen/qwen3-coder")
	m = send(t, m, pressKey("enter"))
	m = selectRow(t, m, "max")
	if len(m.items) != 1 || m.items[0] != (config.PreferredAgent{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "max"}) {
		t.Fatalf("items = %+v", m.items)
	}

	// No remote at all also falls back to typing.
	m = NewAgentPreferredModel(&config.Config{}, "", nil)
	m = send(t, m, pressRune('a'))
	m = pickAgentRow(t, m, "pi")
	if m.step != stepModelTyping {
		t.Fatalf("step = %v", m.step)
	}
}

func TestPreferredLoadingShowsHost(t *testing.T) {
	m := NewAgentPreferredModel(&config.Config{}, "regent0", fakeLister(piModels, nil))
	m = send(t, m, pressRune('a'))
	i := slices.IndexFunc(m.choices, func(c preferredChoice) bool { return c.command == "pi" })
	for range i {
		m = send(t, m, pressKey("down"))
	}
	next, cmd := m.Update(pressKey("enter"))
	m = next.(AgentPreferredModel)
	if cmd == nil || m.step != stepModelLoading || !strings.Contains(m.viewString(), "Listing models on regent0") {
		t.Fatalf("step=%v view:\n%s", m.step, m.viewString())
	}
	// A listing for another agent is ignored.
	m = send(t, m, preferredModelsMsg{command: "other", models: piModels})
	if m.step != stepModelLoading {
		t.Fatalf("stray listing applied: step=%v", m.step)
	}
}

func TestPreferredEscInAddStaysOnPage(t *testing.T) {
	m, _ := promptModel(domainSessionForPreferred())
	m.state = viewStateMenu
	m.agentPreferred = NewAgentPreferredModel(m.cfg, "", nil)
	m.agentPreferred.step = stepAgentTyping
	m.agentPreferred.input = "codex"
	m.state = viewStateAgentPreferred
	next, _ := m.handleAgentPreferredUpdate(pressKey("esc"))
	m = next.(*Model)
	if m.state != viewStateAgentPreferred {
		t.Fatalf("state = %v, want the editor", m.state)
	}
	if m.agentPreferred.step == stepAgentTyping {
		t.Fatal("esc did not leave typing")
	}
}

// The page opens from the Admin Menu on the default remote and completes a
// Pi add without any YAML editing.
func TestPreferredFromAdminMenuAddsPiModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{
		Remotes:      []config.Remote{{Name: "regent0", Host: "regent0.example"}},
		ActiveRemote: "regent0.example",
	}
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.SetSize(120, 40)
	var listedOn string
	m.agentModelLister = func(r config.Remote) modelLister {
		listedOn = r.Host
		return fakeLister(piModels, nil)
	}
	m.state = viewStateMenu
	found := false
	for i, item := range m.menu.Items() {
		if mi, ok := item.(menuItem); ok && mi.action == viewStateAgentPreferred {
			if !strings.Contains(mi.desc, "models") {
				t.Fatalf("menu description %q must mention models", mi.desc)
			}
			m.menu.Select(i)
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Preferred agents missing from the Admin Menu")
	}
	next, _ := m.handleMenuUpdate(pressKey("enter"))
	m = next.(*Model)
	if m.state != viewStateAgentPreferred || listedOn != "regent0.example" {
		t.Fatalf("state=%v listedOn=%q", m.state, listedOn)
	}

	press := func(msg tea.Msg) {
		t.Helper()
		next, cmd := m.handleAgentPreferredUpdate(msg)
		m = next.(*Model)
		for cmd != nil {
			out := cmd()
			cmd = nil
			if out != nil {
				next, cmd = m.handleAgentPreferredUpdate(out)
				m = next.(*Model)
			}
		}
	}
	press(pressRune('a'))
	for m.agentPreferred.choices[m.agentPreferred.pick].command != "pi" {
		press(pressKey("down"))
	}
	press(pressKey("enter"))
	if m.agentPreferred.step != stepProvider || !strings.Contains(m.agentPreferred.viewString(), "models from regent0") {
		t.Fatalf("step=%v view:\n%s", m.agentPreferred.step, m.agentPreferred.viewString())
	}
	for m.agentPreferred.rows()[m.agentPreferred.pick].value != "openrouter" {
		press(pressKey("down"))
	}
	press(pressKey("enter"))
	for _, r := range "qwen3-coder" {
		press(pressRune(r))
	}
	press(pressKey("enter"))
	for m.agentPreferred.rows()[m.agentPreferred.pick].value != "high" {
		press(pressKey("down"))
	}
	press(pressKey("enter"))
	press(pressKey("enter"))
	if m.state != viewStateMenu {
		t.Fatalf("save did not return to the menu: state=%v err=%v", m.state, m.agentPreferred.err)
	}
	want := []config.PreferredAgent{{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"}}
	if !slices.Equal(cfg.Agents.Preferred, want) {
		t.Fatalf("saved = %+v", cfg.Agents.Preferred)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".aiman", "config.yaml"))
	if err != nil || !strings.Contains(string(raw), "model: openrouter/qwen/qwen3-coder") {
		t.Fatalf("config file: %v\n%s", err, raw)
	}
}

func TestPreferredEmptyListWritesKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: []config.PreferredAgent{}}}
	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "preferred: []") {
		t.Fatalf("empty preferred was omitted:\n%s", body)
	}
}

func domainSessionForPreferred() domain.Session {
	return domain.Session{ID: "p", RemoteHost: "regent0"}
}

var _ tea.Model = AgentPreferredModel{}

func TestPreferredClearedListRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Agents: config.AgentsConfig{Preferred: config.PreferredFromCommands("claude")}}
	m := NewAgentPreferredModel(cfg, "", nil)
	m = send(t, m, pressRune('d'))
	m = send(t, m, pressKey("enter"))
	if !m.saved {
		t.Fatalf("not saved: %v", m.err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.Preferred == nil || len(loaded.Agents.Preferred) != 0 {
		t.Fatalf("cleared preferred reloaded as %#v", loaded.Agents.Preferred)
	}
}

func TestPreferredEditorAcceptsTypedCommand(t *testing.T) {
	m := NewAgentPreferredModel(&config.Config{}, "", nil)
	m = send(t, m, pressRune('a'))
	for range m.choices {
		m = send(t, m, pressKey("down"))
	}
	m = send(t, m, pressKey("enter"))
	if m.step != stepAgentTyping {
		t.Fatal("the last row must switch to typing a command")
	}
	m = typeText(t, m, "my-agent")
	m = send(t, m, pressKey("enter"))
	// An unknown agent has no catalog: Default or Other, then no effort step.
	m = selectRow(t, m, "Default")
	if m.inFlow() || len(m.items) != 1 || m.items[0] != (config.PreferredAgent{Agent: "my-agent"}) {
		t.Fatalf("step=%v items=%v", m.step, m.items)
	}
}
