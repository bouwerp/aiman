package ui

import (
	"errors"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

func fitModel(t *testing.T, cols, rows int) *Model {
	t.Helper()
	m := &Model{panelMode: panelModePreview}
	m.viewport.SetWidth(cols)
	m.viewport.SetHeight(rows)
	return m
}

var fitSession = domain.Session{ID: "sess-1", TmuxSession: "demo"}

// TestSchedulePreviewFitArmsOnce is the bug this design exists to avoid: the
// poll ticker runs faster than the debounce, so re-arming on every tick would
// bump the generation each time and the pending timer would never fire. Only the
// first tick arms; later ones update the target in place.
func TestSchedulePreviewFitArmsOnce(t *testing.T) {
	m := fitModel(t, 154, 40)

	if cmd := m.schedulePreviewFit(fitSession); cmd == nil {
		t.Fatal("first call should arm the debounce")
	}
	gen := m.fitGen
	if !m.fitArmed {
		t.Fatal("expected the timer to be marked armed")
	}

	// Several more polls arrive before the timer lands.
	for i := 0; i < 5; i++ {
		if cmd := m.schedulePreviewFit(fitSession); cmd != nil {
			t.Fatalf("call %d re-armed the timer; the pending one would be orphaned", i+2)
		}
	}
	if m.fitGen != gen {
		t.Fatalf("generation moved from %d to %d, invalidating the armed timer", gen, m.fitGen)
	}

	// The armed timer lands and issues the resize.
	if cmd := m.applyPreviewFitTick(previewFitTickMsg{gen: gen}); cmd == nil {
		t.Fatal("the armed timer should issue a resize")
	}
	if m.fitArmed {
		t.Error("timer should no longer be armed after firing")
	}
}

// While the size is still changing the pending target must track the latest
// value, so a window drag ends up applying the size it settled on.
func TestSchedulePreviewFitTracksLatestSize(t *testing.T) {
	m := fitModel(t, 154, 40)
	m.schedulePreviewFit(fitSession)

	m.viewport.SetWidth(120)
	m.schedulePreviewFit(fitSession)
	m.viewport.SetWidth(200)
	m.schedulePreviewFit(fitSession)

	if m.fitPending == nil {
		t.Fatal("expected a pending fit")
	}
	if m.fitPending.cols != 200 {
		t.Fatalf("pending cols = %d, want the latest value 200", m.fitPending.cols)
	}
}

// A superseded timer must not act; otherwise a stale size could be applied
// after a newer one.
func TestApplyPreviewFitTickIgnoresSupersededGeneration(t *testing.T) {
	m := fitModel(t, 154, 40)
	m.schedulePreviewFit(fitSession)
	stale := m.fitGen - 1

	if cmd := m.applyPreviewFitTick(previewFitTickMsg{gen: stale}); cmd != nil {
		t.Fatal("a superseded timer must not issue a resize")
	}
	if !m.fitArmed {
		t.Error("a superseded tick must not disarm the live timer")
	}
}

// Once a size is known to be in place, nothing should be scheduled for it again.
func TestSchedulePreviewFitSkipsSizeAlreadyApplied(t *testing.T) {
	m := fitModel(t, 154, 40)
	want, ok := m.desiredPreviewFit(fitSession)
	if !ok {
		t.Fatal("expected a desired fit")
	}
	m.fitApplied = map[string]string{fitSession.ID: want.size()}

	if cmd := m.schedulePreviewFit(fitSession); cmd != nil {
		t.Fatal("no work should be scheduled for a size already in place")
	}

	// A change in panel size is new work again.
	m.viewport.SetWidth(200)
	if cmd := m.schedulePreviewFit(fitSession); cmd == nil {
		t.Fatal("a new size should schedule a fit")
	}
}

// A fit that could not be applied — someone is attached — must back off rather
// than being retried on every poll.
func TestPreviewFitBacksOffWhenNotApplied(t *testing.T) {
	m := fitModel(t, 154, 40)
	m.applyPreviewFitDone(previewFitDoneMsg{sessionID: fitSession.ID, size: "154x40", applied: false})

	if cmd := m.schedulePreviewFit(fitSession); cmd != nil {
		t.Fatal("a declined fit must not be retried immediately")
	}
	if until, ok := m.fitBackoff[fitSession.ID]; !ok || time.Until(until) <= 0 {
		t.Fatal("expected a future backoff deadline")
	}

	// Once the backoff expires it is tried again — a client that was attached
	// may have detached.
	m.fitBackoff[fitSession.ID] = time.Now().Add(-time.Second)
	if cmd := m.schedulePreviewFit(fitSession); cmd == nil {
		t.Fatal("after the backoff expires the fit should be retried")
	}
}

func TestPreviewFitDoneRecordsSuccessAndClearsBackoff(t *testing.T) {
	m := fitModel(t, 154, 40)
	m.applyPreviewFitDone(previewFitDoneMsg{sessionID: fitSession.ID, size: "154x40", applied: false})
	m.applyPreviewFitDone(previewFitDoneMsg{sessionID: fitSession.ID, size: "154x40", applied: true})

	if m.fitApplied[fitSession.ID] != "154x40" {
		t.Errorf("applied size not recorded: %q", m.fitApplied[fitSession.ID])
	}
	if _, backing := m.fitBackoff[fitSession.ID]; backing {
		t.Error("a successful fit should clear the backoff")
	}
}

func TestPreviewFitDoneBacksOffOnError(t *testing.T) {
	m := fitModel(t, 154, 40)
	m.applyPreviewFitDone(previewFitDoneMsg{
		sessionID: fitSession.ID, size: "154x40", err: errors.New("ssh died"),
	})
	if _, backing := m.fitBackoff[fitSession.ID]; !backing {
		t.Error("an error should back off rather than retry every poll")
	}
	if m.fitApplied[fitSession.ID] != "" {
		t.Error("a failed fit must not be recorded as applied")
	}
}

// The floors exist because agent TUIs assume a classic terminal; a panel
// narrower than that would leave them unusable, so the panel scrolls instead.
func TestDesiredPreviewFitAppliesFloors(t *testing.T) {
	m := fitModel(t, 40, 8)
	want, ok := m.desiredPreviewFit(fitSession)
	if !ok {
		t.Fatal("expected a desired fit")
	}
	if want.cols != usecase.MinTerminalCols || want.rows != usecase.MinTerminalRows {
		t.Fatalf("got %dx%d, want the floors %dx%d",
			want.cols, want.rows, usecase.MinTerminalCols, usecase.MinTerminalRows)
	}
}

// Nothing to fit when the preview is not what is on screen, or before the
// panel has been sized.
func TestDesiredPreviewFitRequiresASizedPreview(t *testing.T) {
	m := fitModel(t, 154, 40)
	m.panelMode = panelModeTerminal
	if _, ok := m.desiredPreviewFit(fitSession); ok {
		t.Error("no fit should be wanted while the terminal panel is shown")
	}

	unsized := &Model{panelMode: panelModePreview}
	if _, ok := unsized.desiredPreviewFit(fitSession); ok {
		t.Error("no fit should be wanted before the panel has a size")
	}
}
