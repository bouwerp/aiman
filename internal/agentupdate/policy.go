package agentupdate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mode is the agents.update.mode setting.
type Mode string

// Update modes. Off disables guards, probes and notifications; notify probes
// and notifies; auto also applies updates inside the window.
const (
	ModeOff    Mode = "off"
	ModeNotify Mode = "notify"
	ModeAuto   Mode = "auto"
)

// Defaults for an unset agents.update block.
const (
	DefaultMode   = ModeAuto
	DefaultWindow = "03:00-06:00"
)

// ParseMode reads a mode; empty is DefaultMode.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return DefaultMode, nil
	case ModeOff, ModeNotify, ModeAuto:
		return m, nil
	default:
		return "", fmt.Errorf("agents.update.mode %q: want off, notify or auto", s)
	}
}

// Window is a daily host-local time range [start, end). When end is before
// start the window runs across midnight.
type Window struct {
	start, end int // minutes after midnight
}

var windowRe = regexp.MustCompile(`^(\d{2}):(\d{2})-(\d{2}):(\d{2})$`)

// ParseWindow reads "HH:MM-HH:MM".
func ParseWindow(s string) (Window, error) {
	m := windowRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Window{}, fmt.Errorf("agents.update.window %q: want HH:MM-HH:MM", s)
	}
	start, err := clock(m[1], m[2])
	if err != nil {
		return Window{}, fmt.Errorf("agents.update.window %q: %w", s, err)
	}
	end, err := clock(m[3], m[4])
	if err != nil {
		return Window{}, fmt.Errorf("agents.update.window %q: %w", s, err)
	}
	if start == end {
		return Window{}, fmt.Errorf("agents.update.window %q: start equals end", s)
	}
	return Window{start: start, end: end}, nil
}

func clock(hh, mm string) (int, error) {
	h, _ := strconv.Atoi(hh)
	m, _ := strconv.Atoi(mm)
	if h > 23 || m > 59 {
		return 0, fmt.Errorf("%s:%s is not a time of day", hh, mm)
	}
	return h*60 + m, nil
}

// Contains reports whether t's wall-clock time falls inside the window. The
// caller passes host-local time.
func (w Window) Contains(t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return m >= w.start && m < w.end
	}
	return m >= w.start || m < w.end
}

// String renders the window as "HH:MM-HH:MM".
func (w Window) String() string {
	return fmt.Sprintf("%02d:%02d-%02d:%02d", w.start/60, w.start%60, w.end/60, w.end%60)
}

// Policy is the parsed agents.update block.
type Policy struct {
	Mode   Mode
	Window Window
	// Hold lists agents that are never updated automatically. Manual
	// updates still run.
	Hold []string
}

// NewPolicy parses the config values; empty mode and window take the defaults.
func NewPolicy(mode, window string, hold []string) (Policy, error) {
	m, err := ParseMode(mode)
	if err != nil {
		return Policy{}, err
	}
	if strings.TrimSpace(window) == "" {
		window = DefaultWindow
	}
	w, err := ParseWindow(window)
	if err != nil {
		return Policy{}, err
	}
	return Policy{Mode: m, Window: w, Hold: append([]string(nil), hold...)}, nil
}

// Probes reports whether serve checks versions at all.
func (p Policy) Probes() bool { return p.Mode != ModeOff }

// Notifies reports whether a new version raises a notification.
func (p Policy) Notifies() bool { return p.Mode != ModeOff }

// Held reports whether agent is on the hold list.
func (p Policy) Held(agent string) bool {
	for _, h := range p.Hold {
		if h == agent {
			return true
		}
	}
	return false
}

// AutoApply reports whether serve may apply an update to agent at now,
// before the session gate is consulted.
func (p Policy) AutoApply(agent string, now time.Time) bool {
	return p.Mode == ModeAuto && !p.Held(agent) && p.Window.Contains(now)
}
