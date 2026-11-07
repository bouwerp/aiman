// Package awslogineval is the P7 eval for phone AWS sign-in
// (docs/specs/phone-aws-login.md): an in-process serve wired the way
// cmd/aiman/serve.go's wireAWSLogin wires it, with the real awslogin engine
// pointed at an httptest Identity Center.
package awslogineval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/evals/harness"
	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/server"
)

// Planted values: none may appear in any aws.* response.
const (
	plantKeyID        = "AKIAEVALPLANTED00001"
	plantSecret       = "evalPlantedSecretDoNotLeak0123456789abcd"
	plantToken        = "evalPlantedSessionTokenDoNotLeak"
	plantMintKeyID    = "ASIAEVALMINTED000001"
	plantMintSecret   = "EVAL-MINTED-SECRET-ACCESS-KEY"
	plantMintToken    = "EVAL-MINTED-SESSION-TOKEN"
	plantClientSecret = "EVAL-CLIENT-SECRET"
	plantDeviceCode   = "EVAL-DEVICE-CODE"
	plantAccessToken  = "EVAL-ACCESS-TOKEN"

	evalUserCode = "EVAL-CODE"
	evalRegion   = "eu-west-1"
	evalAccount  = "222222222222"
)

var planted = []string{
	plantKeyID, plantSecret, plantToken, plantMintKeyID, plantMintSecret,
	plantMintToken, plantClientSecret, plantDeviceCode, plantAccessToken,
}

// delegations: sso has all three SSO facts and a section with a future expiry;
// nosso has none and no section.
func delegations() []config.ServeAWSDelegation {
	return []config.ServeAWSDelegation{
		{
			Profile: "sso", AccountID: "111111111111", DurationSeconds: 43200,
			SSOStartURL: "https://eval.awsapps.com/start", SSORegion: evalRegion,
			SSOAccountID: evalAccount, SSORoleName: "aiman-delegated",
		},
		{Profile: "nosso", AccountID: "111111111111", DurationSeconds: 3600},
	}
}

// fakeIC answers the four Identity Center calls: register, device
// authorization, one immediate token, and role credentials.
type fakeIC struct {
	srv     *httptest.Server
	expires time.Time

	mu    sync.Mutex
	paths []string
}

func newFakeIC(t *testing.T) *fakeIC {
	t.Helper()
	ic := &fakeIC{expires: time.Now().UTC().Add(12 * time.Hour).Truncate(time.Second)}
	ic.srv = httptest.NewServer(http.HandlerFunc(ic.serve))
	t.Cleanup(ic.srv.Close)
	return ic
}

