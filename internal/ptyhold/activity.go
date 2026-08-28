package ptyhold

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"time"
)

// activityFlushInterval is the shortest gap between activity.json writes while
// output keeps arriving.
//
// A busy agent produces output many times a second, and the file exists to be
// read by something polling from another process — sub-second precision buys
// nothing and would mean a disk write per frame for the lifetime of the session.
// A changed title is flushed immediately regardless, because that is the edge a
// reader is waiting for.
const activityFlushInterval = time.Second

// activityTracker records what a session's output says about it, and publishes
// that to activity.json.
//
// Deliberately cheap: a title scan over the bytes already in hand and a
// timestamp. Everything expensive about judging a session — replaying the spool
// through an emulator, matching patterns against rendered prose — is what this
// exists to avoid.
type activityTracker struct {
	dir     string
	scanner titleScanner
	modes   modeScanner

	mu           sync.Mutex
	bytes        int64
	lastOutput   time.Time
	title        string
	titleChanged time.Time
	altScreen    bool
	mouse        bool
	lastFlush    time.Time
	dirty        bool
}

func newActivityTracker(dir string) *activityTracker {
	return &activityTracker{dir: dir}
}

// observe folds one chunk of output in, flushing when it is worth doing.
func (a *activityTracker) observe(data []byte) {
	titles := a.scanner.Feed(data)
	now := time.Now()

	a.mu.Lock()
	a.modes.Feed(data)
	alt, mouse := a.modes.Modes()
	// A mode change is what attach is waiting for, so it flushes like a title
	// change rather than waiting out the interval.
	modesMoved := alt != a.altScreen || mouse != a.mouse
	a.altScreen, a.mouse = alt, mouse
	a.bytes += int64(len(data))
	a.lastOutput = now
	a.dirty = true
	titleMoved := false
	for _, title := range titles {
		if title != a.title {
			a.title = title
			a.titleChanged = now
			titleMoved = true
		}
	}
	due := titleMoved || modesMoved || now.Sub(a.lastFlush) >= activityFlushInterval
	if !due {
		a.mu.Unlock()
		return
	}
	a.lastFlush = now
	snapshot := a.snapshotLocked()
	a.dirty = false
	a.mu.Unlock()

	a.write(snapshot)
}

// flush writes the current state if anything has changed since the last write,
// so a session that goes quiet still leaves an accurate final timestamp.
func (a *activityTracker) flush() {
	a.mu.Lock()
	if !a.dirty {
		a.mu.Unlock()
		return
	}
	a.lastFlush = time.Now()
	snapshot := a.snapshotLocked()
	a.dirty = false
	a.mu.Unlock()

	a.write(snapshot)
}

func (a *activityTracker) snapshotLocked() Activity {
	// Nanosecond precision: this is a sub-second signal — an agent changes its
	// title several times a second — and second-granularity stamps could not
	// tell two frames apart. RFC3339 parsing accepts the fraction, so readers
	// need no special case.
	act := Activity{Bytes: a.bytes, Title: a.title, AltScreen: a.altScreen, Mouse: a.mouse}
	if !a.lastOutput.IsZero() {
		act.LastOutput = a.lastOutput.UTC().Format(time.RFC3339Nano)
	}
	if !a.titleChanged.IsZero() {
		act.TitleChanged = a.titleChanged.UTC().Format(time.RFC3339Nano)
	}
	return act
}

func (a *activityTracker) write(act Activity) {
	raw, err := json.Marshal(act)
	if err != nil {
		return
	}
	// Best effort throughout: the holder's job is to own a terminal, and losing
	// an activity write must never disturb that.
	_ = writeFileAtomic(filepath.Join(a.dir, ActivityFile), raw)
}

// ReadActivity returns a session's published activity. A missing or unreadable
// file is an empty Activity, which callers read as "nothing known".
func ReadActivity(root, id string) Activity {
	var act Activity
	raw, err := readSmallFile(filepath.Join(Dir(root, id), ActivityFile))
	if err != nil {
		return act
	}
	if err := json.Unmarshal([]byte(raw), &act); err != nil {
		return Activity{}
	}
	return act
}

// LastOutputAt parses LastOutput, reporting whether it was usable.
func (a Activity) LastOutputAt() (time.Time, bool) { return parseActivityTime(a.LastOutput) }

// TitleChangedAt parses TitleChanged, reporting whether it was usable.
func (a Activity) TitleChangedAt() (time.Time, bool) { return parseActivityTime(a.TitleChanged) }

func parseActivityTime(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
