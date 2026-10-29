package ui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestMetaLineSkipsMissingFields(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := domain.Session{
		AgentName:  "claude",
		AgentModel: "opus",
		Branch:     "wtb-2445-standby",
		CreatedAt:  now.Add(-42 * time.Minute),
	}
	plain := ansi.Strip(renderMetaLine(s, detailMetaExtras{}, now, 120))
	if plain != "claude opus · 42m · wtb-2445-standby" {
		t.Fatalf("meta %q", plain)
	}

	cost := detailMetaExtras{HasCost: true, CostUSD: 1.234, HasCache: true, CacheRatio: 0.81, WatchingPR: 12}
	withCost := ansi.Strip(renderMetaLine(s, cost, now, 200))
	if !strings.HasSuffix(withCost, "~$1.23 · cache 81% · watching PR #12") {
		t.Fatalf("cost meta %q", withCost)
	}

	tok := detailMetaExtras{Tokens: 412000, HasCache: true, CacheRatio: 0.5}
	withTok := ansi.Strip(renderMetaLine(s, tok, now, 200))
	if !strings.HasSuffix(withTok, "412k tok · cache 50%") {
		t.Fatalf("token meta %q", withTok)
	}
	if strings.Contains(withTok, "$") {
		t.Fatalf("unpriced tokens must not show a dollar amount, got %q", withTok)
	}
}

func TestMetaLineContextGauge(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := domain.Session{AgentName: "claude", AgentModel: "opus", Branch: "wtb-2445-standby", CreatedAt: now.Add(-42 * time.Minute)}
	base := detailMetaExtras{HasCache: true, CacheRatio: 0.5}

	at62 := base
	at62.ContextWindow = 200_000
	at62.ContextPct = 0.62
	line := renderMetaLine(s, at62, now, 200)
	plain := ansi.Strip(line)
	if !strings.HasSuffix(plain, "cache 50% · ctx 62%") {
		t.Fatalf("62%% line %q", plain)
	}
	if !strings.Contains(line, a1Text("ctx 62%", a1Dim, false, false)) {
		t.Fatalf("62%% gauge is not dim: %q", line)
	}

	at85 := base
	at85.ContextWindow = 200_000
	at85.ContextPct = 0.85
	line = renderMetaLine(s, at85, now, 200)
	if !strings.Contains(ansi.Strip(line), "ctx 85%") {
		t.Fatalf("85%% line %q", ansi.Strip(line))
	}
	if !strings.Contains(line, a1Text("ctx 85%", a1Wait, false, false)) {
		t.Fatalf("85%% gauge is not the warn style: %q", line)
	}

	at96 := base
	at96.ContextWindow = 200_000
	at96.ContextPct = 0.96
	line = renderMetaLine(s, at96, now, 200)
	if !strings.Contains(ansi.Strip(line), "ctx 96%") {
		t.Fatalf("96%% line %q", ansi.Strip(line))
	}
	if !strings.Contains(line, a1Text("ctx 96%", checksColor("failure"), false, false)) {
		t.Fatalf("96%% gauge is not the red style: %q", line)
	}

	plain = ansi.Strip(renderMetaLine(s, base, now, 200))
	if strings.Contains(plain, "ctx") {
		t.Fatalf("no context data rendered %q", plain)
	}
}

func TestGitPRLineCompact(t *testing.T) {
	st := domain.GitStatus{
		Ahead:          2,
		Behind:         0,
		StagedCount:    1,
		UnstagedCount:  1,
		UntrackedCount: 1,
		PullRequest: &domain.PullRequest{
			Number:        12,
			State:         "OPEN",
			ReviewStatus:  "approved",
			ChecksStatus:  "success",
			ChecksSummary: "3/3",
		},
	}
	line := renderGitPRLine(st, 160)
	if strings.Contains(line, "\n") {
		t.Fatalf("git/PR line wrapped:\n%s", line)
	}
	plain := ansi.Strip(line)
	for _, want := range []string{"↑2↓0", "3 changed", "PR #12", "CI:3/3", "Review PR"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in %q", want, plain)
		}
	}

	drift := ansi.Strip(renderGitPRLine(domain.GitStatus{Ahead: 2, Behind: 1}, 80))
	if !strings.Contains(drift, "↑2↓1") || !strings.Contains(drift, "No open PR") {
		t.Fatalf("ahead/behind without a PR = %q", drift)
	}

	clean := renderGitPRLine(domain.GitStatus{Branch: "main"}, 80)
	if clean != "" {
		t.Fatalf("clean tree with no PR = %q, want empty", clean)
	}

	narrow := renderGitPRLine(st, 40)
	if w := lipgloss.Width(narrow); w > 40 {
		t.Fatalf("width 40: rendered %d", w)
	}
	if !strings.Contains(ansi.Strip(narrow), "PR #12") {
		t.Fatalf("narrow line dropped the PR: %q", ansi.Strip(narrow))
	}
}

func TestTodoLinesRenderStates(t *testing.T) {
	orig := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = orig })

	snap := todoSnapshot{
		HasList: true,
		List: domain.TodoList{
			Rev:        1,
			NotInAgent: []string{"t4"},
			Items: []domain.TodoItem{
				{ID: "t1", Text: "Add standby template", State: domain.TodoDone},
				{ID: "t2", Text: "Run preprod --check", State: domain.TodoActive},
				{ID: "t3", Text: "Open PR", State: domain.TodoPending},
				{ID: "t4", Text: "Old idea", State: domain.TodoDropped},
			},
		},
	}
	lines := renderTodoLines(todoView{Snap: snap, Cursor: -1}, 80)
	if len(lines) != 4 {
		t.Fatalf("lines %d: %q", len(lines), lines)
	}
	want := []string{
		"▸ Run preprod --check",
		"○ Open PR",
		"✓ Add standby template",
		"– Old idea ⚠ not in agent's list",
	}
	for i, line := range lines {
		if got := ansi.Strip(line); got != want[i] {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}
	if !strings.Contains(lines[3], "9m") {
		t.Fatalf("dropped line should be struck, got %q", lines[3])
	}
}

