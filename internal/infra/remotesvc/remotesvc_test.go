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
	if !strings.Contains(start, "systemctl --user restart aiman-serve") {
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
	if !strings.Contains(upd, "install.sh") || !strings.Contains(upd, "systemctl --user restart aiman-serve") {
		t.Fatalf("update:\n%s", upd)
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
