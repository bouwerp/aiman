package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// scriptedAWSRemote answers command -v aws and get-caller-identity per profile.
type scriptedAWSRemote struct {
	*fakeRemote
	mu       sync.Mutex
	noCLI    bool
	identity map[string]func() (string, error)
	checks   []string
}

func (r *scriptedAWSRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.HasPrefix(cmd, "command -v aws") {
		if r.noCLI {
			return "", errors.New("exit status 127")
		}
		return "/usr/bin/aws\n", nil
	}
	for profile, answer := range r.identity {
		if strings.Contains(cmd, "--profile '"+profile+"'") || strings.Contains(cmd, "--profile "+profile+" ") {
			r.checks = append(r.checks, profile)
			return answer()
		}
	}
	return "", errors.New("unexpected command " + cmd)
}

func (r *scriptedAWSRemote) checked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.checks...)
}

// awsCheckServer has three delegated sections with a future recorded expiry, one
// delegation with no section and one section with no delegation.
func awsCheckServer(t *testing.T, remote *scriptedAWSRemote) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	exp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	var b strings.Builder
	for _, name := range []string{"good", "bad", "odd", "stray"} {
		b.WriteString("[" + name + "]\naws_access_key_id = AKIAX\nx_security_token_expires = " + exp + "\n\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := ptyServer(t)
	srv.remote = remote
	srv.SetAWS(config.ServeAWSConfig{Delegations: []config.ServeAWSDelegation{
		{Profile: "good"}, {Profile: "bad"}, {Profile: "odd"}, {Profile: "missing"},
	}}, path, &scriptedLogin{})
	return srv, path
}

func awsRowStatuses(t *testing.T, resp Response) map[string]string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("aws.status: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Profiles []struct {
			Profile string `json:"profile"`
			Status  string `json:"status"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, row := range got.Profiles {
		out[row.Profile] = row.Status
	}
	return out
}

func TestHandleAWSStatusCheck(t *testing.T) {
	remote := &scriptedAWSRemote{fakeRemote: &fakeRemote{}, identity: map[string]func() (string, error){
		"good": func() (string, error) { return "111 arn:aws:sts::111:x\n", nil },
		"bad":  func() (string, error) { return "An error occurred (ExpiredToken)", errors.New("exit status 255") },
		"odd":  func() (string, error) { return "", context.DeadlineExceeded },
	}}
	srv, path := awsCheckServer(t, remote)

	got := awsRowStatuses(t, rpc(t, srv, "", "aws.status", map[string]any{"check": true}))
	want := map[string]string{
		"good": awsStatusValid, "bad": awsStatusExpired, "odd": awsStatusValid,
		"missing": awsStatusNotPushed, "stray": awsStatusValid,
	}
	for profile, status := range want {
		if got[profile] != status {
			t.Errorf("%s = %q, want %q", profile, got[profile], status)
		}
	}
	first := remote.checked()
	if strings.Join(first, ",") != "good,bad,odd" {
		t.Fatalf("checked %v, want the delegated sections once each", first)
	}

	// A plain status reads the file alone; a second check runs only what had no
	// conclusive answer.
	if got := awsRowStatuses(t, rpc(t, srv, "", "aws.status", nil)); got["bad"] != awsStatusValid {
		t.Errorf("plain status bad = %q, want the file's valid", got["bad"])
	}
	if got := awsRowStatuses(t, rpc(t, srv, "", "aws.status", map[string]any{"check": true})); got["bad"] != awsStatusExpired {
		t.Errorf("cached bad = %q, want expired", got["bad"])
	}
	if n := len(remote.checked()); n != len(first)+1 {
		t.Errorf("second check ran %d more, want only odd", n-len(first))
	}

	// New tokens in the file void the cached verdict.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	remote.identity["bad"] = func() (string, error) { return "111 arn:aws:sts::111:x\n", nil }
	remote.mu.Unlock()
	if got := awsRowStatuses(t, rpc(t, srv, "", "aws.status", map[string]any{"check": true})); got["bad"] != awsStatusValid {
		t.Errorf("after a rewrite bad = %q, want valid", got["bad"])
	}
}

func TestHandleAWSStatusCheckSkippedWithoutCLI(t *testing.T) {
	remote := &scriptedAWSRemote{fakeRemote: &fakeRemote{}, noCLI: true}
	srv, _ := awsCheckServer(t, remote)
	got := awsRowStatuses(t, rpc(t, srv, "", "aws.status", map[string]any{"check": true}))
	if got["good"] != awsStatusValid || got["bad"] != awsStatusValid {
		t.Errorf("rows = %v, want the file's status", got)
	}
	if n := len(remote.checked()); n != 0 {
		t.Errorf("ran %d checks with no aws on the host", n)
	}
}

func TestHandleAWSUnwired(t *testing.T) {
	srv, _, _ := ptyServer(t)
	for _, method := range []string{"aws.status", "aws.login_start", "aws.login_status"} {
		resp := rpc(t, srv, "", method, map[string]any{"profile": "dev", "login_id": "x"})
		if resp.Error == nil || resp.Error.Code != CodeUnavailable {
			t.Errorf("%s = %+v, want unavailable", method, resp.Error)
		}
	}
}

// scriptedLogin returns one login and walks Status through states.
type scriptedLogin struct {
	mu     sync.Mutex
	login  awslogin.Login
	states []awslogin.Login
	starts int
}

func (l *scriptedLogin) Start(_ context.Context, d awslogin.Delegation) (awslogin.Login, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.starts++
	out := l.login
	out.Profile = d.Profile
	return out, nil
}

func (l *scriptedLogin) Status(string) (awslogin.Login, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.states) == 0 {
		return awslogin.Login{}, false
	}
	next := l.states[0]
	if len(l.states) > 1 {
		l.states = l.states[1:]
	}
	return next, true
}

func awsLedgerRows(t *testing.T, srv *Server) []domain.SessionEvent {
	t.Helper()
	lister, ok := srv.repo.(sessionEventLister)
	if !ok {
		t.Fatal("repository has no ledger")
	}
	rows, err := lister.ListSessionEvents(context.Background(), domain.AWSLoginLedger, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAWSLoginLedger(t *testing.T) {
	const code = "ABCD-EFGH"
	const id = "0123456789abcdef0123456789abcdef"
	url := "https://device.sso.eu-west-1.amazonaws.com/?user_code=" + code
	engine := &scriptedLogin{
		login: awslogin.Login{ID: id, State: awslogin.StatePending, UserCode: code, VerificationURIComplete: url},
		states: []awslogin.Login{
			{ID: id, Profile: "dev", State: awslogin.StatePending, UserCode: code},
			{ID: id, Profile: "dev", State: awslogin.StateMinting, UserCode: code},
			{ID: id, Profile: "dev", State: awslogin.StateFailed, Failure: awslogin.FailureWriteFailed, UserCode: code},
		},
	}
	srv, _, _ := ptyServer(t)
	srv.SetAWS(config.ServeAWSConfig{Delegations: []config.ServeAWSDelegation{{
		Profile: "dev", SSOStartURL: "https://x.awsapps.com/start", SSORegion: "eu-west-1", SSOAccountID: "222222222222",
	}}}, filepath.Join(t.TempDir(), "credentials"), engine)
	srv.aws.watchEvery = time.Millisecond

	// The engine hands the same pending login back: one started row, one watcher.
	for range 2 {
		if resp := rpc(t, srv, "", "aws.login_start", map[string]any{"profile": "dev"}); resp.Error != nil {
			t.Fatalf("start: %+v", resp.Error)
		}
	}

	var rows []domain.SessionEvent
	deadline := time.Now().Add(5 * time.Second)
	for len(rows) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		rows = awsLedgerRows(t, srv)
	}
	if len(rows) != 2 {
		t.Fatalf("ledger rows = %+v, want started and finished", rows)
	}
	if rows[0].Kind != domain.EventAWSLoginStarted || rows[0].Actor != domain.EventActorUser {
		t.Errorf("first row = %+v", rows[0])
	}
	if rows[1].Kind != domain.EventAWSLoginFinished {
		t.Errorf("second row = %+v", rows[1])
	}
	var finished map[string]string
	if err := json.Unmarshal(rows[1].Payload, &finished); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"profile": "dev", "state": awslogin.StateFailed, "failure": awslogin.FailureWriteFailed}
	if len(finished) != len(want) {
		t.Errorf("finished payload = %v, want %v", finished, want)
	}
	for k, v := range want {
		if finished[k] != v {
			t.Errorf("finished[%s] = %q, want %q", k, finished[k], v)
		}
	}
	for _, row := range rows {
		for _, leak := range []string{code, id, url} {
			if strings.Contains(string(row.Payload), leak) {
				t.Errorf("%s payload holds %q: %s", row.Kind, leak, row.Payload)
			}
		}
	}
	if engine.starts != 2 {
		t.Errorf("engine starts = %d, want 2", engine.starts)
	}
}
