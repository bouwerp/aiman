package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bouwerp/aiman/internal/selfupdate"
	"github.com/bouwerp/aiman/internal/server"
)

func TestWireHostUpdateNotifyGatewayTexts(t *testing.T) {
	m := &selfupdate.Manager{Version: "v0.42.0"}
	n := &hostNotifier{}
	wireHostUpdateNotify(context.Background(), m, n, "remote1")
	m.OnGatewayBehind("v0.41.0", "v0.42.0")
	m.OnApplied(selfupdate.Attempt{Kind: selfupdate.AttemptKindGateway, From: "v0.41.0", To: "v0.42.0", Status: selfupdate.AttemptOK})
	m.OnApplied(selfupdate.Attempt{Kind: selfupdate.AttemptKindGateway, From: "v0.41.0", To: "v0.42.0", Status: selfupdate.AttemptFailed, Error: "gateway install failed (exit 3)"})
	want := []string{
		"gateway v0.41.0 behind serve v0.42.0 on remote1 (aiman host update)",
		"gateway updated v0.41.0 → v0.42.0 on remote1",
		"gateway update failed: gateway install failed (exit 3) (to v0.42.0 on remote1)",
	}
	if len(n.got) != len(want) {
		t.Fatalf("emitted %+v", n.got)
	}
	for i, e := range n.got {
		if e.text != want[i] {
			t.Errorf("emit %d = %q, want %q", i, e.text, want[i])
		}
	}
}

func TestFormatHostVersionGateway(t *testing.T) {
	got := formatHostVersion(server.HostVersionResult{
		Version: "v0.42.0", Mode: "auto", Status: "current",
		Gateway: &server.HostGateway{Installed: true, Version: "v0.41.0", Running: true, Status: "behind"},
		Attempt: &server.HostUpdateTried{At: "2026-11-08T10:15:30Z", From: "v0.41.0", To: "v0.42.0", Status: "started", Kind: "gateway"},
	})
	want := "v0.42.0 · latest - · auto · current · never checked\ngateway v0.41.0 · behind · running\ngateway attempt v0.41.0 → v0.42.0 started at "
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("got %q, want prefix %q", got, want)
	}
	if got := formatHostUpdate(server.HostUpdateResult{Accepted: true, Reason: selfupdate.ReasonGateway, Target: "v0.42.0"}); got != "accepted: gateway v0.42.0\n" {
		t.Errorf("gateway accepted = %q", got)
	}
}

func TestReadHostGatewayPrefersInstallTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	gw, err := readHostGateway(context.Background())
	if err != nil || gw.Installed {
		t.Fatalf("no gateway: %+v, %v", gw, err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\necho 'aiman-gateway v0.41.0 (built 2026-11-01)'\n"
	if err := os.WriteFile(filepath.Join(bin, "aiman-gateway"), []byte(stub), 0o755); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	gw, err = readHostGateway(context.Background())
	if err != nil || !gw.Installed || gw.Version != "v0.41.0" {
		t.Fatalf("gateway = %+v, %v; want installed v0.41.0", gw, err)
	}
}
