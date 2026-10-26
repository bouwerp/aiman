package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

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
