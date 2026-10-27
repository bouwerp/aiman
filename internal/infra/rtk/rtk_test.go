package rtk

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallScriptShape(t *testing.T) {
	s := InstallScript()
	for _, want := range []string{
		InstallURL,
		"RTK_TELEMETRY_DISABLED=1",
		"RTK_EDITION=oss",
		"--auto-patch",
		"rtk init --global --codex",
		"RTK_WRONG_BINARY=1",
		"rtk gain --all --format json",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("install script missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "sudo") {
		t.Fatalf("install script must not use sudo:\n%s", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "rtk gain") && !strings.Contains(line, "--format json") {
			t.Fatalf("rtk gain without --format json: %q", line)
		}
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(s)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n rejected the script: %v\n%s", err, out)
	}
}

func TestParseInstall(t *testing.T) {
	r := ParseInstall("RTK_VERSION=rtk 0.40.1\nRTK_HOOK=claude ok\nRTK_HOOK=codex failed\n")
	if r.Version != "rtk 0.40.1" {
		t.Fatalf("version %q", r.Version)
	}
	if len(r.Hooks) != 2 || !r.Hooks["claude"] || r.Hooks["codex"] {
		t.Fatalf("hooks %+v", r.Hooks)
	}
	if r.WrongBinary {
		t.Fatal("WrongBinary set without marker")
	}
	if !ParseInstall("noise\nRTK_WRONG_BINARY=1\n").WrongBinary {
		t.Fatal("WrongBinary not parsed")
	}
}

const gainSample = `{"summary":{"total_commands":300,"total_input":1500000,"total_output":300000,"total_saved":1200000,"avg_savings_pct":80.0},"daily":[{"date":"2026-10-21","commands":10}]}`

func TestParseGain(t *testing.T) {
	g, err := ParseGain([]byte(gainSample))
	if err != nil {
		t.Fatal(err)
	}
	want := Gain{Commands: 300, InputTokens: 1500000, OutputTokens: 300000, SavedTokens: 1200000, SavingsPct: 80.0}
	if g != want {
		t.Fatalf("gain %+v, want %+v", g, want)
	}
	if _, err := ParseGain([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if _, err := ParseGain([]byte(`{"daily":[]}`)); err == nil {
		t.Fatal("expected error when summary is missing")
	}
}

type fakeExecutor struct {
	out string
	err error
	cmd string
}

func (f *fakeExecutor) Execute(_ context.Context, cmd string) (string, error) {
	f.cmd = cmd
	return f.out, f.err
}

func TestFetchGainMissing(t *testing.T) {
	ex := &fakeExecutor{out: "RTK_MISSING\n"}
	g, installed, err := FetchGain(context.Background(), ex)
	if err != nil || installed || g != (Gain{}) {
		t.Fatalf("got %+v installed=%v err=%v", g, installed, err)
	}
	if !strings.Contains(ex.cmd, "rtk gain --all --format json") {
		t.Fatalf("command %q", ex.cmd)
	}
}

func TestFetchGainInstalled(t *testing.T) {
	g, installed, err := FetchGain(context.Background(), &fakeExecutor{out: gainSample + "\n"})
	if err != nil || !installed || g.SavedTokens != 1200000 {
		t.Fatalf("got %+v installed=%v err=%v", g, installed, err)
	}
}

func TestFetchGainExecuteError(t *testing.T) {
	_, installed, err := FetchGain(context.Background(), &fakeExecutor{err: errors.New("ssh down")})
	if err == nil || installed {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
}
