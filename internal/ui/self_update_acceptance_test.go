package ui

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance tests for tui-self-update (design rev 3, piece P1).

type fakeStaged struct {
	target   string
	applied  *bool
	applyErr error
	discard  *bool
}

func (s fakeStaged) Target() string { return s.target }
func (s fakeStaged) Apply() error   { *s.applied = true; return s.applyErr }
func (s fakeStaged) Discard()       { *s.discard = true }

type fakeUpdater struct {
	mu       sync.Mutex
	staged   int
	tag      string
	stageErr error
	applied  bool
	discard  bool
}

func (f *fakeUpdater) Stage(_ context.Context, tag string, progress func(string)) (Staged, error) {
	f.mu.Lock()
	f.staged++
	f.tag = tag
	f.mu.Unlock()
	progress("download aiman-test.tar.gz")
	progress("verify checksums.txt")
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	return fakeStaged{target: "/home/op/.local/bin/aiman", applied: &f.applied, discard: &f.discard}, nil
}

func (f *fakeUpdater) stages() int { f.mu.Lock(); defer f.mu.Unlock(); return f.staged }

func useUpdater(t *testing.T, u SelfUpdater) {
	t.Helper()
	SetSelfUpdater(u)
	t.Cleanup(func() { SetSelfUpdater(nil) })
}

func selfUpdateModel(t *testing.T) *Model {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("laptop self-update is Unix only")
	}
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.version = "v0.67.0"
	m.hint = &ReleaseHint{Tag: "v0.67.1", Source: "github"}
	m.state = viewStateMain
	m.currentTab = tabSessions
	return m
}

// collect runs cmd and returns every message it yields, batches flattened,
// without applying them. Ticks and waits are not followed.
func collect(cmd tea.Cmd, depth int) []tea.Msg {
	if cmd == nil || depth > 6 {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, collect(c, depth+1)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}

// drive applies msgs and every message the commands they return yield,
// reporting whether the program was asked to quit.
func drive(m *Model, msgs []tea.Msg) (quit bool) {
	for len(msgs) > 0 {
		msg := msgs[0]
		msgs = msgs[1:]
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
			continue
		}
		_, next := m.Update(msg)
		msgs = append(msgs, collect(next, 0)...)
	}
	return quit
}

func press(m *Model, k tea.KeyPressMsg) bool {
	_, cmd := m.Update(k)
	return drive(m, collect(cmd, 0))
}

func screen(m *Model) string { return ansi.Strip(m.View().Content) }

func TestSelfUpdateUOpensThePageOnlyWithAHint(t *testing.T) {
	m := selfUpdateModel(t)
	useUpdater(t, &fakeUpdater{})
	m.hint = nil
	press(m, pressRune('U'))
	if m.state != viewStateMain {
		t.Fatalf("U without a hint changed the view to %v", m.state)
	}
	m.hint = &ReleaseHint{Tag: "v0.67.1", Source: "github"}
	press(m, pressRune('U'))
	if out := screen(m); !strings.Contains(out, "v0.67.0 → v0.67.1") {
		t.Fatalf("page lacks the versions:\n%s", out)
	}
	if !strings.Contains(m.versionSlot(), "U to update") {
		t.Fatalf("hint %q does not name U", m.versionSlot())
	}
}

func TestSelfUpdateInstallsThenQuitsForRestart(t *testing.T) {
	m := selfUpdateModel(t)
	u := &fakeUpdater{}
	useUpdater(t, u)
	press(m, pressRune('U'))
	quit := press(m, pressKey("enter"))
	if u.stages() != 1 || u.tag != "v0.67.1" {
		t.Fatalf("staged %d times, tag %q; want once, v0.67.1", u.stages(), u.tag)
	}
	if !u.applied {
		t.Fatal("staged binary was not applied")
	}
	if !quit || !m.RestartRequested() {
		t.Fatalf("quit=%v restart=%v, want both after a good apply", quit, m.RestartRequested())
	}
	var r Restarter = m
	_ = r
}

func TestSelfUpdateStageFailureKeepsRunning(t *testing.T) {
	m := selfUpdateModel(t)
	u := &fakeUpdater{stageErr: errors.New("checksum mismatch for aiman-test.tar.gz")}
	useUpdater(t, u)
	press(m, pressRune('U'))
	quit := press(m, pressKey("enter"))
	if quit || m.RestartRequested() || u.applied {
		t.Fatalf("quit=%v restart=%v applied=%v after a failed stage", quit, m.RestartRequested(), u.applied)
	}
	if out := screen(m); !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("page lacks the error:\n%s", out)
	}
}

func busyCreate(m *Model) {
	if m.creatingSessions == nil {
		m.creatingSessions = map[string]*creatingSession{}
	}
	m.creatingSessions["pending-1"] = &creatingSession{placeholder: domain.Session{ID: "pending-1", Name: "fix-login"}}
}

func TestSelfUpdateWarnsAboutBusyWorkThenGoesAhead(t *testing.T) {
	m := selfUpdateModel(t)
	u := &fakeUpdater{}
	useUpdater(t, u)
	busyCreate(m)
	if busy := m.selfUpdateBusy(); len(busy) == 0 || !strings.Contains(strings.Join(busy, "\n"), "fix-login") {
		t.Fatalf("selfUpdateBusy() = %q, want the create", busy)
	}
	press(m, pressRune('U'))
	press(m, pressKey("enter"))
	if u.stages() != 0 {
		t.Fatal("staged before the operator confirmed the warning")
	}
	out := screen(m)
	if !strings.Contains(out, "These will be cut by the restart") || !strings.Contains(out, "fix-login") {
		t.Fatalf("page lacks the warning:\n%s", out)
	}
	quit := press(m, pressKey("enter"))
	if u.stages() != 1 || !u.applied || !quit {
		t.Fatalf("stages=%d applied=%v quit=%v after the second enter", u.stages(), u.applied, quit)
	}
}

// Busy work that starts during the download warns again before apply.
func TestSelfUpdateRechecksBusyWorkBeforeApply(t *testing.T) {
	m := selfUpdateModel(t)
	u := &fakeUpdater{}
	useUpdater(t, u)
	press(m, pressRune('U'))
	_, cmd := m.Update(pressKey("enter"))
	staged := collect(cmd, 0)
	busyCreate(m)
	if quit := drive(m, staged); quit || u.applied {
		t.Fatalf("quit=%v applied=%v with new busy work after stage", quit, u.applied)
	}
	if out := screen(m); !strings.Contains(out, "fix-login") {
		t.Fatalf("page lacks the new busy work:\n%s", out)
	}
	if quit := press(m, pressKey("enter")); !quit || !u.applied {
		t.Fatalf("quit=%v applied=%v after confirming", quit, u.applied)
	}
}
