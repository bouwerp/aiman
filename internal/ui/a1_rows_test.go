package ui

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestRowGlyphMapping(t *testing.T) {
	idleDone := item{
		activity:  "idle",
		hasTodos:  true,
		todoDone:  4,
		todoTotal: 4,
	}
	idlePartial := item{activity: "idle", hasTodos: true, todoDone: 1, todoTotal: 4}
	cases := []struct {
		name  string
		it    item
		glyph string
		col   color.Color
	}{
		{name: "needs input", it: item{needsInput: true, activity: "busy"}, glyph: "●", col: lipgloss.Color("#e0a83a")},
		{name: "escalated", it: item{escalated: true, activity: "idle"}, glyph: "●", col: lipgloss.Color("#e0a83a")},
		{name: "create-failed", it: item{activity: "create-failed"}, glyph: "✗"},
		{name: "exited", it: item{activity: "exited"}, glyph: "✗"},
		{name: "stale", it: item{activity: "stale"}, glyph: "✗"},
		{name: "agent ended", it: item{activity: "busy", session: domain.Session{AgentEnded: true}}, glyph: "✓", col: lipgloss.Color("#86b874")},
		{name: "idle list done", it: idleDone, glyph: "✓", col: lipgloss.Color("#86b874")},
		{name: "idle list open", it: idlePartial, glyph: "○"},
		{name: "idle no list", it: item{activity: "idle"}, glyph: "○"},
		{name: "busy", it: item{activity: "busy"}, glyph: "◐", col: lipgloss.Color("#6ea8dc")},
		{name: "creating", it: item{activity: "creating"}, glyph: "◐", col: lipgloss.Color("#6ea8dc")},
		{name: "bgwait", it: item{activity: "bgwait"}, glyph: "◐", col: lipgloss.Color("#6ea8dc")},
		{name: "terminating", it: item{activity: "terminating"}, glyph: "◐", col: lipgloss.Color("#6ea8dc")},
		{name: "ended beats needs-you only after wait", it: item{needsInput: true, session: domain.Session{AgentEnded: true}}, glyph: "●", col: lipgloss.Color("#e0a83a")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, col := rowGlyph(c.it)
			if got != c.glyph {
				t.Fatalf("glyph %q, want %q", got, c.glyph)
			}
			if c.col != nil && col != c.col {
				t.Fatalf("color %v, want %v", col, c.col)
			}
		})
	}
}

func TestProgressBarUsesFillAndFaint(t *testing.T) {
	orig := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = orig })

	out := progressBar(1, 2, 4, lipgloss.Color("#86b874"))
	if ansi.Strip(out) != "━━━━" {
		t.Fatalf("bar %q", ansi.Strip(out))
	}
	// #86b874 filled, #3a4240 faint.
	if !strings.Contains(out, "38;2;134;184;116") || !strings.Contains(out, "38;2;58;66;64") {
		t.Fatalf("bar colors %q", out)
	}
}

func TestA1RowShowsProgress(t *testing.T) {
	it := item{
		session:    domain.Session{IssueKey: "WTB-2445", Name: "Sundae standby"},
		needsInput: true,
		hasTodos:   true,
		todoDone:   3,
		todoTotal:  7,
	}
	plain := ansi.Strip(renderA1Row(it, false, 60))
	for _, want := range []string{"●", "WTB-2445", "Sundae standby", "━", "3/7"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "input") || strings.Contains(plain, "⚠") {
		t.Fatalf("row still has the old activity chrome: %q", plain)
	}
	if !strings.HasSuffix(plain, "3/7") {
		t.Fatalf("progress should sit at the right edge, got %q", plain)
	}
	orig := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = orig })
	selected := renderA1Row(it, true, 60)
	if got := lipgloss.Width(selected); got != 60 {
		t.Fatalf("selected width %d, want 60", got)
	}
	// 34,41,48 is #222930. Every padded cell has to carry it, not only the text.
	if !strings.Contains(selected, "48;2;34;41;48") {
		t.Fatalf("selected row missing background: %q", selected)
	}
}

