package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// ciRPCRemote answers every command with out and records what it ran.
type ciRPCRemote struct {
	out      string
	commands []string
}

func (r *ciRPCRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.commands = append(r.commands, cmd)
	return r.out, nil
}

func (r *ciRPCRemote) WriteFile(context.Context, string, []byte) error { return nil }

func TestParseCIResult(t *testing.T) {
	got, err := parseCIResult(`{"type":"ci","ci":{"watch":true,"pr_number":12,"pr_state":"OPEN","head_sha":"abc","escalated":["lint"],"last_error":"gh: rate limited"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := CIStatus{Watch: true, PRNumber: 12, PRState: "OPEN", Escalated: []string{"lint"}, LastError: "gh: rate limited"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}

	for _, bad := range []string{`{"type":"ci"`, `{"type":"ci"}`, `not json`} {
		if _, err := parseCIResult(bad); err == nil {
			t.Errorf("parseCIResult(%q) returned no error", bad)
		}
	}
}

func TestCIRemoteUnsupported(t *testing.T) {
	remote := &ciRPCRemote{out: `{"error":{"code":"bad_request","message":"unknown method: ci.get"}}`}
	_, err := GetCIStatusOnRemote(context.Background(), remote, "id1")
	if !errors.Is(err, ErrCIUnsupported) {
		t.Fatalf("ci.get err = %v, want ErrCIUnsupported", err)
	}
	if !errors.Is(err, ErrRPCUnsupported) {
		t.Fatalf("ci.get err = %v no longer matches ErrRPCUnsupported", err)
	}
	if _, err := SetPRWatchOnRemote(context.Background(), remote, "id1", true); !errors.Is(err, ErrCIUnsupported) {
		t.Fatalf("ci.watch err = %v, want ErrCIUnsupported", err)
	}
}

func TestSetPRWatchOnRemoteParams(t *testing.T) {
	remote := &ciRPCRemote{out: `{"result":{"type":"ci","ci":{"watch":false,"pr_number":7}}}`}
	got, err := SetPRWatchOnRemote(context.Background(), remote, "id1", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Watch || got.PRNumber != 7 {
		t.Fatalf("status = %+v", got)
	}
	params := decodeRPCParams(t, commandContaining(t, remote.commands, "aiman rpc ci.watch"))
	if params["session"] != "id1" || params["on"] != false || len(params) != 2 {
		t.Fatalf("params = %#v", params)
	}
}
