package cgroup

import (
	"fmt"
	"time"
)

// Alert thresholds. A session alerts when it has held at least 90% of its
// MemoryHigh for AlertHold, or when its memory pressure passes 50%. Having
// alerted it stays "over" until it is back under 80% and 25%, and it alerts
// again at most once per AlertEvery while it stays over.
const (
	AlertMemoryRatio    = 0.90
	AlertClearRatio     = 0.80
	AlertPressure       = 50.0
	AlertClearPressure  = 25.0
	AlertHold           = 30 * time.Second
	AlertEvery          = 10 * time.Minute
	alertKeyUserSlice   = "slice:user"
	alertKeySessionsAll = "slice:sessions"
)

// AlertScope says what an alert is about.
type AlertScope string

const (
	AlertSession       AlertScope = "session"
	AlertUserSlice     AlertScope = "user_slice"
	AlertSessionsSlice AlertScope = "sessions_slice"
)

// Alert is one threshold crossing worth telling someone about.
type Alert struct {
	Scope AlertScope
	// SessionID and SessionName are set for a session alert.
	SessionID, SessionName string
	Usage                  Usage
	// Throttled is true when the kernel is already slowing the cgroup
	// (pressure over the threshold), false when it is only near its limit.
	Throttled bool
}

// Text is the alert in one line, without the session's name:
// "5.8G/6G memory, pressure 61%".
func (a Alert) Text() string {
	s := FormatSize(a.Usage.MemoryCurrent) + "/" + FormatSize(a.Usage.MemoryHigh) + " memory"
	if a.Usage.MemoryPressure >= 1 {
		s += fmt.Sprintf(", pressure %.0f%%", a.Usage.MemoryPressure)
	}
	return s
}

type alertState struct {
	over      bool
	overSince time.Time
	lastAlert time.Time
}

// Alerter turns successive snapshots into alerts. It is not safe for
// concurrent use; the sampler's goroutine owns it.
type Alerter struct {
	states map[string]*alertState
}

// NewAlerter returns an Alerter with no history.
func NewAlerter() *Alerter { return &Alerter{states: map[string]*alertState{}} }

// Evaluate returns the alerts the snapshot raises, and forgets sessions
// that are no longer in it.
func (a *Alerter) Evaluate(snap Snapshot, now time.Time) []Alert {
	if !snap.Supported {
		return nil
	}
	var out []Alert
	live := map[string]bool{}
	for _, s := range snap.Sessions {
		live[s.ID] = true
		// A session waits out AlertHold near its limit; pressure means it
		// is being throttled now.
		if throttled, fire := a.step(s.ID, s.Usage, AlertHold, now); fire {
			out = append(out, Alert{Scope: AlertSession, SessionID: s.ID, SessionName: s.Name, Usage: s.Usage, Throttled: throttled})
		}
	}
	slice := func(key string, scope AlertScope, u *Usage) {
		live[key] = true
		if u == nil {
			return
		}
		// A slice alerts on memory alone: its pressure is every session's.
		mem := *u
		mem.MemoryPressure = 0
		if _, fire := a.step(key, mem, 0, now); fire {
			out = append(out, Alert{Scope: scope, Usage: *u})
		}
	}
	slice(alertKeyUserSlice, AlertUserSlice, snap.UserSlice)
	slice(alertKeySessionsAll, AlertSessionsSlice, snap.SessionsSlice)
	for key := range a.states {
		if !live[key] {
			delete(a.states, key)
		}
	}
	return out
}

func (a *Alerter) step(key string, u Usage, hold time.Duration, now time.Time) (throttled, fire bool) {
	st := a.states[key]
	if st == nil {
		st = &alertState{}
		a.states[key] = st
	}
	ratio := 0.0
	if u.MemoryHigh > 0 {
		ratio = float64(u.MemoryCurrent) / float64(u.MemoryHigh)
	}
	throttled = u.MemoryPressure > AlertPressure
	switch {
	case ratio >= AlertMemoryRatio || throttled:
		if !st.over {
			st.over, st.overSince = true, now
		}
	case ratio < AlertClearRatio && u.MemoryPressure < AlertClearPressure:
		st.over = false
	}
	if !st.over {
		return throttled, false
	}
	if !throttled && now.Sub(st.overSince) < hold {
		return throttled, false
	}
	if !st.lastAlert.IsZero() && now.Sub(st.lastAlert) < AlertEvery {
		return throttled, false
	}
	st.lastAlert = now
	return throttled, true
}