func (ic *fakeIC) serve(w http.ResponseWriter, r *http.Request) {
	ic.mu.Lock()
	ic.paths = append(ic.paths, r.URL.Path)
	ic.mu.Unlock()
	now := time.Now().UTC()
	var body any
	switch r.URL.Path {
	case "/client/register":
		body = map[string]any{
			"clientId": "eval-client", "clientSecret": plantClientSecret,
			"clientIdIssuedAt": now.Unix(), "clientSecretExpiresAt": now.Add(90 * 24 * time.Hour).Unix(),
		}
	case "/device_authorization":
		body = map[string]any{
			"deviceCode": plantDeviceCode, "userCode": evalUserCode,
			"verificationUri":         "https://device.sso.eu-west-1.amazonaws.com/",
			"verificationUriComplete": "https://device.sso.eu-west-1.amazonaws.com/?user_code=" + evalUserCode,
			"expiresIn":               600, "interval": 1,
		}
	case "/token":
		body = map[string]any{"accessToken": plantAccessToken, "tokenType": "Bearer", "expiresIn": 28800}
	case "/federation/credentials":
		if r.URL.Query().Get("role_name") != "aiman-delegated" || r.URL.Query().Get("account_id") != evalAccount ||
			r.Header.Get("x-amz-sso_bearer_token") != plantAccessToken {
			w.Header().Set("x-amzn-ErrorType", "ForbiddenException")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body = map[string]any{"roleCredentials": map[string]any{
			"accessKeyId": plantMintKeyID, "secretAccessKey": plantMintSecret,
			"sessionToken": plantMintToken, "expiration": ic.expires.UnixMilli(),
		}}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

type credsWriter struct{ path, lock string }

func (w credsWriter) WriteHostCredentials(profile string, c *awsdelegation.SessionCredentials) error {
	return awsdelegation.WriteHostCredentials(w.path, w.lock, profile, c)
}

type rig struct {
	t      *testing.T
	socket string
	creds  string
	future time.Time
	ic     *fakeIC
}

// newRig starts serve under a temp HOME holding ~/.aws/credentials with one
// section, sso, whose recorded expiry is 6 hours out.
func newRig(t *testing.T) *rig {
	t.Helper()
	home, err := os.MkdirTemp("", "aimanaws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	awsDir := filepath.Join(home, ".aws")
	aimanDir := filepath.Join(home, config.DirName)
	for _, d := range []string{awsDir, aimanDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	creds := filepath.Join(awsDir, "credentials")
	section := "[sso]\n" +
		"aws_access_key_id = " + plantKeyID + "\n" +
		"aws_secret_access_key = " + plantSecret + "\n" +
		"aws_session_token = " + plantToken + "\n" +
		awsdelegation.ExpiryKey + " = " + future.Format(time.RFC3339) + "\n"
	if err := os.WriteFile(creds, []byte(section), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := server.Listen(aimanDir)
	if err != nil {
		t.Skipf("cannot create a unix socket: %v", err)
	}
	repo, err := sqlite.NewRepository(filepath.Join(aimanDir, "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	pty := ptyfake.New()
	srv := server.New(ln, repo, harness.NewFakeRemote(home, pty), harness.FakeCreator{Dir: home, PTY: pty},
		contextstore.NewFiles(filepath.Join(aimanDir, "context")), pty, "eval")

	ic := newFakeIC(t)
	engine := awslogin.New(awslogin.Options{
		Home:      home,
		Endpoints: awslogin.Endpoints{OIDC: func(string) string { return ic.srv.URL }, Portal: func(string) string { return ic.srv.URL }},
		Writer:    credsWriter{path: creds, lock: filepath.Join(aimanDir, "aws-credentials.lock")},
	})
	srv.SetAWS(config.ServeAWSConfig{Delegations: delegations()}, creds, engine)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		engine.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		_ = repo.Close()
	})
	return &rig{t: t, socket: server.SocketPath(aimanDir), creds: creds, future: future, ic: ic}
}

// call sends one request as caller (empty is the operator) and fails the test
// when any planted value appears in the response.
func (r *rig) call(caller, method string, params any) server.Response {
	r.t.Helper()
	old, had := os.LookupEnv("AIMAN_ID")
	if caller != "" {
		_ = os.Setenv("AIMAN_ID", caller)
	}
	resp, err := server.Call(r.socket, method, params)
	if had {
		_ = os.Setenv("AIMAN_ID", old)
	} else {
		_ = os.Unsetenv("AIMAN_ID")
	}
	if err != nil {
		r.t.Fatalf("%s: %v", method, err)
	}
	b, _ := json.Marshal(resp)
	for _, v := range planted {
		if strings.Contains(string(b), v) {
			r.t.Fatalf("%s response carries a planted secret value", method)
		}
	}
	return resp
}

func decode(t *testing.T, resp server.Response, out any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error %s: %s", resp.Error.Code, resp.Error.Message)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func wantCode(t *testing.T, resp server.Response, code string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("want %s, got result %+v", code, resp.Result)
	}
	if resp.Error.Code != code {
		t.Fatalf("code = %s (%q), want %s", resp.Error.Code, resp.Error.Message, code)
	}
}

type statusRow struct {
	Profile   string `json:"profile"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
	SSOReady  bool   `json:"sso_ready"`
}

type loginView struct {
	LoginID                 string `json:"login_id"`
	Profile                 string `json:"profile"`
	State                   string `json:"state"`
	Failure                 string `json:"failure"`
	UserCode                string `json:"user_code"`
	VerificationURIComplete string `json:"verification_uri_complete"`
}

func (r *rig) status() map[string]statusRow {
	r.t.Helper()
	var got struct {
		Type     string      `json:"type"`
		Profiles []statusRow `json:"profiles"`
	}
	decode(r.t, r.call("", "aws.status", map[string]any{}), &got)
	if got.Type != "aws_status" {
		r.t.Fatalf("type = %q, want aws_status", got.Type)
	}
	rows := map[string]statusRow{}
	for _, row := range got.Profiles {
		rows[row.Profile] = row
	}
	return rows
}

func TestEvalAWSLogin(t *testing.T) {
	r := newRig(t)

	t.Run("status rows", func(t *testing.T) {
		rows := r.status()
		if len(rows) != 2 {
			t.Errorf("got %d rows, want 2: %+v", len(rows), rows)
		}
		sso, ok := rows["sso"]
		if !ok {
			t.Fatalf("no row for sso: %+v", rows)
		}
		if sso.Status != "valid" || !sso.SSOReady {
			t.Errorf("sso = %+v, want valid and sso_ready", sso)
		}
		if at, err := time.Parse(time.RFC3339, sso.ExpiresAt); err != nil || !at.Equal(r.future) {
			t.Errorf("sso expires_at = %q, want %s", sso.ExpiresAt, r.future.Format(time.RFC3339))
		}
		nosso, ok := rows["nosso"]
		if !ok {
			t.Fatalf("no row for nosso: %+v", rows)
		}
		if nosso.Status != "not_pushed" || nosso.SSOReady || nosso.ExpiresAt != "" {
			t.Errorf("nosso = %+v, want not_pushed, not sso_ready, no expires_at", nosso)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		wantCode(t, r.call("", "aws.login_start", map[string]any{"profile": "nosso"}), server.CodeAWSNoSSO)
		wantCode(t, r.call("", "aws.login_start", map[string]any{"profile": "nope"}), server.CodeInvalidParams)
		wantCode(t, r.call("sess-eval", "aws.login_start", map[string]any{"profile": "sso"}), server.CodeNeedsOperator)
		r.ic.mu.Lock()
		reached := len(r.ic.paths)
		r.ic.mu.Unlock()
		if reached != 0 {
			t.Errorf("a refused start reached Identity Center %d times", reached)
		}
	})

	t.Run("device flow", func(t *testing.T) {
		var start struct {
			Type  string    `json:"type"`
			Login loginView `json:"login"`
		}
		decode(t, r.call("", "aws.login_start", map[string]any{"profile": "sso"}), &start)
		if start.Type != "aws_login" || start.Login.State != awslogin.StatePending ||
			start.Login.UserCode != evalUserCode || start.Login.LoginID == "" {
			t.Fatalf("login_start = %+v, want a pending aws_login with the user code", start)
		}

		var last loginView
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			var st struct {
				Type  string    `json:"type"`
				Login loginView `json:"login"`
			}
			decode(t, r.call("", "aws.login_status", map[string]any{"login_id": start.Login.LoginID}), &st)
			last = st.Login
			if last.UserCode != "" || last.VerificationURIComplete != "" {
				t.Fatalf("login_status carries the code or URL: %+v", last)
			}
			if last.State != awslogin.StatePending && last.State != awslogin.StateApproved && last.State != awslogin.StateMinting {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if last.State != awslogin.StateDone {
			t.Fatalf("login ended %q (failure %q), want done", last.State, last.Failure)
		}

		st, err := os.Stat(r.creds)
		if err != nil {
			t.Fatal(err)
		}
		if mode := st.Mode().Perm(); mode != 0o600 {
			t.Errorf("credentials mode = %o, want 600", mode)
		}
		profiles, err := awsdelegation.ReadHostCredentialProfiles(r.creds)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range profiles {
			if p.Name == "sso" && p.HasExpiry && p.ExpiresAt.Equal(r.ic.expires) {
				found = true
			}
		}
		if !found {
			t.Errorf("credentials file has no sso section with the minted expiry: %v", profileNames(profiles))
		}
		raw, err := os.ReadFile(r.creds)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), plantMintKeyID) {
			t.Error("credentials file does not hold the minted key id")
		}

		rows := r.status()
		if row := rows["sso"]; row.Status != "valid" {
			t.Errorf("sso after the flow = %+v, want valid", row)
		} else if at, err := time.Parse(time.RFC3339, row.ExpiresAt); err != nil || !at.Equal(r.ic.expires) {
			t.Errorf("sso expires_at after the flow = %q, want %s", row.ExpiresAt, r.ic.expires.Format(time.RFC3339))
		}
	})
}

func profileNames(ps []awsdelegation.HostProfile) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return fmt.Sprint(names)
}
