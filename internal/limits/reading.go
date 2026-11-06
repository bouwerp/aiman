// Package limits reads how much of each agent provider's allowance or balance
// is left on this host. See docs/specs/provider-limits.md.
package limits

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"
)

// State says how far a Reading can be trusted.
type State string

const (
	// StateOK is a reading the source could read in full.
	StateOK State = "ok"
	// StateUnknown is a source that ran and could not read the answer.
	StateUnknown State = "unknown"
	// StateUnavailable is a source that does not apply on this host.
	StateUnavailable State = "unavailable"
)

// Window is one limit period.
type Window struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	// ResetsAt is zero when the provider gives no exact time.
	ResetsAt time.Time `json:"resets_at,omitzero"`
	// ResetsText is a reset shown as printed, for a screen that gives no year or
	// zone. It is never parsed into a guessed time.
	ResetsText string `json:"resets_text,omitempty"`
	// Reset is true once ResetsAt has passed; UsedPercent is then zero.
	Reset bool `json:"reset,omitempty"`
}

// LeftText is what remains of the window, such as "70% left".
func (w Window) LeftText() string {
	left := max(0, math.Round((100-w.UsedPercent)*10)/10)
	return strconv.FormatFloat(left, 'f', -1, 64) + "% left"
}

// Balance is money or credits left.
type Balance struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// Reading is one provider's numbers at one moment.
type Reading struct {
	Provider string    `json:"provider"`
	Plan     string    `json:"plan,omitempty"`
	Windows  []Window  `json:"windows,omitempty"`
	Balances []Balance `json:"balances,omitempty"`
	Spend    string    `json:"spend,omitempty"`
	State    State     `json:"state"`
	// Reason explains a state other than ok. It never carries a key, a header or
	// a response body.
	Reason string `json:"reason,omitempty"`
	// ObservedAt is when the provider said it, not when serve asked.
	ObservedAt time.Time `json:"observed_at,omitzero"`
	Source     string    `json:"source,omitempty"`
}

// Source produces a Reading for one provider.
type Source interface {
	Name() string
	MinAge() time.Duration
	Timeout() time.Duration
	Read(ctx context.Context) Reading
}

// Snapshot is what limits.get returns.
type Snapshot struct {
	Readings   []Reading `json:"readings"`
	Refreshing bool      `json:"refreshing"`
}

// Env is what sources read from the host. Tests replace every field.
type Env struct {
	Home   string
	Now    func() time.Time
	Getenv func(string) string
	// Run runs a command and returns stdout and its exit code. A command that
	// cannot start returns an error matching exec.ErrNotFound when it is missing.
	Run func(ctx context.Context, name string, args ...string) (stdout string, exit int, err error)
	// Transport replaces the HTTP transport. Nil means the real one.
	Transport http.RoundTripper
}