func TestA1RowWithoutListHasNoBar(t *testing.T) {
	it := item{session: domain.Session{IssueKey: "WTB-1", Name: "impl", TerminalName: "impl"}}
	plain := ansi.Strip(renderA1Row(it, false, 60))
	if strings.Contains(plain, "━") || strings.Contains(plain, "/") {
		t.Fatalf("session without a list must not show a bar or n/m, got %q", plain)
	}
}

func TestA1RowFitsNarrowWidth(t *testing.T) {
	it := item{
		session:   domain.Session{IssueKey: "WTB-2445", Name: strings.Repeat("n", 60)},
		hasTodos:  true,
		todoDone:  3,
		todoTotal: 7,
	}
	for _, width := range []int{20, 26, 40} {
		out := renderA1Row(it, false, width)
		if got := lipgloss.Width(out); got > width {
			t.Errorf("width %d: rendered %d", width, got)
		}
		if !strings.Contains(ansi.Strip(out), "…") {
			t.Errorf("width %d: missing ellipsis in %q", width, ansi.Strip(out))
		}
		if got := lipgloss.Width(renderA1Row(it, true, width)); got != width {
			t.Errorf("selected width %d: rendered %d", width, got)
		}
		if width < 30 && strings.Contains(ansi.Strip(out), "━") {
			t.Errorf("width %d: bar should be dropped, got %q", width, ansi.Strip(out))
		}
	}
}

func TestA1PalettePrompt(t *testing.T) {
	if a1Prompt != lipgloss.Color("#b994dc") {
		t.Fatalf("prompt color %v", a1Prompt)
	}
}

func TestA1HeaderNeedsYou(t *testing.T) {
	out := renderA1Header("regent0", 4, 1, false, 100)
	plain := ansi.Strip(out)
	if !strings.HasPrefix(plain, "aiman · regent0") {
		t.Fatalf("prefix %q", plain)
	}
	if !strings.HasSuffix(plain, "4 sessions · 1 needs you") {
		t.Fatalf("suffix %q", plain)
	}
	if got := lipgloss.Width(out); got > 100 {
		t.Fatalf("width %d", got)
	}
}

func TestA1HostLabel(t *testing.T) {
	one := &Model{cfg: &config.Config{Remotes: []config.Remote{{Name: "regent0", Host: "10.0.0.1"}}}}
	if got := one.a1HostLabel(); got != "regent0" {
		t.Fatalf("one remote: %q", got)
	}
	many := &Model{cfg: &config.Config{Remotes: []config.Remote{
		{Name: "a", Host: "h1"},
		{Name: "b", Host: "h2"},
	}}}
	if got := many.a1HostLabel(); got != "all remotes" {
		t.Fatalf("many remotes: %q", got)
	}
	many.remoteFilter = "h2"
	if got := many.a1HostLabel(); got != "b" {
		t.Fatalf("filter: %q", got)
	}
}

func TestNeedsYouCountsWaitAndFailedGlyphs(t *testing.T) {
	items := []list.Item{
		item{header: true, activity: "waiting"},
		item{needsInput: true},
		item{activity: "stale"},
		item{activity: "busy"},
		item{escalated: true},
		item{activity: "idle"},
	}
	if got := needsYouCount(items); got != 3 {
		t.Fatalf("needs you %d, want 3", got)
	}
}

func TestMakeItemReadsTodoSummary(t *testing.T) {
	m := &Model{
		cfg: &config.Config{},
		todoSummaries: map[string]todoSummary{
			"s1": {Done: 1, Total: 4, Rev: 2},
		},
	}
	it := m.makeItem(domain.Session{ID: "s1", RemoteHost: "h"})
	if !it.hasTodos || it.todoDone != 1 || it.todoTotal != 4 || it.escalated {
		t.Fatalf("item %+v", it)
	}
}

func TestRefreshTodoProgressUpdatesItem(t *testing.T) {
	m := &Model{todoSummaries: map[string]todoSummary{
		"s1": {Done: 2, Total: 5, Rev: 3},
	}}
	m.list = list.New([]list.Item{
		item{session: domain.Session{ID: "s1", Name: "impl"}},
	}, newSessionListDelegate(), 40, 10)
	m.refreshTodoProgress("s1")
	it := m.list.Items()[0].(item)
	if !it.hasTodos || it.todoDone != 2 || it.todoTotal != 5 {
		t.Fatalf("item %+v", it)
	}
}
