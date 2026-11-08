// Package selfupdate lets serve install a newer aiman release on its own host.
// The Manager polls GitHub for the latest release, waits until it is ready
// (its checksums.txt exists), defers while serve is busy, and runs the update
// script detached so it survives the restart it causes. Whichever serve
// process is alive when the script ends settles the attempt from the Result
// file the wrapper writes.
package selfupdate

import "time"

// Mode is the host update policy.
type Mode string

const (
	// ModeOff never polls.
	ModeOff Mode = "off"
	// ModeNotify polls and reports a new release once per tag.
	ModeNotify Mode = "notify"
	// ModeAuto polls and installs a ready newer release.
	ModeAuto Mode = "auto"
)

// Policy is the host update configuration.
type Policy struct {
	Mode       Mode
	CheckEvery time.Duration
	// RetryAfter is how long a failed install of one tag waits before the
	// next try.
	RetryAfter time.Duration
}

const (
	DefaultCheckEvery      = 10 * time.Minute
	MinCheckEvery          = time.Minute
	DefaultRetryAfter      = time.Hour
	DefaultFirstProbeDelay = 2 * time.Minute
	DefaultDeferRetry      = 30 * time.Second
	// ResultWait bounds how long a started attempt may go without a Result
	// before it counts as failed (when its wrapper is gone).
	ResultWait = 3 * time.Minute
	// MaxDefer is how long the gate honours every Busy counter for one tag;
	// after it only Busy[0], the long calls, still defers.
	MaxDefer = 30 * time.Minute
)

// mode returns the effective mode: empty is auto, an unknown value is notify.
func (p Policy) mode() Mode {
	switch p.Mode {
	case "":
		return ModeAuto
	case ModeOff, ModeNotify, ModeAuto:
		return p.Mode
	default:
		return ModeNotify
	}
}

func (p Policy) checkEvery() time.Duration {
	switch {
	case p.CheckEvery <= 0:
		return DefaultCheckEvery
	case p.CheckEvery < MinCheckEvery:
		return MinCheckEvery
	default:
		return p.CheckEvery
	}
}

func (p Policy) retryAfter() time.Duration {
	if p.RetryAfter <= 0 {
		return DefaultRetryAfter
	}
	return p.RetryAfter
}
