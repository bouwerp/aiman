package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

// host.version carries the gateway block (gateway-update rev 2).

type hostVersionGateway struct {
	Gateway *struct {
		Installed bool   `json:"installed"`
		Version   string `json:"version"`
		Running   bool   `json:"running"`
		Status    string `json:"status"`
	} `json:"gateway"`
	Attempt *struct {
		Kind string `json:"kind"`
	} `json:"attempt"`
}

func TestHostVersionCarriesTheGateway(t *testing.T) {
	m, _ := hostUpdateManager(t, selfupdate.ModeNotify)
	m.ReadGateway = func(context.Context) (selfupdate.Gateway, error) {
		return selfupdate.Gateway{Installed: true, Version: "v0.33.0", Running: true}, nil
	}
	srv := hostUpdateServer(t)
	srv.SetHostUpdates(m, hostUpdateName)

	var raw json.RawMessage
	rpcOK(t, srv, "", "host.version", map[string]any{}, &raw)
	var got hostVersionGateway
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Gateway == nil {
		t.Fatalf("host.version has no gateway block: %s", raw)
	}
	g := got.Gateway
	if !g.Installed || g.Version != "v0.33.0" || !g.Running || g.Status != "behind" {
		t.Fatalf("gateway = %+v, want installed running v0.33.0 behind serve %s", *g, hostUpdateSelf)
	}
}

func TestHostVersionWithoutManagerHasNoGateway(t *testing.T) {
	srv := hostUpdateServer(t)
	var raw json.RawMessage
	rpcOK(t, srv, "", "host.version", map[string]any{}, &raw)
	var got hostVersionGateway
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Gateway != nil {
		t.Fatalf("gateway = %+v without a manager, want absent", *got.Gateway)
	}
}
