//go:build acceptance

package server

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const (
	awsLogCaller  = "sess-aws-log"
	awsLogPeerPID = 6161
)

type awsLogResult struct {
	Type   string                `json:"type"`
	Events []domain.SessionEvent `json:"events"`
}

// awsLogServer is TestAWSLoginLedger's rig: one login that starts and ends,
// with both ledger rows written before it returns.
func awsLogServer(t *testing.T) *Server {
	t.Helper()
	const id = "0123456789abcdef0123456789abcdef"
	engine := &scriptedLogin{
		login: awslogin.Login{ID: id, State: awslogin.StatePending},
		states: []awslogin.Login{
			{ID: id, Profile: "dev", State: awslogin.StatePending},
			{ID: id, Profile: "dev", State: awslogin.StateFailed, Failure: awslogin.FailureWriteFailed},
		},
	}
	srv, _, _ := ptyServer(t, todoSession(awsLogCaller))
	srv.procInSession = func(pid int) bool { return pid == awsLogPeerPID }
	srv.SetAWS(config.ServeAWSConfig{Delegations: []config.ServeAWSDelegation{{
		Profile: "dev", SSOStartURL: "https://x.awsapps.com/start", SSORegion: "eu-west-1", SSOAccountID: "222222222222",
	}}}, filepath.Join(t.TempDir(), "credentials"), engine)
	srv.aws.watchEvery = time.Millisecond

	if resp := rpc(t, srv, "", "aws.login_start", map[string]any{"profile": "dev"}); resp.Error != nil {
		t.Fatalf("start: %+v", resp.Error)
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
	return srv
}

func awsLog(t *testing.T, srv *Server, params map[string]any) awsLogResult {
	t.Helper()
	var got awsLogResult
	rpcOK(t, srv, "", "aws.log", params, &got)
	if got.Type != "aws_log" {
		t.Fatalf("type = %q, want aws_log", got.Type)
	}
	return got
}

func TestAWSLogReturnsLoginRows(t *testing.T) {
	srv := awsLogServer(t)
	ledger := awsLedgerRows(t, srv)

	got := awsLog(t, srv, nil)
	if len(got.Events) != 2 {
		t.Fatalf("events = %+v, want two", got.Events)
	}
	if got.Events[0].Kind != domain.EventAWSLoginStarted || got.Events[1].Kind != domain.EventAWSLoginFinished {
		t.Fatalf("kinds = %s, %s, want started then finished", got.Events[0].Kind, got.Events[1].Kind)
	}
	for i, ev := range got.Events {
		if ev.Seq == 0 || ev.Seq != ledger[i].Seq {
			t.Errorf("event %d seq = %d, want ledger seq %d", i, ev.Seq, ledger[i].Seq)
		}
	}
	if got.Events[0].Seq >= got.Events[1].Seq {
		t.Errorf("events not oldest first: seq %d then %d", got.Events[0].Seq, got.Events[1].Seq)
	}
	var finished map[string]string
	if err := json.Unmarshal(got.Events[1].Payload, &finished); err != nil {
		t.Fatal(err)
	}
	if finished["profile"] != "dev" || finished["state"] != awslogin.StateFailed {
		t.Errorf("finished payload = %v", finished)
	}

	since := awsLog(t, srv, map[string]any{"since_seq": got.Events[0].Seq})
	if len(since.Events) != 1 || since.Events[0].Seq != got.Events[1].Seq || since.Events[0].Kind != domain.EventAWSLoginFinished {
		t.Fatalf("since_seq %d = %+v, want only the finished row", got.Events[0].Seq, since.Events)
	}
}

func TestAWSLogRefusesSessionCaller(t *testing.T) {
	srv := awsLogServer(t)
	for _, tc := range []struct {
		name   string
		caller string
		pid    int
	}{
		{name: "named caller", caller: awsLogCaller, pid: scopeNoPID},
		{name: "peer in session", pid: awsLogPeerPID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := scopeCall(t, srv, tc.caller, tc.pid, "aws.log", nil)
			if resp.Error == nil {
				t.Fatalf("want needs_operator, got result %+v", resp.Result)
			}
			if resp.Error.Code != CodeNeedsOperator {
				t.Fatalf("code = %s (%q), want %s", resp.Error.Code, resp.Error.Message, CodeNeedsOperator)
			}
		})
	}
}
