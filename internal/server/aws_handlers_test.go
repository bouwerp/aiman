package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// Planted values: none may ever appear in an aws.* response.
const (
	awsPlantedKeyID  = "AKIAPLANTEDKEYID0001"
	awsPlantedSecret = "plantedSecretValueDoNotLeak0123456789abcd"
	awsPlantedToken  = "plantedSessionTokenDoNotLeakZZZ"
	awsSessionCaller = "sess-aws"
	awsPeerPID       = 5151
)

// fakeAWSLogin records Start calls and answers from programmed results.
type fakeAWSLogin struct {
	mu       sync.Mutex
	starts   []awslogin.Delegation
	startErr error
	login    awslogin.Login
	logins   map[string]awslogin.Login
	statuses []string
}

func (f *fakeAWSLogin) Start(_ context.Context, d awslogin.Delegation) (awslogin.Login, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, d)
	if f.startErr != nil {
		return awslogin.Login{}, f.startErr
	}
	l := f.login
	l.Profile = d.Profile
	return l, nil
}

func (f *fakeAWSLogin) Status(id string) (awslogin.Login, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, id)
	l, ok := f.logins[id]
	return l, ok
}

func (f *fakeAWSLogin) startCalls() []awslogin.Delegation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]awslogin.Delegation(nil), f.starts...)
}

// awsDelegations is serve's delegated list for the tests:
//   - dev: full SSO facts, custom permission set, section with a future expiry
//   - old: full SSO facts, default permission set, section with a past expiry
//   - approx: no SSO start URL, section with no recorded expiry, 1h duration
//   - ghost: SSO account id missing, no section on the host
func awsDelegations() config.ServeAWSConfig {
	return config.ServeAWSConfig{Delegations: []config.ServeAWSDelegation{
		{
			Profile: "dev", AccountID: "111111111111", RoleName: "laptop-role",
			DurationSeconds: 43200,
			SSOStartURL:     "https://example.awsapps.com/start", SSORegion: "eu-west-1",
			SSOAccountID: "222222222222", SSORoleName: "aiman-custom",
		},
		{
			Profile: "old", AccountID: "111111111111",
			SSOStartURL: "https://example.awsapps.com/start", SSORegion: "eu-west-1",
			SSOAccountID: "222222222222",
		},
		{
			Profile: "approx", AccountID: "111111111111", DurationSeconds: 3600,
			SSORegion: "eu-west-1", SSOAccountID: "222222222222",
		},
		{
			Profile: "ghost", AccountID: "111111111111",
			SSOStartURL: "https://example.awsapps.com/start", SSORegion: "eu-west-1",
		},
	}}
}

type awsFixture struct {
	srv     *Server
	login   *fakeAWSLogin
	path    string
	future  time.Time
	past    time.Time
	modTime time.Time
}

func awsSection(name, expiry string) string {
	b := "[" + name + "]\n" +
		"aws_access_key_id = " + awsPlantedKeyID + "\n" +
		"aws_secret_access_key = " + awsPlantedSecret + "\n" +
		"aws_session_token = " + awsPlantedToken + "\n"
	if expiry != "" {
		b += "x_security_token_expires = " + expiry + "\n"
	}
	return b + "\n"
}

// newAWSFixture writes a credentials file under a temp HOME and wires the
// server with awsDelegations and a fake engine. Sections: dev (future expiry),
// old (past expiry), approx (none), stray (not delegated, none) and other (not
// delegated, future expiry).
func newAWSFixture(t *testing.T) *awsFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".aws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	f := &awsFixture{
		future:  now.Add(6 * time.Hour),
		past:    now.Add(-3 * time.Hour),
		modTime: now.Add(-2 * time.Hour),
		path:    filepath.Join(dir, "credentials"),
	}
	content := awsSection("dev", f.future.Format(time.RFC3339)) +
		awsSection("old", f.past.Format(time.RFC3339)) +
		awsSection("approx", "") +
		awsSection("stray", "") +
		awsSection("other", f.future.Format(time.RFC3339))
	if err := os.WriteFile(f.path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.path, f.modTime, f.modTime); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := ptyServer(t, todoSession(awsSessionCaller))
	srv.procInSession = func(pid int) bool { return pid == awsPeerPID }
	f.login = &fakeAWSLogin{logins: map[string]awslogin.Login{}}
	srv.SetAWS(awsDelegations(), f.path, f.login)
	f.srv = srv
	return f
}

