package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/charmbracelet/x/ansi"
)

func promptModel(sessions ...domain.Session) (*Model, *fakeTodoClient) {
	m, fake := todoEventModel(sessions...)
	m.state = viewStateMain
	m.promptSent = map[string]time.Time{}
	m.creatingSessions = map[string]*creatingSession{}
	m.version = "v0.0.0-test"
	m.width = 120
	m.height = 40
	m.list.SetSize(40, 20)
	return m, fake
}

func TestCtrlPFocusesPrompt(t *testing.T) {
	const id = "s1"
	m, fake := promptModel(domain.Session{ID: id, IssueKey: "AIM-1", Name: "impl", RemoteHost: "regent0"})

	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	if m.focus != focusPrompt {
		t.Fatalf("focus = %v, want focusPrompt", m.focus)
	}

	next, _ = m.Update(pressKey("hi there"))
	m = next.(*Model)
	next, cmd := m.Update(pressKey("enter"))
	m = next.(*Model)
	if m.focus != focusPrompt {
		t.Fatalf("focus after send = %v, want focusPrompt", m.focus)
	}
	if cmd == nil {
		t.Fatal("enter with text did not return a send command")
	}
	_ = cmd()
	if len(fake.prompts) != 1 || fake.prompts[0] != (fakePrompt{Session: id, Text: "hi there"}) {
		t.Fatalf("prompts = %+v", fake.prompts)
	}

	next, _ = m.Update(pressKey("esc"))
	m = next.(*Model)
	if m.focus != focusList {
		t.Fatalf("focus after esc = %v, want focusList", m.focus)
	}
}

func TestPromptFocusSwallowsMainKeys(t *testing.T) {
	m, _ := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)

	next, _ = m.Update(pressKey("n"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("q"))
	m = next.(*Model)

	if m.state != viewStateMain {
		t.Fatalf("state = %v, want viewStateMain", m.state)
	}
	if m.promptInput.Value() != "nq" {
		t.Fatalf("input = %q, want nq", m.promptInput.Value())
	}
}

func TestBlankPromptNotSent(t *testing.T) {
	m, fake := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)

	next, cmd := m.Update(pressKey("enter"))
	m = next.(*Model)
	if cmd != nil {
		_ = cmd()
	}
	if len(fake.prompts) != 0 {
		t.Fatalf("prompts = %+v", fake.prompts)
	}
	if m.focus != focusPrompt {
		t.Fatalf("focus = %v, want focusPrompt", m.focus)
	}
}

func TestPromptSendErrorRestoresText(t *testing.T) {
	m, fake := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	fake.err = errors.New("offline")
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("hi there"))
	m = next.(*Model)
	next, cmd := m.Update(pressKey("enter"))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("expected a send command")
	}
	next, toast := m.Update(cmd())
	m = next.(*Model)
	if m.promptInput.Value() != "hi there" {
		t.Fatalf("input = %q, want the text restored", m.promptInput.Value())
	}
	const want = "⚠️  Reply not sent: offline"
	if m.snapshotToast != want {
		t.Fatalf("toast = %q", m.snapshotToast)
	}
	if toast == nil {
		t.Fatal("expected the toast to clear")
	}
	if _, sent := m.promptSent["s1"]; sent {
		t.Fatal("a failed send left a prompt timestamp")
	}
}

func TestPromptFailedSendStaysOnTheSessionThatSentIt(t *testing.T) {
	a := domain.Session{ID: "a", IssueKey: "AIM-1", Name: "one", RemoteHost: "regent0"}
	b := domain.Session{ID: "b", IssueKey: "AIM-2", Name: "two", RemoteHost: "regent0"}

	t.Run("empty neighbour", func(t *testing.T) {
		m, fake := promptModel(a, b)
		fake.err = errors.New("offline")
		m, cmd := sendThenLeave(t, m, "for a")

		next, _ := m.Update(pressKey("down"))
		m = next.(*Model)
		if m.promptInput.Value() != "" {
			t.Fatalf("B field = %q before the error", m.promptInput.Value())
		}
		next, _ = m.Update(cmd())
		m = next.(*Model)
		if m.promptInput.Value() != "" {
			t.Fatalf("B field received %q", m.promptInput.Value())
		}
		if m.promptBuf["a"] != "for a" {
			t.Fatalf("A draft = %q", m.promptBuf["a"])
		}
		next, _ = m.Update(pressKey("up"))
		m = next.(*Model)
		if m.promptInput.Value() != "for a" {
			t.Fatalf("A field = %q", m.promptInput.Value())
		}
	})

	t.Run("neighbour already has a draft", func(t *testing.T) {
		m, fake := promptModel(a, b)
		fake.err = errors.New("offline")
		m, cmd := sendThenLeave(t, m, "for a")

		next, _ := m.Update(pressKey("down"))
		m = next.(*Model)
		next, _ = m.Update(pressKey("ctrl+p"))
		m = next.(*Model)
		next, _ = m.Update(pressKey("keep b"))
		m = next.(*Model)
		next, _ = m.Update(cmd())
		m = next.(*Model)
		if m.promptInput.Value() != "keep b" {
			t.Fatalf("B draft = %q", m.promptInput.Value())
		}
		if m.promptBuf["a"] != "for a" {
			t.Fatalf("A draft = %q", m.promptBuf["a"])
		}
		next, _ = m.Update(pressKey("esc"))
		m = next.(*Model)
		next, _ = m.Update(pressKey("up"))
		m = next.(*Model)
		if m.promptInput.Value() != "for a" {
			t.Fatalf("A field = %q", m.promptInput.Value())
		}
	})
}

