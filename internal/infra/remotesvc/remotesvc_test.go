package remotesvc

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestParseProbeSystemdActiveWithSocket(t *testing.T) {
	out := "" +
		"DRIVER=systemd\n" +
		"ACTIVE=active\n" +
		"VERSION=aiman v0.10.1\n" +
		"SOCKET=1\n" +
		"---LOGS---\n" +
		"listening on /home/u/.aiman/aiman.sock\n"
	st := ParseProbe(KindServe, "host.example", out)
	if st.Kind != string(KindServe) || st.RemoteHost != "host.example" {
		t.Fatalf("identity %+v", st)
	}
	if st.Status != domain.DaemonStatusRunning {
		t.Fatalf("status %s", st.Status)
	}
	if st.Driver != "systemd" || !st.SocketOK || st.Version != "aiman v0.10.1" {
		t.Fatalf("probe %+v", st)
	}
	if !strings.Contains(st.Logs, "listening") {
		t.Fatalf("logs %q", st.Logs)
	}
}

func TestParseProbeInactive(t *testing.T) {
	st := ParseProbe(KindServe, "h", "DRIVER=none\nACTIVE=inactive\nVERSION=missing\nSOCKET=0\n---LOGS---\n")
	if st.Status != domain.DaemonStatusStopped {
		t.Fatalf("status %s", st.Status)
	}
	if st.SocketOK {
		t.Fatal("socket should be down")
	}
}

func TestParseProbeFailedMapsToError(t *testing.T) {
	st := ParseProbe(KindServe, "regent0", ""+
		"DRIVER=systemd\n"+
		"ACTIVE=failed\n"+
		"VERSION=aiman v0.10.1\n"+
		"SOCKET=0\n"+
		"---LOGS---\n"+
		"Main process exited, code=exited, status=1/FAILURE\n")
	if st.Status != domain.DaemonStatusError {
		t.Fatalf("status %s want ERROR", st.Status)
	}
	if st.SocketOK {
		t.Fatal("failed unit must not report socket up")
	}
}

func TestParseProbeFailedInactiveConcatenation(t *testing.T) {
	// systemctl is-active prints "failed" and exits 3; `|| echo inactive` then
	// concatenates to "failed inactive" on one ACTIVE= line.
	st := ParseProbe(KindServe, "regent0", ""+
		"DRIVER=systemd\n"+
		"ACTIVE=failed inactive\n"+
		"VERSION=aiman v0.10.1\n"+
		"SOCKET=1\n"+
		"---LOGS---\n"+
		"aiman-serve.service: Scheduled restart job, restart counter is at 5.\n")
	if st.Status != domain.DaemonStatusError {
		t.Fatalf("status %s want ERROR (got STOPPED means concat was ignored)", st.Status)
	}
	if st.SocketOK {
		t.Fatal("stale sock after crash must not show as up")
	}
}

func TestParseProbeActivatingMapsToRunning(t *testing.T) {
	st := ParseProbe(KindServe, "h", "DRIVER=systemd\nACTIVE=activating\nVERSION=aiman v0.10.1\nSOCKET=0\n---LOGS---\n")
	if st.Status != domain.DaemonStatusRunning {
		t.Fatalf("status %s want RUNNING", st.Status)
	}
}

func TestParseProbeIgnoresStaleSocketWhenNotActive(t *testing.T) {
	st := ParseProbe(KindServe, "h", "DRIVER=systemd\nACTIVE=inactive\nVERSION=aiman v0.10.1\nSOCKET=1\n---LOGS---\n")
	if st.SocketOK {
		t.Fatal("SOCKET=1 while inactive is a leftover sock")
	}
	if st.Status != domain.DaemonStatusStopped {
		t.Fatalf("status %s", st.Status)
	}
}

func TestServeUnitFileIsUserService(t *testing.T) {
	u := UnitFile(KindServe)
	for _, want := range []string{
		"[Unit]",
		"ExecStart=",
		"aiman serve",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestInstallScriptEnablesLingerAndUnit(t *testing.T) {
	s := InstallEnableScript(KindServe)
	for _, want := range []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now aiman-serve.service",
		"loginctl enable-linger",
		"aiman-serve.service",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("install script missing %q", want)
		}
	}
}

func TestStartStopUpdateScripts(t *testing.T) {
	start := StartScript(KindServe)
	if !strings.Contains(start, "systemctl --user start aiman-serve") &&
		!strings.Contains(start, "systemctl --user restart aiman-serve") {
		t.Fatalf("start:\n%s", start)
	}
	if !strings.Contains(start, "nohup") {
		t.Fatal("start should fall back to nohup")
	}
	stop := StopScript(KindServe)
	if !strings.Contains(stop, "systemctl --user stop aiman-serve") {
		t.Fatalf("stop:\n%s", stop)
	}
	upd := UpdateScript(KindServe)
	if !strings.Contains(upd, "install.sh") {
		t.Fatalf("update:\n%s", upd)
	}
	if !strings.Contains(upd, "systemctl --user start aiman-serve") &&
		!strings.Contains(upd, "systemctl --user restart aiman-serve") {
		t.Fatalf("update missing start/restart:\n%s", upd)
	}
}

