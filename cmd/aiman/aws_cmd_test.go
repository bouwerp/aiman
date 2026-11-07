package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestAWSPermissionSetPolicyCommand(t *testing.T) {
	t.Run("regions flag prints the policy", func(t *testing.T) {
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-policy", "--regions", "us-east-2,eu-west-1"}, nil, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		want := awsdelegation.PermissionSetPolicy([]string{"us-east-2", "eu-west-1"})
		if want == "" || strings.TrimSpace(out.String()) != strings.TrimSpace(want) {
			t.Fatalf("stdout = %q, want PermissionSetPolicy output %q", out.String(), want)
		}
		if !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Fatalf("stdout is not JSON:\n%s", out.String())
		}
	})

	t.Run("steps", func(t *testing.T) {
		var out bytes.Buffer
		args := []string{"permission-set-policy", "--regions", "us-east-2", "--name", "team-delegated", "--account", "123456789012", "--steps"}
		if err := runAWS(args, nil, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		for _, want := range []string{"team-delegated", "123456789012", "PT12H"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("steps missing %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("regions from config", func(t *testing.T) {
		cfg := &config.Config{Remotes: []config.Remote{{
			Name: "one", Host: "h1",
			AWSDelegation: &config.AWSDelegation{Profile: "default", SyncCredentials: true, Regions: []string{"eu-west-1"}},
		}}}
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-policy"}, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		want := awsdelegation.PermissionSetPolicy([]string{"eu-west-1"})
		if want == "" || strings.TrimSpace(out.String()) != strings.TrimSpace(want) {
			t.Fatalf("stdout = %q, want policy for config regions %q", out.String(), want)
		}
	})

	t.Run("no regions anywhere refuses", func(t *testing.T) {
		for _, cfg := range []*config.Config{nil, {}} {
			var out bytes.Buffer
			err := runAWS([]string{"permission-set-policy"}, cfg, &out)
			if err == nil || !strings.Contains(err.Error(), "--regions") {
				t.Fatalf("err = %v, want a refusal naming --regions", err)
			}
			if out.Len() != 0 {
				t.Fatalf("refusal printed to stdout: %q", out.String())
			}
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		var out bytes.Buffer
		if err := runAWS([]string{"nope"}, nil, &out); err == nil {
			t.Fatal("want an error for an unknown subcommand")
		}
	})
}