func sendThenLeave(t *testing.T, m *Model, text string) (*Model, tea.Cmd) {
	t.Helper()
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	next, _ = m.Update(pressKey(text))
	m = next.(*Model)
	next, cmd := m.Update(pressKey("enter"))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("enter did not send")
	}
	next, _ = m.Update(pressKey("esc"))
	return next.(*Model), cmd
}

func TestPromptSecondEnterWaitsAndStaleResultIsIgnored(t *testing.T) {
	const id = "s1"
	m, fake := promptModel(domain.Session{ID: id, Name: "impl", RemoteHost: "regent0"})
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("first"))
	m = next.(*Model)
	next, cmd := m.Update(pressKey("enter"))
	m = next.(*Model)
	if cmd == nil {
		t.Fatal("first enter did not send")
	}

	next, _ = m.Update(pressKey("second"))
	m = next.(*Model)
	next, again := m.Update(pressKey("enter"))
	m = next.(*Model)
	if again != nil {
		_ = again()
	}
	_ = cmd()
	if len(fake.prompts) != 1 || fake.prompts[0].Text != "first" {
		t.Fatalf("prompts = %+v, want only the first note", fake.prompts)
	}
	if m.promptInput.Value() != "second" {
		t.Fatalf("input = %q, want the unsent second line", m.promptInput.Value())
	}

	sent := m.promptSent[id]
	next, _ = m.Update(promptSentMsg{
		host: "regent0", sessionID: id, sentAt: sent.Add(-time.Second), err: errors.New("late"),
	})
	m = next.(*Model)
	if m.promptInput.Value() != "second" {
		t.Fatalf("stale error set input to %q", m.promptInput.Value())
	}
	if !m.promptSent[id].Equal(sent) {
		t.Fatal("stale result replaced the in-flight send time")
	}

	next, _ = m.Update(promptSentMsg{
		host: "regent0", sessionID: id, sentAt: sent, err: errors.New("offline"),
	})
	m = next.(*Model)
	if m.promptInput.Value() != "second" {
		t.Fatalf("error restored over a newer line: %q", m.promptInput.Value())
	}
}

func TestPromptSubmitsToTheSessionThatOpenedIt(t *testing.T) {
	a := domain.Session{ID: "a", IssueKey: "AIM-1", Name: "one", RemoteHost: "regent0"}
	b := domain.Session{ID: "b", IssueKey: "AIM-2", Name: "two", RemoteHost: "regent0"}
	m, fake := promptModel(a, b)
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("note"))
	m = next.(*Model)
	m.list.Select(1)

	_, cmd := m.Update(pressKey("enter"))
	if cmd == nil {
		t.Fatal("expected a send")
	}
	_ = cmd()
	if len(fake.prompts) != 1 || fake.prompts[0].Session != "a" || fake.prompts[0].Text != "note" {
		t.Fatalf("prompts = %+v, want session a", fake.prompts)
	}
}

func TestPromptDraftFollowsTheSelectedSession(t *testing.T) {
	a := domain.Session{ID: "a", IssueKey: "AIM-1", Name: "one", RemoteHost: "regent0"}
	b := domain.Session{ID: "b", IssueKey: "AIM-2", Name: "two", RemoteHost: "regent0"}
	m, _ := promptModel(a, b)
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("for a"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("esc"))
	m = next.(*Model)
	next, _ = m.Update(pressKey("down"))
	m = next.(*Model)

	if m.promptInput.Value() != "" {
		t.Fatalf("AIM-2 is showing %q", m.promptInput.Value())
	}
	if plain := ansi.Strip(m.renderMainView()); !strings.Contains(plain, "Reply to AIM-2") {
		t.Fatalf("unfocused line:\n%s", plain)
	}
	next, _ = m.Update(pressKey("up"))
	m = next.(*Model)
	if m.promptInput.Value() != "for a" {
		t.Fatalf("AIM-1 draft = %q", m.promptInput.Value())
	}
}

func TestPromptFocusBlocksListSelection(t *testing.T) {
	a := domain.Session{ID: "a", IssueKey: "AIM-1", Name: "one", RemoteHost: "regent0"}
	b := domain.Session{ID: "b", IssueKey: "AIM-2", Name: "two", RemoteHost: "regent0"}
	m, _ := promptModel(a, b)
	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	idx := m.list.Index()

	next, _ = m.forwardToFocused(pressKey("down"), nil)
	m = next.(*Model)
	if m.list.Index() != idx {
		t.Fatalf("list index %d, want %d while replying", m.list.Index(), idx)
	}
	click := tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseLeft}
	next, _ = m.Update(click)
	m = next.(*Model)
	if got, _ := m.selectedSessionItem(); got.session.ID != "a" {
		t.Fatalf("mouse moved the reply onto %s", got.session.ID)
	}
}