type awsProfileRow struct {
	Profile      string `json:"profile"`
	Status       string `json:"status"`
	ExpiresAt    string `json:"expires_at"`
	ExpiryApprox bool   `json:"expiry_approx"`
	SSOReady     bool   `json:"sso_ready"`
}

type awsStatusResult struct {
	Type     string          `json:"type"`
	Profiles []awsProfileRow `json:"profiles"`
}

type awsLoginResult struct {
	Type  string                     `json:"type"`
	Login map[string]json.RawMessage `json:"login"`
}

func assertNoPlantedSecret(t *testing.T, resp Response) {
	t.Helper()
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{awsPlantedKeyID, awsPlantedSecret, awsPlantedToken} {
		if strings.Contains(string(b), v) {
			t.Fatalf("response carries a planted credential value: %s", b)
		}
	}
}

func decodeResult(t *testing.T, resp Response, out any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s %q", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func parseExpiry(t *testing.T, row awsProfileRow) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, row.ExpiresAt)
	if err != nil {
		t.Fatalf("%s: expires_at %q is not RFC3339: %v", row.Profile, row.ExpiresAt, err)
	}
	return at
}

func TestAWSStatusRows(t *testing.T) {
	f := newAWSFixture(t)
	resp := rpc(t, f.srv, "", "aws.status", map[string]any{})
	assertNoPlantedSecret(t, resp)
	var got awsStatusResult
	decodeResult(t, resp, &got)
	if got.Type != "aws_status" {
		t.Fatalf("type = %q, want aws_status", got.Type)
	}
	rows := map[string]awsProfileRow{}
	for _, r := range got.Profiles {
		if _, dup := rows[r.Profile]; dup {
			t.Fatalf("profile %q listed twice", r.Profile)
		}
		rows[r.Profile] = r
	}

	approxAt := f.modTime.Add(time.Hour)
	cases := []struct {
		profile  string
		status   string
		expires  *time.Time
		approx   bool
		ssoReady bool
	}{
		{profile: "dev", status: "valid", expires: &f.future, ssoReady: true},
		{profile: "old", status: "expired", expires: &f.past, ssoReady: true},
		{profile: "approx", status: "expired", expires: &approxAt, approx: true},
		{profile: "ghost", status: "not_pushed"},
		{profile: "other", status: "valid", expires: &f.future},
		{profile: "stray", status: "unmanaged"},
	}
	if len(got.Profiles) != len(cases) {
		t.Errorf("got %d rows, want %d: %+v", len(got.Profiles), len(cases), got.Profiles)
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			row, ok := rows[tc.profile]
			if !ok {
				t.Fatalf("no row for %q in %+v", tc.profile, got.Profiles)
			}
			if row.Status != tc.status {
				t.Errorf("status = %q, want %q", row.Status, tc.status)
			}
			if row.ExpiryApprox != tc.approx {
				t.Errorf("expiry_approx = %v, want %v", row.ExpiryApprox, tc.approx)
			}
			if row.SSOReady != tc.ssoReady {
				t.Errorf("sso_ready = %v, want %v", row.SSOReady, tc.ssoReady)
			}
			if tc.expires == nil {
				if row.ExpiresAt != "" {
					t.Errorf("expires_at = %q, want none", row.ExpiresAt)
				}
				return
			}
			at := parseExpiry(t, row)
			if d := at.Sub(*tc.expires); d > time.Second || d < -time.Second {
				t.Errorf("expires_at = %s, want %s", at, tc.expires)
			}
		})
	}
}

