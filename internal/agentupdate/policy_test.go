package agentupdate

import (
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, time.Local) }

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeAuto, "auto": ModeAuto, "notify": ModeNotify, "off": ModeOff, "OFF": ModeOff} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q %v", in, got, err)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Error("expected error")
	}
}

func TestWindowSameDay(t *testing.T) {
	w, err := ParseWindow("03:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(2, 59), false}, {at(3, 0), true}, {at(5, 59), true}, {at(6, 0), false}, {at(23, 0), false}} {
		if got := w.Contains(c.t); got != c.want {
			t.Errorf("%s: got %v", c.t.Format("15:04"), got)
		}
	}
}

func TestWindowAcrossMidnight(t *testing.T) {
	w, err := ParseWindow("22:30-02:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(22, 29), false}, {at(22, 30), true}, {at(23, 59), true}, {at(0, 0), true}, {at(1, 59), true}, {at(2, 0), false}, {at(12, 0), false}} {
		if got := w.Contains(c.t); got != c.want {
			t.Errorf("%s: got %v", c.t.Format("15:04"), got)
		}
	}
	if w.String() != "22:30-02:00" {
		t.Errorf("string %q", w.String())
	}
}

func TestWindowMalformed(t *testing.T) {
	for _, s := range []string{"", "03:00", "3-6", "03:00-6", "24:00-01:00", "03:60-04:00", "03:00-03:00", "aa:bb-cc:dd", "03:00-06:00-07:00", "-03:00-06:00", "+3:00-06:00"} {
		if _, err := ParseWindow(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}

func TestPolicy(t *testing.T) {
	p, err := NewPolicy("", "", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != ModeAuto || p.Window.String() != DefaultWindow {
		t.Errorf("defaults: %+v", p)
	}
	if !p.Notifies() || !p.Probes() {
		t.Error("auto should notify and probe")
	}
	if !p.AutoApply("claude", at(4, 0)) {
		t.Error("claude inside window should apply")
	}
	if p.AutoApply("claude", at(12, 0)) {
		t.Error("outside window should not apply")
	}
	if p.AutoApply("codex", at(4, 0)) || !p.Held("codex") {
		t.Error("held agent should not auto apply")
	}
	n, _ := NewPolicy("notify", "01:00-02:00", nil)
	if n.AutoApply("claude", at(1, 30)) || !n.Notifies() {
		t.Error("notify mode")
	}
	off, _ := NewPolicy("off", "", nil)
	if off.AutoApply("claude", at(4, 0)) || off.Notifies() || off.Probes() {
		t.Error("off mode")
	}
	if _, err := NewPolicy("auto", "bad", nil); err == nil {
		t.Error("bad window should fail")
	}
	if _, err := NewPolicy("loud", "", nil); err == nil {
		t.Error("bad mode should fail")
	}
}
