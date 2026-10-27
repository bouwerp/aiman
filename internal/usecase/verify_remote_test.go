package usecase

import (
	"context"
	"reflect"
	"testing"
)

func TestStartVerifyOnRemoteParams(t *testing.T) {
	remote := &ciRPCRemote{out: `{"result":{"type":"review","review":{"status":"starting"}}}`}
	status, err := StartVerifyOnRemote(context.Background(), remote, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if status != "starting" {
		t.Fatalf("status = %q", status)
	}
	params := decodeRPCParams(t, commandContaining(t, remote.commands, "aiman rpc verify.start"))
	if !reflect.DeepEqual(params, map[string]any{"session": "s1"}) {
		t.Fatalf("params = %#v", params)
	}
}