func TestAWSLoginStartRefusals(t *testing.T) {
	cases := []struct {
		name     string
		caller   string
		pid      int
		profile  string
		startErr error
		code     string
		reaches  bool
	}{
		{name: "unknown profile", profile: "stray", code: CodeInvalidParams},
		{name: "missing profile", profile: "", code: CodeInvalidParams},
		{name: "no start url", profile: "approx", code: CodeAWSNoSSO},
		{name: "no sso account", profile: "ghost", code: CodeAWSNoSSO},
		{name: "rate limited", profile: "dev", startErr: awslogin.ErrRateLimited, code: CodeAWSRateLimited, reaches: true},
		{name: "session caller", caller: awsSessionCaller, profile: "dev", code: CodeNeedsOperator},
		{name: "session peer", pid: awsPeerPID, profile: "dev", code: CodeNeedsOperator},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAWSFixture(t)
			f.login.startErr = tc.startErr
			resp := scopeCall(t, f.srv, tc.caller, tc.pid, "aws.login_start", map[string]any{"profile": tc.profile})
			assertNoPlantedSecret(t, resp)
			if resp.Error == nil {
				t.Fatalf("want %s, got result %+v", tc.code, resp.Result)
			}
			if resp.Error.Code != tc.code {
				t.Fatalf("code = %s (%q), want %s", resp.Error.Code, resp.Error.Message, tc.code)
			}
			if n := len(f.login.startCalls()); tc.reaches != (n > 0) {
				t.Errorf("engine Start called %d times; want reached=%v", n, tc.reaches)
			}
		})
	}
}

func TestAWSLoginStartReturnsCode(t *testing.T) {
	f := newAWSFixture(t)
	exp := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	f.login.login = awslogin.Login{
		ID: "0123456789abcdef0123456789abcdef", State: awslogin.StatePending,
		UserCode: "ABCD-EFGH", VerificationURIComplete: "https://device.sso.eu-west-1.amazonaws.com/?user_code=ABCD-EFGH",
		ExpiresIn: 600, ExpiresAt: exp,
	}
	resp := rpc(t, f.srv, "", "aws.login_start", map[string]any{"profile": "dev"})
	assertNoPlantedSecret(t, resp)
	var got awsLoginResult
	decodeResult(t, resp, &got)
	if got.Type != "aws_login" {
		t.Fatalf("type = %q, want aws_login", got.Type)
	}
	wantStr := map[string]string{
		"login_id":                  f.login.login.ID,
		"profile":                   "dev",
		"state":                     awslogin.StatePending,
		"user_code":                 "ABCD-EFGH",
		"verification_uri_complete": f.login.login.VerificationURIComplete,
	}
	for k, want := range wantStr {
		var v string
		if err := json.Unmarshal(got.Login[k], &v); err != nil || v != want {
			t.Errorf("login.%s = %s, want %q", k, got.Login[k], want)
		}
	}
	var expiresIn int
	if err := json.Unmarshal(got.Login["expires_in"], &expiresIn); err != nil || expiresIn != 600 {
		t.Errorf("login.expires_in = %s, want 600", got.Login["expires_in"])
	}
	var at string
	if err := json.Unmarshal(got.Login["expires_at"], &at); err != nil {
		t.Errorf("login.expires_at = %s: %v", got.Login["expires_at"], err)
	} else if parsed, err := time.Parse(time.RFC3339, at); err != nil || !parsed.Equal(exp) {
		t.Errorf("login.expires_at = %q, want %s", at, exp.Format(time.RFC3339))
	}

	starts := f.login.startCalls()
	if len(starts) != 1 {
		t.Fatalf("engine Start called %d times, want 1", len(starts))
	}
	want := awslogin.Delegation{
		Profile: "dev", StartURL: "https://example.awsapps.com/start",
		SSORegion: "eu-west-1", AccountID: "222222222222", RoleName: "aiman-custom",
	}
	if starts[0] != want {
		t.Errorf("Start delegation = %+v, want %+v", starts[0], want)
	}

	// A delegation with no sso_role_name leaves RoleName empty: the engine's default.
	rpcOK(t, f.srv, "", "aws.login_start", map[string]any{"profile": "old"}, nil)
	starts = f.login.startCalls()
	if len(starts) != 2 || starts[1].Profile != "old" || starts[1].RoleName != "" {
		t.Errorf("second Start = %+v, want profile old with empty RoleName", starts)
	}
}

