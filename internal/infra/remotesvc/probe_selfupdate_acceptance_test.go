package remotesvc

import (
	"strings"
	"testing"
	"time"
)

func TestP1ProbeScriptPrintsSelfUpdate(t *testing.T) {
	s := ProbeScript(KindServe)
	if !strings.Contains(s, "SELFUPDATE=") {
		t.Fatalf("serve probe does not print SELFUPDATE=:\n%s", s)
	}
	if !strings.Contains(s, "host-update.json") {
		t.Fatalf("serve probe does not read ~/.aiman/host-update.json:\n%s", s)
	}
	// The SELFUPDATE line must come before the logs marker, where ParseProbe
	// stops reading keys.
	if i, j := strings.Index(s, "SELFUPDATE="), strings.Index(s, "---LOGS---"); j >= 0 && i > j {
		t.Fatalf("SELFUPDATE= is printed after ---LOGS---:\n%s", s)
	}
}

func TestP1ParseProbeSelfUpdate(t *testing.T) {
	base := "DRIVER=systemd\nACTIVE=active\nVERSION=aiman v0.34.0\nSOCKET=1\n"

	out := base + `SELFUPDATE={"checked_at":"2026-11-08T10:15:00Z","attempt":{"status":"started","from":"v0.34.0","to":"v0.35.0","pid":4242,"at":"2026-11-08T10:15:30Z"}}` +
		"\n---LOGS---\nsome log\n"
	d := ParseProbe(KindServe, "remote1", out)
	if d.SelfUpdate == nil {
		t.Fatal("SelfUpdate is nil for a probe with a started attempt")
	}
	if d.SelfUpdate.Status != "started" || d.SelfUpdate.To != "v0.35.0" {
		t.Fatalf("SelfUpdate = %+v, want started to v0.35.0", *d.SelfUpdate)
	}
	if want := time.Date(2026, 11, 8, 10, 15, 30, 0, time.UTC); !d.SelfUpdate.At.Equal(want) {
		t.Fatalf("SelfUpdate.At = %v, want %v", d.SelfUpdate.At, want)
	}
	if d.Version != "aiman v0.34.0" || !d.SocketOK || d.Logs != "some log" {
		t.Fatalf("other probe fields changed: %+v", d)
	}

	for name, line := range map[string]string{
		"empty":       "SELFUPDATE=\n",
		"absent":      "",
		"no attempt":  `SELFUPDATE={"checked_at":"2026-11-08T10:15:00Z","attempt":null}` + "\n",
		"not json":    "SELFUPDATE={oops\n",
		"line in log": "",
	} {
		t.Run(name, func(t *testing.T) {
			out := base + line + "---LOGS---\n"
			if name == "line in log" {
				out += `SELFUPDATE={"attempt":{"status":"started","to":"v0.35.0","at":"2026-11-08T10:15:30Z"}}` + "\n"
			}
			if d := ParseProbe(KindServe, "remote1", out); d.SelfUpdate != nil {
				t.Fatalf("SelfUpdate = %+v, want nil", *d.SelfUpdate)
			}
		})
	}
}
