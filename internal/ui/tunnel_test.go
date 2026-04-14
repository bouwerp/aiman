package ui

import "testing"

func TestParseTunnelSpec(t *testing.T) {
	tunnel, err := parseTunnelSpec("5173:8080")
	if err != nil {
		t.Fatalf("parseTunnelSpec returned error: %v", err)
	}
	if tunnel.LocalPort != 5173 || tunnel.RemotePort != 8080 {
		t.Fatalf("unexpected tunnel parsed: %+v", tunnel)
	}
}

func TestParseTunnelSpecRejectsInvalid(t *testing.T) {
	if _, err := parseTunnelSpec("bad"); err == nil {
		t.Fatal("expected parse error for invalid spec")
	}
	if _, err := parseTunnelSpec("0:8080"); err == nil {
		t.Fatal("expected parse error for invalid local port")
	}
	if _, err := parseTunnelSpec("8080:70000"); err == nil {
		t.Fatal("expected parse error for invalid remote port")
	}
}
