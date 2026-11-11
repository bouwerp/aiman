package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
)

// gateway-update rev 2, piece B: the Agent API page shows the gateway's
// version, and the laptop leaves the gateway to a host whose own update
// manager runs.

func gatewayDaemon(version string, status domain.DaemonStatus) domain.Daemon {
	return domain.Daemon{RemoteHost: "h", Kind: string(remotesvc.KindGateway), Status: status, Version: version}
}

func TestAgentAPIShowsTheGatewayVersion(t *testing.T) {
	cases := []struct {
		name string
		gw   domain.Daemon
		want string
	}{
		{"current", gatewayDaemon("aiman-gateway v0.42.0", domain.DaemonStatusRunning), "gateway v0.42.0"},
		{"behind", gatewayDaemon("aiman-gateway v0.41.0 (built x)", domain.DaemonStatusRunning), "gateway behind: v0.41.0"},
		{"not installed", gatewayDaemon("", domain.DaemonStatusStopped), "gateway not installed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewModel(oneRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
			m.state = viewStateAgentAPI
			m.storeDaemon(domain.Daemon{RemoteHost: "h", Kind: string(remotesvc.KindServe), Status: domain.DaemonStatusRunning, SocketOK: true, Version: "aiman v0.42.0"})
			m.storeDaemon(c.gw)
			if out := m.renderAgentAPIView(); !strings.Contains(out, c.want) {
				t.Fatalf("missing %q in:\n%s", c.want, out)
			}
		})
	}
}

func TestLaptopLeavesGatewayToAManagedHost(t *testing.T) {
	m := autoUpdateModel("v0.42.0")
	serve := runningServe("aiman v0.42.0")
	serve.HostManaged = true
	m.daemons = map[string]domain.Daemon{domain.DaemonKey("regent0", string(remotesvc.KindServe)): serve}
	gw := domain.Daemon{RemoteHost: "regent0", Kind: string(remotesvc.KindGateway), Status: domain.DaemonStatusRunning, Version: "aiman-gateway v0.41.0"}
	if cmd := m.maybeAutoUpdateServe(gw); cmd != nil {
		t.Fatal("the laptop updated the gateway of a host whose own manager owns it")
	}

	serve.HostManaged = false
	m.daemons[domain.DaemonKey("regent0", string(remotesvc.KindServe))] = serve
	if cmd := m.maybeAutoUpdateServe(gw); cmd == nil {
		t.Fatal("without a host manager the laptop should still update an older gateway")
	}
}

func TestProbeMarksAManagedHost(t *testing.T) {
	recent := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	old := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	cases := map[string]bool{
		`SELFUPDATE={"checked_at":"` + recent + `","attempt":null}`: true,
		`SELFUPDATE={"checked_at":"` + old + `","attempt":null}`:    false,
		`SELFUPDATE=`: false,
	}
	for line, want := range cases {
		out := "DRIVER=systemd\nACTIVE=active\nVERSION=aiman v0.42.0\nSOCKET=1\n" + line + "\n"
		d := remotesvc.ParseProbe(remotesvc.KindServe, "h", out)
		if d.HostManaged != want {
			t.Errorf("%s: HostManaged = %v, want %v", line, d.HostManaged, want)
		}
	}
}
