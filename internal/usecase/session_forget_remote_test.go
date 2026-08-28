package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestForgetSessionOnRemoteCallsServe(t *testing.T) {
	r := &recordingRemote{out: `{"type":"session_forgotten","id":"abc"}`}
	if err := ForgetSessionOnRemote(context.Background(), r, "abc"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(r.joined(), `aiman session forget "abc"`) {
		t.Errorf("wrong command: %s", r.joined())
	}
}

// A remote with no serve running, or one too old to know the method, cannot be
// holding the row — failing the teardown over it would strand the local record
// after the terminal has already been killed.
func TestForgetSessionOnRemoteToleratesAMissingServe(t *testing.T) {
	for _, out := range []string{
		`{"error":{"code":"not_found","message":"session not found"}}`,
		`{"error":{"code":"server_not_running","message":"no socket"}}`,
		`{"error":{"code":"invalid_params","message":"unknown method session.forget"}}`,
		`bash: aiman: command not found`,
	} {
		r := &recordingRemote{out: out, err: errors.New("exit status 1")}
		if err := ForgetSessionOnRemote(context.Background(), r, "abc"); err != nil {
			t.Errorf("%s should be tolerated, got %v", out, err)
		}
	}
}

func TestForgetSessionOnRemoteSurfacesRealFailures(t *testing.T) {
	r := &recordingRemote{out: "permission denied", err: errors.New("exit status 1")}
	if err := ForgetSessionOnRemote(context.Background(), r, "abc"); err == nil {
		t.Error("a real failure must not be swallowed")
	}
}

func TestForgetSessionOnRemoteIgnoresAnEmptyID(t *testing.T) {
	r := &recordingRemote{}
	if err := ForgetSessionOnRemote(context.Background(), r, "  "); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("nothing should be sent without an id: %v", r.cmds)
	}
}
