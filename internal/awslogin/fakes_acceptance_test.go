package awslogin_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

// Planted values. Each is recognisable so a leak can be found by substring.
const (
	plantClientSecret = "PLANTED-CLIENT-SECRET-7f3a"
	plantDeviceCode   = "PLANTED-DEVICE-CODE-91bc"
	plantAccessToken  = "PLANTED-ACCESS-TOKEN-55de"
	plantRefreshToken = "PLANTED-REFRESH-TOKEN-0a12"
	plantSecretKey    = "PLANTED-SECRET-ACCESS-KEY-c0ff"
	plantSessionToken = "PLANTED-SESSION-TOKEN-beef"
	plantAccessKeyID  = "ASIAPLANTEDKEYID0001"

	testUserCode = "ABCD-EFGH"
	testRegion   = "eu-west-1"
	testStartURL = "https://example.awsapps.com/start"
	testAccount  = "123456789012"
)

var testStart = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

var allPlanted = []string{plantClientSecret, plantDeviceCode, plantAccessToken, plantRefreshToken, plantSecretKey, plantSessionToken}

// fakeClock is Options.Now and Options.After. With auto set, After advances
// the clock by d and fires at once; otherwise a wait fires on Advance.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	auto    bool
	waits   []time.Duration
	waiters []clockWaiter
}

type clockWaiter struct {
	at time.Time
	ch chan time.Time
}

func newClock(auto bool) *fakeClock { return &fakeClock{now: testStart, auto: auto} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	ch := make(chan time.Time, 1)
	if c.auto {
		c.now = c.now.Add(d)
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, clockWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
			continue
		}
		kept = append(kept, w)
	}
	c.waiters = kept
}

// icResp is one programmed answer: an HTTP status, an AWS error type (sent in
// x-amzn-ErrorType and the body) or a JSON body.
type icResp struct {
	status  int
	errType string
	oauth   string // OAuth error code for CreateToken errors, e.g. authorization_pending
	body    any
	rawBody string
}

type icRequest struct {
	Path   string
	At     time.Time
	Region string
	Form   map[string]string
	Bearer string
	Query  map[string]string
}

// fakeIC is a fake Identity Center serving the four REST calls.
type fakeIC struct {
	t     *testing.T
	srv   *httptest.Server
	clock *fakeClock

	mu          sync.Mutex
	log         []icRequest
	regions     []string
	devCount    int
	interval    int
	expiresIn   int
	regExpires  time.Time
	register    []icResp // consumed in order; default success
	deviceAuth  []icResp // consumed in order; default success
	tokens      []icResp // consumed in order
	tokenDflt   icResp   // when tokens is exhausted
	portal      []icResp // consumed in order; default success
	credExpires time.Time
}

func newFakeIC(t *testing.T, clock *fakeClock) *fakeIC {
	t.Helper()
	ic := &fakeIC{
		t:           t,
		clock:       clock,
		interval:    1,
		expiresIn:   600,
		regExpires:  testStart.Add(90 * 24 * time.Hour),
		tokenDflt:   icResp{status: http.StatusBadRequest, errType: "AuthorizationPendingException", oauth: "authorization_pending"},
		credExpires: testStart.Add(12 * time.Hour),
	}
	ic.srv = httptest.NewServer(http.HandlerFunc(ic.serve))
	t.Cleanup(ic.srv.Close)
	return ic
}

func tokenOK() icResp {
	return icResp{status: http.StatusOK, body: map[string]any{
		"accessToken":  plantAccessToken,
		"refreshToken": plantRefreshToken,
		"tokenType":    "Bearer",
		"expiresIn":    28800,
	}}
}

func tokenErr(errType, oauth string) icResp {
	return icResp{status: http.StatusBadRequest, errType: errType, oauth: oauth}
}

func pop(q *[]icResp) (icResp, bool) {
	if len(*q) == 0 {
		return icResp{}, false
	}
	r := (*q)[0]
	*q = (*q)[1:]
	return r, true
}

