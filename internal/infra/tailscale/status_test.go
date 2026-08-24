package tailscale

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const sampleStatus = `{
  "BackendState": "Running",
  "AuthURL": "",
  "MagicDNSSuffix": "tail-example.ts.net",
  "CurrentTailnet": {
    "Name": "user@example.com",
    "MagicDNSEnabled": true,
    "MagicDNSSuffix": "tail-example.ts.net"
  },
  "Self": {
    "HostName": "regent0",
    "DNSName": "regent0.tail-example.ts.net.",
    "Online": true,
    "OS": "linux",
    "TailscaleIPs": ["100.64.1.2", "fd7a:115c:a1e0::1"]
  },
  "Peer": {
    "nodekey:abc": {"HostName": "iphone16", "Online": true, "OS": "iOS"},
    "nodekey:def": {"HostName": "macbook", "Online": true, "OS": "macOS"},
    "nodekey:ghi": {"HostName": "old-phone", "Online": false, "OS": "iOS"}
  }
}`

func TestParseStatusJSON(t *testing.T) {
	st, err := ParseStatusJSON([]byte(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Ready() || st.SSHHost() != "regent0.tail-example.ts.net" {
		t.Fatalf("%+v", st)
	}
	if st.IPv4 != "100.64.1.2" || st.IPv6 == "" {
		t.Fatalf("ips %+v", st)
	}
	if !st.MagicDNSEnabled || st.Tailnet != "user@example.com" {
		t.Fatalf("tailnet %+v", st)
	}
	if len(st.MobilePeersOnline) != 1 || st.MobilePeersOnline[0] != "iphone16" {
		t.Fatalf("mobile %+v", st.MobilePeersOnline)
	}
}

func TestParseStatusJSONNeedsLogin(t *testing.T) {
	st, err := ParseStatusJSON([]byte(`{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if st.Ready() || st.AuthURL == "" {
		t.Fatalf("%+v", st)
	}
}

func TestClientProbeMissingBinary(t *testing.T) {
	c := &Client{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	_, err := c.Probe(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("%v", err)
	}
}

func TestClientProbeUsesExec(t *testing.T) {
	c := &Client{
		LookPath: func(string) (string, error) { return "/usr/bin/tailscale", nil },
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "tailscale" || len(args) != 2 || args[0] != "status" {
				t.Fatalf("%s %v", name, args)
			}
			return []byte(sampleStatus), nil
		},
	}
	st, err := c.Probe(context.Background())
	if err != nil || !st.Ready() {
		t.Fatalf("%+v %v", st, err)
	}
}
