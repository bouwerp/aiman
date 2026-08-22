package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

func TestDaemonListHasServeAndTriggerPerRemote(t *testing.T) {
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	items := m.daemonList.Items()
	if len(items) != 4 {
		t.Fatalf("len=%d want 4 (2 remotes × serve+trigger)", len(items))
	}
	first, ok := items[0].(daemonItem)
	if !ok || first.daemon.Kind != string(remotesvc.KindServe) {
		t.Fatalf("first %+v", items[0])
	}
	if first.Title() != "serve  ·  10.0.1.5" {
		t.Fatalf("title %q", first.Title())
	}
	second := items[1].(daemonItem)
	if second.daemon.Kind != string(remotesvc.KindTrigger) {
		t.Fatalf("second kind %s", second.daemon.Kind)
	}
}

func TestStoreDaemonRoundTrip(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.storeDaemon(domain.Daemon{
		RemoteHost: "10.0.1.5",
		Kind:       string(remotesvc.KindServe),
		Status:     domain.DaemonStatusRunning,
		SocketOK:   true,
		Driver:     "systemd",
		Version:    "aiman v0.10.1",
	})
	m.applyRemoteFilter()
	got := m.daemons[domain.DaemonKey("10.0.1.5", string(remotesvc.KindServe))]
	if got.Status != domain.DaemonStatusRunning || !got.SocketOK {
		t.Fatalf("%+v", got)
	}
	if desc := m.daemonList.Items()[0].(daemonItem).Description(); desc != "RUNNING  aiman v0.10.1  socket  systemd" {
		t.Fatalf("desc %q", desc)
	}
}
