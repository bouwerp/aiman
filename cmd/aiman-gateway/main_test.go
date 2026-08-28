package main

import "testing"

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.funnel || opts.port != 8080 || opts.hostname != "aiman-gateway" {
		t.Fatalf("%+v", opts)
	}
}

func TestParseFlagsFunnelPort(t *testing.T) {
	opts, err := parseFlags([]string{"--funnel"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.funnel || opts.port != 443 {
		t.Fatalf("%+v", opts)
	}
}

func TestParseFlagsAllowLoginRepeatable(t *testing.T) {
	opts, err := parseFlags([]string{"--allow-login", "a@x", "--allow-login", "b@x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.allow) != 2 || opts.allow[0] != "a@x" || opts.allow[1] != "b@x" {
		t.Fatalf("%v", opts.allow)
	}
}

func TestParseFlagsVersion(t *testing.T) {
	opts, err := parseFlags([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.version {
		t.Fatal("expected version")
	}
}
