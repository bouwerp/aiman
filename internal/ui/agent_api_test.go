package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/infra/rtk"
)

func TestMenuHasAgentAPIItem(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	found := false
	for _, it := range m.menu.Items() {
		mi, ok := it.(menuItem)
		if ok && mi.action == viewStateAgentAPI {
			found = true
			if !strings.Contains(strings.ToLower(mi.Title()), "agent api") {
				t.Fatalf("title %q", mi.Title())
			}
			break
		}
	}
	if !found {
		t.Fatal("admin menu missing Agent API")
	}
}

func TestMenuEnterOpensAgentAPISettings(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateMenu
	for i, it := range m.menu.Items() {
		if mi, ok := it.(menuItem); ok && mi.action == viewStateAgentAPI {
			m.menu.Select(i)
			break
		}
	}
	got, _ := m.handleMenuUpdate(pressKey("enter"))
	model := got.(*Model)
	if model.state != viewStateAgentAPI {
		t.Fatalf("state %v", model.state)
	}
}

func TestAgentAPIViewListsEachRemote(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.enterAgentAPI()
	out := m.renderAgentAPIView()
	for _, want := range []string{"Agent API", "10.0.1.5", "i install/enable", "PROBING"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestAgentAPIInstallStaysOnSettingsPage(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	m.storeDaemon(domain.Daemon{RemoteHost: "10.0.1.5", Kind: string(remotesvc.KindServe)})
	_, _, _ = m.runSelectedServiceOp("install", "Installing")
	if m.state != viewStateLoading {
		t.Fatalf("state %v", m.state)
	}
	if m.loadingNext != viewStateAgentAPI {
		t.Fatalf("loadingNext %v", m.loadingNext)
	}
}

func TestAgentAPIProbeKeyShowsProbing(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	got, cmd := m.handleAgentAPIUpdate(pressKey("r"))
	model := got.(*Model)
	if cmd == nil {
		t.Fatal("r must fire a probe command")
	}
	out := model.renderAgentAPIView()
	if !strings.Contains(out, "PROBING") {
		t.Fatalf("r must show PROBING while the SSH probe is in flight:\n%s", out)
	}
}

func TestAgentAPIHintFailedSystemdSaysRestart(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	m.storeDaemon(domain.Daemon{
		RemoteHost: "10.0.1.5",
		Kind:       string(remotesvc.KindServe),
		Status:     domain.DaemonStatusError,
		Driver:     "systemd",
		Version:    "aiman v0.10.1",
		Logs:       "Main process exited, code=exited, status=1/FAILURE",
	})
	out := m.renderAgentAPIView()
	if strings.Contains(out, "Press i to install and enable") {
		t.Fatalf("installed failed unit must not tell the operator to install:\n%s", out)
	}
	if !strings.Contains(out, "restart") {
		t.Fatalf("failed systemd unit must point at restart:\n%s", out)
	}
}

func TestAgentAPIHintUninstalledSaysInstall(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	out := m.renderAgentAPIView()
	if !strings.Contains(out, "Press i to install and enable") {
		t.Fatalf("missing install hint:\n%s", out)
	}
}

func TestApplyDaemonProbeClearsProbing(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	_, _ = m.handleAgentAPIUpdate(pressKey("r"))
	_, _ = m.applyDaemonProbe(daemonProbeMsg{daemon: domain.Daemon{
		RemoteHost: "10.0.1.5",
		Kind:       string(remotesvc.KindServe),
		Status:     domain.DaemonStatusError,
		Driver:     "systemd",
		Logs:       "failed",
	}})
	out := m.renderAgentAPIView()
	if strings.Contains(out, "PROBING") {
		t.Fatalf("PROBING must clear after the probe returns:\n%s", out)
	}
	if !strings.Contains(out, "ERROR") {
		t.Fatalf("want ERROR after failed probe:\n%s", out)
	}
}

func TestAgentAPICursorMovesBetweenRemotes(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	got, _ := m.handleAgentAPIUpdate(pressKey("down"))
	model := got.(*Model)
	if model.agentAPICursor != 1 {
		t.Fatalf("cursor %d", model.agentAPICursor)
	}
	d, ok := model.selectedAgentAPIDaemon()
	if !ok || d.RemoteHost == "10.0.1.5" {
		t.Fatalf("selected %+v ok=%v", d, ok)
	}
}

func oneRemoteCfg() *config.Config {
	return &config.Config{Remotes: []config.Remote{{Name: "dev-box", Host: "h", User: "ubuntu", Root: "/home/ubuntu"}}}
}

func TestAgentAPIKeyTStartsRTKInstall(t *testing.T) {
	m := NewModel(oneRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	got, cmd := m.handleAgentAPIUpdate(pressRune('t'))
	model := got.(*Model)
	if cmd == nil {
		t.Fatal("t must start the RTK install")
	}
	if model.state != viewStateLoading {
		t.Fatalf("state %v", model.state)
	}
	if model.loadingNext != viewStateAgentAPI {
		t.Fatalf("loadingNext %v", model.loadingNext)
	}
	if !strings.Contains(model.loadingMsg, "RTK") {
		t.Fatalf("loadingMsg %q", model.loadingMsg)
	}
}

func TestRTKInstallMsgToasts(t *testing.T) {
	cases := []struct {
		name    string
		msg     rtkInstallMsg
		want    string
		isError bool
	}{
		{
			name:    "failed hook",
			msg:     rtkInstallMsg{host: "h", report: rtk.InstallReport{Version: "rtk 0.40.1", Hooks: map[string]bool{"claude": true, "codex": false}}},
			want:    "✓  RTK rtk 0.40.1 on h · hooks: claude · failed: codex",
			isError: true,
		},
		{
			name: "all hooks ok",
			msg:  rtkInstallMsg{host: "h", report: rtk.InstallReport{Version: "rtk 0.40.1", Hooks: map[string]bool{"codex": true, "claude": true}}},
			want: "✓  RTK rtk 0.40.1 on h · hooks: claude, codex",
		},
		{
			name:    "wrong binary",
			msg:     rtkInstallMsg{host: "h", report: rtk.InstallReport{WrongBinary: true}, err: errors.New("exit status 3")},
			want:    `⚠️  another "rtk" is on PATH on h (Rust Type Kit?)`,
			isError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(oneRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
			m.state = viewStateLoading
			m.loadingNext = viewStateAgentAPI
			_, cmd := m.applyRTKInstall(tc.msg)
			if cmd == nil {
				t.Fatal("want toast and re-probe commands")
			}
			if m.state != viewStateAgentAPI {
				t.Fatalf("state %v", m.state)
			}
			if m.snapshotToast != tc.want {
				t.Fatalf("toast = %q, want %q", m.snapshotToast, tc.want)
			}
			if m.snapshotToastError != tc.isError {
				t.Fatalf("isError = %v", m.snapshotToastError)
			}
		})
	}
}

func TestRTKInstallMsgErrorShowsErrorScreen(t *testing.T) {
	m := NewModel(oneRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateLoading
	m.loadingNext = viewStateAgentAPI
	_, _ = m.applyRTKInstall(rtkInstallMsg{host: "h", err: errors.New("curl failed")})
	if m.state != viewStateError || !strings.Contains(m.lastError, "curl failed") {
		t.Fatalf("state %v lastError %q", m.state, m.lastError)
	}
}

func TestAgentAPIShowsRTKLine(t *testing.T) {
	m := NewModel(oneRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	m.storeDaemon(domain.Daemon{RemoteHost: "h", Kind: string(remotesvc.KindServe), Status: domain.DaemonStatusRunning, SocketOK: true})
	out := m.renderAgentAPIView()
	if !strings.Contains(out, "RTK: not installed (t to install)") {
		t.Fatalf("missing RTK line:\n%s", out)
	}
	if !strings.Contains(out, "t install RTK") {
		t.Fatalf("footer missing t hint:\n%s", out)
	}
	m.storeDaemon(domain.Daemon{RemoteHost: "h", Kind: string(remotesvc.KindServe), Status: domain.DaemonStatusRunning, SocketOK: true, RTKVersion: "rtk 0.40.1"})
	if out := m.renderAgentAPIView(); !strings.Contains(out, "RTK: rtk 0.40.1") {
		t.Fatalf("missing RTK version:\n%s", out)
	}
}

func TestRTKGainLine(t *testing.T) {
	g := rtk.Gain{SavedTokens: 1200000, SavingsPct: 80, Commands: 300}
	if got, want := rtkGainLine(g, true), "rtk: 1.2M tokens saved (80%) over 300 commands"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := rtkGainLine(g, false); got != "" {
		t.Fatalf("not installed: got %q", got)
	}
}
