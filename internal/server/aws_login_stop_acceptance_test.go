//go:build acceptance

package server

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// countingLogin is a scriptedLogin that counts Status calls, so the test knows
// a watcher is polling before it stops the watchers.
type countingLogin struct {
	*scriptedLogin
	polls atomic.Int64
}

func (l *countingLogin) Status(id string) (awslogin.Login, bool) {
	l.polls.Add(1)
	return l.scriptedLogin.Status(id)
}

// closeAWSLoginWithin runs CloseAWSLogin and fails when it has not returned
// within d.
func closeAWSLoginWithin(t *testing.T, srv *Server, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		srv.CloseAWSLogin()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("CloseAWSLogin did not return within %s", d)
	}
}

func TestCloseAWSLoginStopsWatchers(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	engine := &countingLogin{scriptedLogin: &scriptedLogin{
		login:  awslogin.Login{ID: id, State: awslogin.StatePending},
		states: []awslogin.Login{{ID: id, Profile: "dev", State: awslogin.StatePending}},
	}}
	srv, _, _ := ptyServer(t)
	srv.SetAWS(config.ServeAWSConfig{Delegations: []config.ServeAWSDelegation{{
		Profile: "dev", SSOStartURL: "https://x.awsapps.com/start", SSORegion: "eu-west-1", SSOAccountID: "222222222222",
	}}}, filepath.Join(t.TempDir(), "credentials"), engine)
	srv.aws.watchEvery = time.Millisecond

	if resp := rpc(t, srv, "", "aws.login_start", map[string]any{"profile": "dev"}); resp.Error != nil {
		t.Fatalf("start: %+v", resp.Error)
	}
	deadline := time.Now().Add(time.Second)
	for engine.polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if engine.polls.Load() == 0 {
		t.Fatal("no watcher polled the engine")
	}

	closeAWSLoginWithin(t, srv, time.Second)

	engine.mu.Lock()
	engine.states = []awslogin.Login{{ID: id, Profile: "dev", State: awslogin.StateDone}}
	engine.mu.Unlock()
	time.Sleep(50 * time.Millisecond)

	rows := awsLedgerRows(t, srv)
	var started, finished int
	for _, row := range rows {
		switch row.Kind {
		case domain.EventAWSLoginStarted:
			started++
		case domain.EventAWSLoginFinished:
			finished++
		}
	}
	if started != 1 || finished != 0 {
		t.Fatalf("ledger = %+v, want one started and no finished", rows)
	}

	closeAWSLoginWithin(t, srv, 100*time.Millisecond)
}