func TestInstallEnablesLingerBeforeUserSystemd(t *testing.T) {
	s := InstallEnableScript(KindServe)
	linger := strings.Index(s, "loginctl enable-linger")
	show := strings.Index(s, "systemctl --user show-environment")
	if linger < 0 || show < 0 || linger > show {
		t.Fatalf("linger must run before show-environment:\n%s", s)
	}
	if !strings.Contains(s, "XDG_RUNTIME_DIR") {
		t.Fatal("install must set XDG_RUNTIME_DIR for SSH")
	}
}

func TestStopTriggerDoesNotKillServe(t *testing.T) {
	stop := StopScript(KindTrigger)
	if strings.Contains(stop, "[a]iman serve") {
		t.Fatalf("trigger stop must not pkill serve:\n%s", stop)
	}
	if !strings.Contains(stop, "[a]iman-trigger") {
		t.Fatalf("trigger stop missing pkill pattern:\n%s", stop)
	}
}

func TestLifecycleScriptsSetRuntimeDir(t *testing.T) {
	for _, s := range []string{
		InstallEnableScript(KindServe),
		StartScript(KindServe),
		StopScript(KindServe),
		ProbeScript(KindServe),
	} {
		if !strings.Contains(s, "XDG_RUNTIME_DIR") {
			t.Fatal("script must set XDG_RUNTIME_DIR for SSH systemctl --user")
		}
	}
}

func TestProbeScriptDoesNotConcatIsActiveFallback(t *testing.T) {
	s := ProbeScript(KindServe)
	if strings.Contains(s, "is-active") && strings.Contains(s, "|| echo inactive") {
		t.Fatalf("is-active || echo inactive concatenates 'failed inactive':\n%s", s)
	}
	if !strings.Contains(s, "ActiveState") {
		t.Fatalf("probe must read ActiveState (is-active exits 3 on failed):\n%s", s)
	}
}

func TestProbeScriptSocketRequiresActive(t *testing.T) {
	s := ProbeScript(KindServe)
	if !strings.Contains(s, `ACTIVE`) || !strings.Contains(s, `aiman.sock`) {
		t.Fatalf("socket check missing:\n%s", s)
	}
	if !strings.Contains(s, `"$ACTIVE" = active`) && !strings.Contains(s, `"$ACTIVE" = "active"`) {
		t.Fatalf("socket must be gated on ACTIVE=active:\n%s", s)
	}
}

func TestProbeScriptIncludesServeLog(t *testing.T) {
	s := ProbeScript(KindServe)
	if !strings.Contains(s, "serve.log") {
		t.Fatalf("probe must tail serve.log (Go errors are not in journalctl):\n%s", s)
	}
}

func TestStartScriptResetsFailedAndClearsLeftovers(t *testing.T) {
	s := StartScript(KindServe)
	for _, want := range []string{
		"reset-failed",
		"aiman-serve.service",
		"[a]iman serve",
		"StartLimitBurst",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("start missing %q:\n%s", want, s)
		}
	}
}

func TestInstallScriptResetsFailedAndClearsLeftovers(t *testing.T) {
	s := InstallEnableScript(KindServe)
	for _, want := range []string{
		"reset-failed",
		"[a]iman serve",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("install missing %q:\n%s", want, s)
		}
	}
}

func TestServeUnitFileRaisesStartLimit(t *testing.T) {
	u := UnitFile(KindServe)
	if !strings.Contains(u, "StartLimitBurst") {
		t.Fatalf("unit missing StartLimitBurst (default 5 in 10s locks the unit failed):\n%s", u)
	}
	if !strings.Contains(u, "StartLimitIntervalSec") {
		t.Fatalf("unit missing StartLimitIntervalSec:\n%s", u)
	}
}

func TestScriptsHaveNoFmtLeftovers(t *testing.T) {
	for _, k := range []Kind{KindServe, KindTrigger} {
		scripts := map[string]string{
			"install": InstallEnableScript(k),
			"start":   StartScript(k),
			"stop":    StopScript(k),
			"probe":   ProbeScript(k),
			"update":  UpdateScript(k),
			"unit":    UnitFile(k),
		}
		for name, s := range scripts {
			if strings.Contains(s, "%!") {
				t.Fatalf("%s %s has fmt error:\n%s", k, name, s)
			}
		}
	}
}
