package remotesvc

import (
	"os/exec"
	"strings"
	"testing"
)

// Acceptance tests for daemon-control piece A (on-host restart and log
// scripts). Do not edit.

func bashSyntaxOK(t *testing.T, name, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: bash -n: %v %s\n%s", name, err, out, script)
	}
}

func TestOnHostRestartScriptRestartsInPlace(t *testing.T) {
	for _, k := range []Kind{KindServe, KindGateway, KindTrigger} {
		s := RestartScript(k)
		bashSyntaxOK(t, string(k), s)
		if !strings.Contains(s, "systemctl --user restart aiman-"+string(k)) {
			t.Errorf("%s: no systemctl restart:\n%s", k, s)
		}
		for _, bad := range []string{"daemon-reload", ".service <<", "enable-linger", "pkill"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s: restart must not rewrite the unit or pkill (%q):\n%s", k, bad, s)
			}
		}
		if !strings.Contains(s, string(k)+".pid") {
			t.Errorf("%s: no nohup fallback through the pid file:\n%s", k, s)
		}
	}
}

func TestOnHostLogsScriptReadsJournalElseFile(t *testing.T) {
	s := LogsScript(KindGateway, 150)
	bashSyntaxOK(t, "logs", s)
	for _, want := range []string{"journalctl --user -u aiman-gateway", "-n 150", "--no-pager", "tail -n 150", ".aiman/gateway.log"} {
		if !strings.Contains(s, want) {
			t.Errorf("logs script lacks %q:\n%s", want, s)
		}
	}
}