func TestQuestionSitsUnderActiveItem(t *testing.T) {
	snap := todoSnapshot{
		HasList: true,
		List: domain.TodoList{
			Rev: 1,
			Items: []domain.TodoItem{
				{ID: "t1", Text: "Add standby template", State: domain.TodoDone},
				{ID: "t2", Text: "Run preprod --check", State: domain.TodoActive},
				{ID: "t3", Text: "Open PR", State: domain.TodoPending},
			},
		},
	}
	q := []string{"Standby is 5.59M slots behind.", "Proceed with handover? (y/n)"}
	lines := renderTodoLines(todoView{Snap: snap, Cursor: -1, Question: q}, 80)
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = ansi.Strip(line)
	}
	active := -1
	for i, line := range plain {
		if strings.Contains(line, "▸") {
			active = i
			break
		}
	}
	if active < 0 || active+2 >= len(plain) {
		t.Fatalf("active item not found in %q", plain)
	}
	if !strings.Contains(plain[active+1], "│ Standby is 5.59M slots behind.") || !strings.Contains(plain[active+2], "│ Proceed with handover? (y/n)") {
		t.Fatalf("question not under the active item: %q", plain)
	}
}

func TestQuestionOnlyWhenWaiting(t *testing.T) {
	it := item{session: domain.Session{
		HookState:        domain.AgentStateIdle,
		HookStateMessage: "Proceed with handover?",
	}}
	if got := waitingQuestion(it, 80); got != nil {
		t.Fatalf("idle hook state returned %q", got)
	}
}

func TestQuestionRendersWhenThereIsNoList(t *testing.T) {
	m := &Model{}
	it := item{session: domain.Session{
		HookState:        domain.AgentStateWaitingInput,
		HookStateMessage: "Proceed with handover?",
	}}
	lines := strings.Split(ansi.Strip(m.renderSessionDetail(it, 80)), "\n")
	if len(lines) < 2 || lines[0] != "no to-do list yet" || !strings.Contains(lines[1], "│ Proceed with handover?") {
		t.Fatalf("question not under the empty list: %q", lines)
	}
}

func TestQuestionRendersWhenTodosUnsupported(t *testing.T) {
	m := &Model{todoUnsupported: map[string]bool{"regent0": true}}
	it := item{session: domain.Session{
		RemoteHost:       "regent0",
		HookState:        domain.AgentStateWaitingInput,
		HookStateMessage: "Proceed with handover?",
	}}
	lines := strings.Split(ansi.Strip(m.renderSessionDetail(it, 80)), "\n")
	if len(lines) < 2 || lines[0] != "to-dos unavailable on this remote" || !strings.Contains(lines[1], "│ Proceed with handover?") {
		t.Fatalf("question not under the unsupported status: %q", lines)
	}
}

func TestTodoWindowKeepsCursorVisible(t *testing.T) {
	orig := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = orig })

	items := make([]domain.TodoItem, 12)
	for i := range items {
		items[i] = domain.TodoItem{ID: "t", Text: "item " + strconv.Itoa(i), State: domain.TodoPending}
	}
	const width = 80
	lines := renderTodoLines(todoView{
		Snap:   todoSnapshot{HasList: true, List: domain.TodoList{Items: items, Rev: 1}},
		Cursor: 10,
	}, width)
	if len(lines) != 8 {
		t.Fatalf("window lines %d, want 8: %q", len(lines), stripLines(lines))
	}
	joined := strings.Join(stripLines(lines), "\n")
	if !strings.Contains(joined, "item 10") {
		t.Fatalf("cursor item missing: %q", joined)
	}
	last := ansi.Strip(lines[len(lines)-1])
	if !strings.Contains(last, "+") || !strings.Contains(last, "more") {
		t.Fatalf("last row = %q, want a + more line", last)
	}
	cursor := ""
	for _, line := range lines {
		if strings.Contains(ansi.Strip(line), "item 10") {
			cursor = line
			break
		}
	}
	if !strings.Contains(cursor, "48;2;34;41;48") {
		t.Fatalf("cursor row missing selected background: %q", cursor)
	}
	if got := lipgloss.Width(cursor); got > width {
		t.Fatalf("cursor width %d", got)
	}
}

func TestNoListYet(t *testing.T) {
	lines := renderTodoLines(todoView{Cursor: -1}, 40)
	if len(lines) != 1 || ansi.Strip(lines[0]) != "no to-do list yet" {
		t.Fatalf("empty snapshot = %q", stripLines(lines))
	}
}

func stripLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = ansi.Strip(line)
	}
	return out
}

// The tier sits right after the agent and model.
func TestMetaLineShowsTier(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	plain := ansi.Strip(renderMetaLine(domain.Session{
		AgentName: "pi", AgentModel: "kimi-k3", Tier: "worker",
	}, detailMetaExtras{}, now, 120))
	if plain != "pi kimi-k3 · worker" {
		t.Fatalf("meta %q", plain)
	}
	if got := ansi.Strip(renderMetaLine(domain.Session{AgentName: "pi"}, detailMetaExtras{}, now, 120)); got != "pi" {
		t.Fatalf("no tier: %q", got)
	}
}
