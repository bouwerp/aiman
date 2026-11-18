package main

import (
	"testing"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

func TestParseEdgeRelay(t *testing.T) {
	got, err := parseEdgeRelay("web=8080=app:8080")
	if err != nil || got != (edge.RelaySpec{Name: "web", Port: 8080, Target: "app:8080"}) {
		t.Fatalf("parseEdgeRelay = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "web", "web=8080", "web=x=app:1", "=1=app:1", "web=1=", "web=-1=app:1"} {
		if _, err := parseEdgeRelay(bad); err == nil {
			t.Errorf("parseEdgeRelay(%q) accepted", bad)
		}
	}
}

func TestParseEdgeFlagsNeedsControlTokenOuter(t *testing.T) {
	base := []string{"--control", "/c/edge.sock", "--token-file", "/c/token", "--outer", "172.31.0.0/16"}
	opts, err := parseEdgeFlags(append(base, "--inner", "172.30.0.0/24,fd00::/64", "--relay", "web=8080=app:8080", "--relay", "db=5432=db:5432", "--dap-relay", "dap=10003=10.232.0.2:2345"))
	if err != nil {
		t.Fatalf("parseEdgeFlags: %v", err)
	}
	if opts.control != "/c/edge.sock" || opts.tokenFile != "/c/token" || opts.outer != "172.31.0.0/16" {
		t.Fatalf("opts = %+v", opts)
	}
	if len(opts.inner) != 2 || len(opts.relays) != 3 || opts.relays[1].Name != "db" || opts.relays[1].Kind != "" || opts.relays[2].Kind != edge.KindDAP {
		t.Fatalf("inner/relays = %+v / %+v", opts.inner, opts.relays)
	}
	for i := 0; i < len(base); i += 2 {
		missing := append(append([]string{}, base[:i]...), base[i+2:]...)
		if _, err := parseEdgeFlags(missing); err == nil {
			t.Errorf("parseEdgeFlags without %s accepted", base[i])
		}
	}
	if _, err := parseEdgeFlags(append(base, "--inner", "nonsense")); err == nil {
		t.Error("bad --inner accepted")
	}
	if _, err := parseEdgeFlags(append(base, "extra")); err == nil {
		t.Error("positional argument accepted")
	}
}

func TestParseEdgeFlagsAWS(t *testing.T) {
	base := []string{"--control", "/c/edge.sock", "--token-file", "/c/token", "--outer", "172.31.0.0/16", "--inner", "172.30.0.0/24"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
	opts, err := parseEdgeFlags(with("--aws-tap", "aws=aws-emu:4566",
		"--proxy", "172.30.0.0/24", "--ca-dir", "/ca", "--block-host-suffix", "amazonaws.com", "--block-host-suffix", "api.aws"))
	if err != nil {
		t.Fatalf("parseEdgeFlags: %v", err)
	}
	if len(opts.awsTaps) != 1 || opts.awsTaps[0] != (edge.RelaySpec{Name: "aws", Port: edge.AWSTapPort, Target: "aws-emu:4566"}) {
		t.Fatalf("taps = %+v", opts.awsTaps)
	}
	if len(opts.proxy.blocks) != 2 || opts.proxy.blocks[1] != "api.aws" {
		t.Fatalf("blocks = %v", opts.proxy.blocks)
	}
	for _, bad := range [][]string{
		with("--aws-tap", "aws"),
		with("--aws-tap", "=x:1"),
		with("--aws-tap", "a=a-emu:4566", "--aws-tap", "b=b-emu:4566"),
		{"--control", "/c", "--token-file", "/t", "--outer", "172.31.0.0/16", "--aws-tap", "aws=aws-emu:4566"},
		with("--block-host-suffix", "amazonaws.com"),
		with("--proxy", "172.30.0.0/24", "--ca-dir", "/ca", "--block-host-suffix", "a b"),
	} {
		if _, err := parseEdgeFlags(bad); err == nil {
			t.Errorf("parseEdgeFlags(%q) accepted", bad)
		}
	}
}