func TestAWSLoginStatusHidesCode(t *testing.T) {
	f := newAWSFixture(t)
	id := "fedcba9876543210fedcba9876543210"
	exp := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	f.login.logins[id] = awslogin.Login{
		ID: id, Profile: "dev", State: awslogin.StateFailed, Failure: awslogin.FailureSSORoleDenied,
		UserCode: "WXYZ-QRST", VerificationURIComplete: "https://device.sso.eu-west-1.amazonaws.com/?user_code=WXYZ-QRST",
		ExpiresIn: 600, ExpiresAt: exp,
	}
	resp := rpc(t, f.srv, "", "aws.login_status", map[string]any{"login_id": id})
	b, _ := json.Marshal(resp)
	for _, leak := range []string{"WXYZ-QRST", "user_code", "verification_uri"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("login_status leaks %q: %s", leak, b)
		}
	}
	var got awsLoginResult
	decodeResult(t, resp, &got)
	if got.Type != "aws_login" {
		t.Fatalf("type = %q, want aws_login", got.Type)
	}
	wantStr := map[string]string{
		"login_id": id,
		"profile":  "dev",
		"state":    awslogin.StateFailed,
		"failure":  awslogin.FailureSSORoleDenied,
	}
	for k, want := range wantStr {
		var v string
		if err := json.Unmarshal(got.Login[k], &v); err != nil || v != want {
			t.Errorf("login.%s = %s, want %q", k, got.Login[k], want)
		}
	}
	var at string
	if err := json.Unmarshal(got.Login["expires_at"], &at); err != nil {
		t.Errorf("login.expires_at = %s: %v", got.Login["expires_at"], err)
	} else if parsed, err := time.Parse(time.RFC3339, at); err != nil || !parsed.Equal(exp) {
		t.Errorf("login.expires_at = %q, want %s", at, exp.Format(time.RFC3339))
	}
}

func TestAWSLoginStatusNotFound(t *testing.T) {
	f := newAWSFixture(t)
	id := "00000000000000000000000000000000"
	resp := rpc(t, f.srv, "", "aws.login_status", map[string]any{"login_id": id})
	if resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("want %s, got %+v / %+v", CodeNotFound, resp.Error, resp.Result)
	}
	f.login.mu.Lock()
	asked := append([]string(nil), f.login.statuses...)
	f.login.mu.Unlock()
	if len(asked) != 1 || asked[0] != id {
		t.Errorf("engine Status asked %v, want [%s]", asked, id)
	}
}

func TestAWSReadMethodsAllowSessionCaller(t *testing.T) {
	f := newAWSFixture(t)
	id := "abababababababababababababababab"
	f.login.logins[id] = awslogin.Login{ID: id, Profile: "dev", State: awslogin.StatePending, UserCode: "LMNO-PQRS"}
	callers := []struct {
		name   string
		caller string
		pid    int
	}{
		{name: "named caller", caller: awsSessionCaller, pid: scopeNoPID},
		{name: "peer in session", pid: awsPeerPID},
	}
	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			resp := scopeCall(t, f.srv, c.caller, c.pid, "aws.status", map[string]any{})
			assertNoPlantedSecret(t, resp)
			var st awsStatusResult
			decodeResult(t, resp, &st)
			if st.Type != "aws_status" || len(st.Profiles) == 0 {
				t.Errorf("aws.status = %+v, want aws_status rows", st)
			}

			resp = scopeCall(t, f.srv, c.caller, c.pid, "aws.login_status", map[string]any{"login_id": id})
			var lg awsLoginResult
			decodeResult(t, resp, &lg)
			var state string
			if err := json.Unmarshal(lg.Login["state"], &state); err != nil || state != awslogin.StatePending {
				t.Errorf("login.state = %s, want pending", lg.Login["state"])
			}
			if b, _ := json.Marshal(resp); strings.Contains(string(b), "LMNO-PQRS") {
				t.Errorf("login_status leaks the user code to a session: %s", b)
			}
		})
	}
}