func TestQuestionKeepsTheDashboardInTheTerminal(t *testing.T) {
	next, _ := newTestStartupModel(&startupSessionRepo{
		sessions: []domain.Session{
			{ID: "a", Name: "impl", Group: "G", RemoteHost: "regent0", TmuxSession: "t1"},
			{ID: "b", Name: "review", Group: "G", RemoteHost: "regent0", TmuxSession: "t2"},
		},
	}).Update(startupReadyMsg{})
	dash := next.(*Model)
	dash.SetSize(80, 24)
	beforeList := dash.list.Height()
	beforeView := dash.viewport.Height()

	next, _ = dash.Update(pressKey("?"))
	dash = next.(*Model)
	view := dash.View().Content
	if h := lipgloss.Height(view); h > 24 {
		t.Fatalf("80x24 with keys open is %d rows", h)
	}
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "n: new") || !strings.Contains(plain, "t: tunnels") {
		t.Fatalf("help is not two lines:\n%s", plain)
	}
	if dash.list.Height() != beforeList-1 || dash.viewport.Height() != beforeView-1 {
		t.Fatalf("list %d->%d viewport %d->%d, want both one row shorter",
			beforeList, dash.list.Height(), beforeView, dash.viewport.Height())
	}

	next, _ = dash.Update(pressKey("?"))
	dash = next.(*Model)
	if h := lipgloss.Height(dash.View().Content); h > 24 {
		t.Fatalf("after closing keys, height %d", h)
	}
	if dash.list.Height() != beforeList || dash.viewport.Height() != beforeView {
		t.Fatalf("chrome was not given back: list %d viewport %d", dash.list.Height(), dash.viewport.Height())
	}
}

func TestPromptMarkerShownAfterSend(t *testing.T) {
	const id = "s1"
	m, _ := promptModel(domain.Session{ID: id, IssueKey: "AIM-9", Name: "impl", RemoteHost: "regent0"})
	sent := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	m.applyPromptSent(promptSentMsg{
		host:      "regent0",
		sessionID: id,
		sentAt:    sent,
		steer:     server.SteerStatus{Queued: 1},
	})
	if plain := ansi.Strip(m.renderMainView()); !strings.Contains(plain, "queued") {
		t.Fatalf("main view missing queued:\n%s", plain)
	}

	zero := 0
	m.applySteerEvent(server.SessionEvent{
		Type:        "steer",
		ID:          id,
		Queued:      &zero,
		DeliveredAt: sent.Add(time.Second).Format(time.RFC3339Nano),
	})
	if plain := ansi.Strip(m.renderMainView()); !strings.Contains(plain, "delivered") {
		t.Fatalf("main view missing delivered:\n%s", plain)
	}
}

func TestHintLineByFocus(t *testing.T) {
	m := &Model{version: "v9"}
	cases := []struct {
		focus a1Focus
		want  string
	}{
		{focusList, "ctrl+p reply · ctrl+t to-dos · a attach · ? keys"},
		{focusPrompt, "enter send · esc back"},
		{focusTodos, "a add · e edit · space done · s start · d drop · x delete · J/K move · esc back"},
	}
	for _, c := range cases {
		m.focus = c.focus
		got := ansi.Strip(m.hintLine(100))
		if !strings.Contains(got, c.want) {
			t.Errorf("focus %v: %q", c.focus, got)
		}
		if !strings.Contains(got, "v9") {
			t.Errorf("focus %v: version not right-aligned in %q", c.focus, got)
		}
	}
}

func TestCtrlPBlockedWhileCreating(t *testing.T) {
	const id = "s1"
	sess := domain.Session{ID: id, Name: "impl", RemoteHost: "regent0"}
	m, _ := promptModel(sess)
	m.creatingSessions[id] = &creatingSession{placeholder: sess}

	next, _ := m.Update(pressKey("ctrl+p"))
	m = next.(*Model)
	if m.focus != focusList {
		t.Fatalf("focus = %v, want focusList", m.focus)
	}
	const want = "⚠️  This session is still being created — wait for it to finish, or press ctrl+k to cancel."
	if m.snapshotToast != want {
		t.Fatalf("toast = %q", m.snapshotToast)
	}
}

func TestQuestionTogglesKeyHelp(t *testing.T) {
	m, _ := promptModel(domain.Session{ID: "s1", Name: "impl", RemoteHost: "regent0"})
	next, _ := m.Update(pressKey("?"))
	m = next.(*Model)
	plain := ansi.Strip(m.renderMainView())
	if !strings.Contains(plain, "n: new") {
		t.Fatalf("long help missing:\n%s", plain)
	}
	if strings.Contains(plain, "ctrl+p reply") {
		t.Fatalf("hint line still shown:\n%s", plain)
	}
	next, _ = m.Update(pressKey("?"))
	m = next.(*Model)
	plain = ansi.Strip(m.renderMainView())
	if !strings.Contains(plain, "ctrl+p reply") {
		t.Fatalf("hint line not restored:\n%s", plain)
	}
}
