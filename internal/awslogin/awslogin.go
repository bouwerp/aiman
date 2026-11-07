// Package awslogin runs the AWS Identity Center device flow in process for
// serve: client registration, device authorization, token polling, role
// credentials and the host credentials write. No aws CLI child is started, and
// the client secret, device code and tokens never leave memory.
//
// Design: docs/specs/phone-aws-login.md (piece P3).
package awslogin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

// DefaultRoleName is the permission set read when Delegation.RoleName is empty.
const DefaultRoleName = "aiman-delegated"

// Login states.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateMinting  = "minting"
	StateDone     = "done"
	StateDenied   = "denied"
	StateExpired  = "expired"
	StateFailed   = "failed"
)

// Failure codes, set only with StateFailed.
const (
	FailureSSORoleDenied = "sso_role_denied"
	FailureWriteFailed   = "write_failed"
	FailureOther         = "other"
)

var (
	// ErrRateLimited: too many pending logins or too many starts in 10 minutes.
	ErrRateLimited = errors.New("awslogin: rate limited")
	// ErrNoSSO: the delegation lacks StartURL, SSORegion or AccountID.
	ErrNoSSO = errors.New("awslogin: delegation has no Identity Center settings")
)

// Delegation names the host profile to write and the Identity Center facts
// used to mint it. RoleName empty means DefaultRoleName.
type Delegation struct {
	Profile   string
	StartURL  string
	SSORegion string
	AccountID string
	RoleName  string
}

// Writer merges credentials into the host credentials file under profile.
type Writer interface {
	WriteHostCredentials(profile string, creds *awsdelegation.SessionCredentials) error
}

// Endpoints return base URLs for a region. A nil func uses the default:
// https://oidc.<region>.amazonaws.com and https://portal.sso.<region>.amazonaws.com.
type Endpoints struct {
	OIDC   func(region string) string
	Portal func(region string) string
}

// Options configure a Manager. Zero values take defaults: Now time.Now, After
// time.After, HTTP a client with a 30s timeout, MaxPending 3, StartsPer10m 6.
type Options struct {
	// Home is the directory holding .aiman/aws-sso-client.json.
	Home      string
	Endpoints Endpoints
	Writer    Writer
	// Now is the clock for deadlines, expiry, rate windows and retention.
	Now func() time.Time
	// After waits between CreateToken polls and for nothing else; deadlines
	// are checked against Now.
	After        func(d time.Duration) <-chan time.Time
	HTTP         *http.Client
	MaxPending   int
	StartsPer10m int
}

// Login is one device-flow sign-in. UserCode and VerificationURIComplete are
// for the operator; ExpiresAt and ExpiresIn come from the device authorization.
type Login struct {
	ID                      string
	Profile                 string
	State                   string
	Failure                 string
	UserCode                string
	VerificationURIComplete string
	ExpiresAt               time.Time
	ExpiresIn               int
}

// Manager owns the logins of one serve process.
type Manager struct{}

var errNotImplemented = errors.New("awslogin: not implemented")

// New returns a Manager.
func New(o Options) *Manager {
	_ = o
	return &Manager{}
}

// Start begins a device-flow login for d and returns it pending. A profile
// with a pending login gets that login back without counting against limits.
func (m *Manager) Start(ctx context.Context, d Delegation) (Login, error) {
	_, _ = ctx, d
	return Login{}, errNotImplemented
}

// Status returns the login with id; false when unknown or finished more than
// 10 minutes ago.
func (m *Manager) Status(id string) (Login, bool) {
	_ = id
	return Login{}, false
}

// Close stops every poll.
func (m *Manager) Close() {}
