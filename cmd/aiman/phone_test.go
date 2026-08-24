package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/tailscale"
)

func TestPhoneReportReady(t *testing.T) {
	st, err := tailscale.ParseStatusJSON([]byte(`{
	  "BackendState": "Running",
	  "Self": {"HostName": "regent0", "DNSName": "regent0.tail-example.ts.net.", "Online": true, "TailscaleIPs": ["100.64.1.2"]},
	  "Peer": {"k": {"HostName": "iphone16", "Online": true, "OS": "iOS"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	r := phoneReport(st, "code")
	if !r.Ready || r.Host != "regent0.tail-example.ts.net" || r.SSH != "ssh code@regent0.tail-example.ts.net" {
		t.Fatalf("%+v", r)
	}
	text := formatPhoneReport(r)
	if !strings.Contains(text, "Termius") || !strings.Contains(text, "iphone16") {
		t.Fatalf("%s", text)
	}
}

func TestPhoneReportNeedsLogin(t *testing.T) {
	st, err := tailscale.ParseStatusJSON([]byte(`{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	r := phoneReport(st, "code")
	if r.Ready || !strings.Contains(r.Next, "login") {
		t.Fatalf("%+v", r)
	}
}

func TestRunPhoneJSON(t *testing.T) {
	old := phoneTS
	t.Cleanup(func() { phoneTS = old })
	phoneTS = &tailscale.Client{
		LookPath: func(string) (string, error) { return "/bin/tailscale", nil },
		Exec: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"BackendState":"Running","Self":{"DNSName":"h.tail-example.ts.net.","Online":true,"TailscaleIPs":["100.1.2.3"]}}`), nil
		},
	}
	if err := runPhone([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
}