func (ic *fakeIC) serve(w http.ResponseWriter, r *http.Request) {
	req := icRequest{Path: r.URL.Path, At: ic.clock.Now(), Form: map[string]string{}, Query: map[string]string{}, Bearer: r.Header.Get("x-amz-sso_bearer_token")}
	if r.Body != nil {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		for k, v := range m {
			req.Form[k] = fmt.Sprint(v)
		}
	}
	for k := range r.URL.Query() {
		req.Query[k] = r.URL.Query().Get(k)
	}
	ic.mu.Lock()
	ic.log = append(ic.log, req)
	var resp icResp
	var ok bool
	switch r.URL.Path {
	case "/client/register":
		if resp, ok = pop(&ic.register); !ok {
			resp = icResp{status: http.StatusOK, body: map[string]any{
				"clientId":              "client-id-1",
				"clientSecret":          plantClientSecret,
				"clientIdIssuedAt":      testStart.Unix(),
				"clientSecretExpiresAt": ic.regExpires.Unix(),
			}}
		}
	case "/device_authorization":
		if resp, ok = pop(&ic.deviceAuth); !ok {
			ic.devCount++
			resp = icResp{status: http.StatusOK, body: map[string]any{
				"deviceCode":              fmt.Sprintf("%s-%d", plantDeviceCode, ic.devCount),
				"userCode":                testUserCode,
				"verificationUri":         "https://device.sso.eu-west-1.amazonaws.com/",
				"verificationUriComplete": "https://device.sso.eu-west-1.amazonaws.com/?user_code=" + testUserCode,
				"expiresIn":               ic.expiresIn,
				"interval":                ic.interval,
			}}
		}
	case "/token":
		if resp, ok = pop(&ic.tokens); !ok {
			resp = ic.tokenDflt
		}
	case "/federation/credentials":
		if resp, ok = pop(&ic.portal); !ok {
			resp = icResp{status: http.StatusOK, body: map[string]any{"roleCredentials": map[string]any{
				"accessKeyId":     plantAccessKeyID,
				"secretAccessKey": plantSecretKey,
				"sessionToken":    plantSessionToken,
				"expiration":      ic.credExpires.UnixMilli(),
			}}}
		}
	default:
		resp = icResp{status: http.StatusNotFound, errType: "UnknownOperationException"}
	}
	ic.mu.Unlock()
	writeResp(w, resp)
}

func writeResp(w http.ResponseWriter, resp icResp) {
	w.Header().Set("Content-Type", "application/json")
	if resp.errType != "" {
		w.Header().Set("x-amzn-ErrorType", resp.errType+":http://internal.amazon.com/coral/com.amazonaws.ssooidc/")
	}
	w.WriteHeader(resp.status)
	switch {
	case resp.rawBody != "":
		_, _ = w.Write([]byte(resp.rawBody))
	case resp.body != nil:
		_ = json.NewEncoder(w).Encode(resp.body)
	case resp.errType != "":
		body := map[string]any{"__type": resp.errType, "message": "error from fake"}
		if resp.oauth != "" {
			body["error"] = resp.oauth
			body["error_description"] = "error from fake"
		}
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (ic *fakeIC) requests(path string) []icRequest {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	var out []icRequest
	for _, r := range ic.log {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (ic *fakeIC) total() int {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return len(ic.log)
}

func (ic *fakeIC) askedRegions() []string {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return append([]string(nil), ic.regions...)
}

func (ic *fakeIC) endpoints() awslogin.Endpoints {
	base := func(region string) string {
		ic.mu.Lock()
		ic.regions = append(ic.regions, region)
		ic.mu.Unlock()
		return ic.srv.URL
	}
	return awslogin.Endpoints{OIDC: base, Portal: base}
}

type writeCall struct {
	profile string
	creds   awsdelegation.SessionCredentials
}

type fakeWriter struct {
	mu    sync.Mutex
	calls []writeCall
	err   error
}

func (f *fakeWriter) WriteHostCredentials(profile string, creds *awsdelegation.SessionCredentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, writeCall{profile: profile, creds: *creds})
	return f.err
}

func (f *fakeWriter) written() []writeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writeCall(nil), f.calls...)
}

// syncBuffer is a log sink safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type rig struct {
	m      *awslogin.Manager
	ic     *fakeIC
	clock  *fakeClock
	writer *fakeWriter
	home   string
}

func newRig(t *testing.T, auto bool, tweak func(*awslogin.Options)) *rig {
	t.Helper()
	clock := newClock(auto)
	ic := newFakeIC(t, clock)
	r := &rig{ic: ic, clock: clock, writer: &fakeWriter{}, home: t.TempDir()}
	o := awslogin.Options{
		Home:      r.home,
		Endpoints: ic.endpoints(),
		Writer:    r.writer,
		Now:       clock.Now,
		After:     clock.After,
		HTTP:      ic.srv.Client(),
	}
	if tweak != nil {
		tweak(&o)
	}
	r.m = awslogin.New(o)
	t.Cleanup(r.m.Close)
	return r
}

func delegation(profile string) awslogin.Delegation {
	return awslogin.Delegation{Profile: profile, StartURL: testStartURL, SSORegion: testRegion, AccountID: testAccount}
}

func isEnd(state string) bool {
	switch state {
	case awslogin.StateDone, awslogin.StateDenied, awslogin.StateExpired, awslogin.StateFailed:
		return true
	}
	return false
}

// waitEnd polls Status until the login reaches an end state, in real time.
func waitEnd(t *testing.T, m *awslogin.Manager, id string) awslogin.Login {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last awslogin.Login
	for time.Now().Before(deadline) {
		l, ok := m.Status(id)
		if !ok {
			t.Fatalf("Status(%q) not found while waiting for an end state (last %+v)", id, last)
		}
		last = l
		if isEnd(l.State) {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("login %q never ended; last state %q", id, last.State)
	return last
}

func mustStart(t *testing.T, m *awslogin.Manager, d awslogin.Delegation) awslogin.Login {
	t.Helper()
	l, err := m.Start(t.Context(), d)
	if err != nil {
		t.Fatalf("Start(%s): %v", d.Profile, err)
	}
	if l.State != awslogin.StatePending {
		t.Fatalf("Start(%s) state = %q, want pending", d.Profile, l.State)
	}
	return l
}
