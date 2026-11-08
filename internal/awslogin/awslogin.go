// Package awslogin runs the AWS Identity Center device flow in process for
// serve: client registration, device authorization, token polling, role
// credentials and the host credentials write. No aws CLI child is started, and
// the client secret, device code and tokens never leave memory.
//
// Design: docs/specs/phone-aws-login.md (piece P3).
package awslogin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

// DefaultRoleName is the permission set read when Delegation.RoleName is empty.
const DefaultRoleName = "aiman-delegated"

const (
	// maxFlow caps one login's polling, whatever expires_in says.
	maxFlow = 10 * time.Minute
	// retention is how long a finished login stays readable.
	retention = 10 * time.Minute
	// startWindow is the period StartsPer10m counts over.
	startWindow = 10 * time.Minute
	// httpTimeout bounds one Identity Center call.
	httpTimeout = 30 * time.Second
)

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
	// ErrNoSSO: the delegation lacks StartURL, SSORegion or AccountID, or its
	// region is not a region name.
	ErrNoSSO = errors.New("awslogin: delegation has no Identity Center settings")

	errClosed = errors.New("awslogin: manager closed")
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
// for the operator. ExpiresAt and ExpiresIn come from the device authorization,
// capped at 10 minutes: they say when polling stops.
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

// regionName keeps a configured region from steering requests to another host,
// because the default endpoints build a hostname from it.
var regionName = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

type record struct {
	login    Login
	finished time.Time
}

// Manager owns the logins of one serve process.
type Manager struct {
	o      Options
	http   *http.Client
	ctx    context.Context // the lifetime of every poll, ended by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// startSlot serialises Start, so a same-profile start that arrives during
	// another's network calls gets that login rather than a second one.
	startSlot chan struct{}
	regMu     sync.Mutex // guards the registration file

	mu     sync.Mutex
	logins map[string]*record
	starts []time.Time
}

// New returns a Manager.
func New(o Options) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.MaxPending <= 0 {
		o.MaxPending = 3
	}
	if o.StartsPer10m <= 0 {
		o.StartsPer10m = 6
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		o:         o,
		http:      noRedirectClient(o.HTTP),
		ctx:       ctx,
		cancel:    cancel,
		startSlot: make(chan struct{}, 1),
		logins:    map[string]*record{},
	}
}

// Start begins a device-flow login for d and returns it pending. A profile
// with a pending login gets that login back without counting against limits.
func (m *Manager) Start(ctx context.Context, d Delegation) (Login, error) {
	if d.StartURL == "" || d.AccountID == "" || !regionName.MatchString(d.SSORegion) {
		return Login{}, ErrNoSSO
	}
	if d.RoleName == "" {
		d.RoleName = DefaultRoleName
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(m.ctx, cancel)()

	select {
	case m.startSlot <- struct{}{}:
		defer func() { <-m.startSlot }()
	case <-ctx.Done():
		return Login{}, ctx.Err()
	}
	if l, ok, err := m.admit(d.Profile); ok || err != nil {
		return l, err
	}
	reg, auth, err := m.authorize(ctx, d)
	if err != nil {
		return Login{}, err
	}
	return m.launch(d, reg, auth)
}

// admit returns the profile's in-flight login, or takes a place in the limits.
func (m *Manager) admit(profile string) (Login, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.o.Now()
	m.pruneLocked(now)
	inFlight := 0
	for _, r := range m.logins {
		if ended(r.login.State) {
			continue
		}
		if r.login.Profile == profile {
			return r.login, true, nil
		}
		inFlight++
	}
	recent := m.starts[:0]
	for _, at := range m.starts {
		if now.Sub(at) < startWindow {
			recent = append(recent, at)
		}
	}
	m.starts = recent
	if inFlight >= m.o.MaxPending || len(m.starts) >= m.o.StartsPer10m {
		return Login{}, false, ErrRateLimited
	}
	m.starts = append(m.starts, now)
	return Login{}, false, nil
}

// launch records the pending login and starts its poll. The login is copied
// before the poll runs, so the caller always sees it pending.
func (m *Manager) launch(d Delegation, reg registration, auth deviceAuthorization) (Login, error) {
	expiresIn := auth.ExpiresIn
	if limit := int(maxFlow / time.Second); expiresIn <= 0 || expiresIn > limit {
		expiresIn = limit
	}
	now := m.o.Now()
	l := Login{
		ID:                      newID(),
		Profile:                 d.Profile,
		State:                   StatePending,
		UserCode:                auth.UserCode,
		VerificationURIComplete: auth.VerificationURIComplete,
		ExpiresAt:               now.Add(time.Duration(expiresIn) * time.Second),
		ExpiresIn:               expiresIn,
	}
	f := &flow{m: m, id: l.ID, d: d, reg: reg, deviceCode: auth.DeviceCode, interval: pollInterval(auth.Interval), deadline: l.ExpiresAt}

	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return Login{}, errClosed
	}
	m.logins[l.ID] = &record{login: l}
	m.wg.Add(1)
	m.mu.Unlock()

	log.Printf("awslogin: login %s %s", l.ID, l.State)
	go f.run(m.ctx)
	return l, nil
}

// Status returns the login with id; false when unknown or finished more than
// 10 minutes ago.
func (m *Manager) Status(id string) (Login, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.o.Now())
	r, ok := m.logins[id]
	if !ok {
		return Login{}, false
	}
	return r.login, true
}

// Pending counts logins still in pending, approved or minting. A nil Manager
// has none.
func (m *Manager) Pending() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.logins {
		switch r.login.State {
		case StatePending, StateApproved, StateMinting:
			n++
		}
	}
	return n
}

// Close stops every poll and waits for them. A login still running ends failed.
func (m *Manager) Close() {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) pruneLocked(now time.Time) {
	for id, r := range m.logins {
		if ended(r.login.State) && now.Sub(r.finished) > retention {
			delete(m.logins, id)
		}
	}
}

func (m *Manager) setState(id, state string) {
	m.mu.Lock()
	if r, ok := m.logins[id]; ok {
		r.login.State = state
	}
	m.mu.Unlock()
	log.Printf("awslogin: login %s %s", id, state)
}

func (m *Manager) finish(id string, out outcome) {
	m.mu.Lock()
	if r, ok := m.logins[id]; ok {
		r.login.State = out.state
		r.login.Failure = out.failure
		// The code and URL are only useful while approval is pending.
		r.login.UserCode = ""
		r.login.VerificationURIComplete = ""
		r.finished = m.o.Now()
	}
	m.mu.Unlock()
	switch {
	case out.detail != "":
		log.Printf("awslogin: login %s %s %s (%s)", id, out.state, out.failure, out.detail)
	case out.failure != "":
		log.Printf("awslogin: login %s %s %s", id, out.state, out.failure)
	default:
		log.Printf("awslogin: login %s %s", id, out.state)
	}
}

func ended(state string) bool {
	switch state {
	case StateDone, StateDenied, StateExpired, StateFailed:
		return true
	}
	return false
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b[:])
}
