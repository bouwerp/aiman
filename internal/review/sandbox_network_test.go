package review

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func sandboxNetworkMode(t *testing.T, network string) string {
	t.Helper()
	sb := Sandbox{
		ID: "rev1", Dir: "/r/sandbox", Src: "/r/src", Out: "/r/out",
		Image: "golang:1.26", Network: network,
	}
	raw, err := Compose(sb, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var doc composeDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse compose: %v\n%s", err, raw)
	}
	svc, ok := doc.Services["sandbox"]
	if !ok {
		t.Fatalf("no sandbox service:\n%s", raw)
	}
	return svc.NetworkMode
}

// PR code runs in the sandbox, so it is offline unless review.sandbox.network
// says otherwise.
func TestComposeDefaultsToNoNetwork(t *testing.T) {
	if got := sandboxNetworkMode(t, ""); got != "none" {
		t.Fatalf("unset review.sandbox.network: network_mode %q, want none", got)
	}
}

func TestComposeConfiguredNetworkWins(t *testing.T) {
	if got := sandboxNetworkMode(t, "none"); got != "none" {
		t.Fatalf("network none: network_mode %q, want none", got)
	}
	if got := sandboxNetworkMode(t, "bridge"); got == "none" {
		t.Fatalf("network bridge: network_mode %q, the configured network must win", got)
	}
}
