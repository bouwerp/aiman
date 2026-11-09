//go:build acceptance

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// writeLaptopConfig puts a config with one remote under the test HOME that
// installFakeAWS set, and returns it loaded the way the command loads it.
func writeLaptopConfig(t *testing.T) *config.Config {
	t.Helper()
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".aiman")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "remotes:\n  - name: devbox\n    host: devbox.example\n    user: code\n    root: /home/code/repos\n    aws_delegations:\n      - profile: beta\n        source_profile: beta\n        sync_credentials: true\n        regions: [eu-west-1]\nactive_remote: devbox\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func reloadLaptopConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load after the command: %v", err)
	}
	return cfg
}

func delegationNamed(r config.Remote, profile string) *config.AWSDelegation {
	for _, d := range r.AllDelegations() {
		if d != nil && d.Profile == profile {
			return d
		}
	}
	return nil
}

func TestAWSPermissionSetApplyWritesTheDelegation(t *testing.T) {
	t.Run("remote gets the entry", func(t *testing.T) {
		installFakeAWS(t, "")
		cfg := writeLaptopConfig(t)
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox"}, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		saved := reloadLaptopConfig(t)
		d := delegationNamed(saved.Remotes[0], "acme-admin")
		if d == nil {
			t.Fatalf("no aws_delegations entry acme-admin on devbox: %+v", saved.Remotes[0].AllDelegations())
		}
		if d.SourceProfile != "acme-admin" || !d.SyncCredentials || strings.Join(d.Regions, ",") != "eu-west-1" {
			t.Errorf("entry = %+v, want source_profile acme-admin, sync_credentials true, regions eu-west-1", d)
		}
		if d.SSOStartURL != "https://d-123.awsapps.com/start" || d.SSORegion != "eu-west-1" || d.SSOAccountID != "123456789012" || d.SSORoleName != "aiman-delegated" {
			t.Errorf("entry = %+v, want the four SSO facts from the apply", d)
		}
		if other := delegationNamed(saved.Remotes[0], "beta"); other == nil || other.SourceProfile != "beta" {
			t.Errorf("the existing beta entry was lost or changed: %+v", other)
		}
		if !strings.Contains(out.String(), "devbox") || !strings.Contains(out.String(), "config.yaml") || !strings.Contains(out.String(), "Agent API") {
			t.Errorf("output does not say what was written and that the config must be pushed:\n%s", out.String())
		}
		if strings.Contains(out.String(), "Put this in remotes[].aws_delegation") {
			t.Errorf("output still asks for a manual edit after writing the entry:\n%s", out.String())
		}
	})

	t.Run("delegation-profile names the entry and reruns update it", func(t *testing.T) {
		installFakeAWS(t, "")
		cfg := writeLaptopConfig(t)
		args := []string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox", "--delegation-profile", "acme"}
		if err := runAWS(args, cfg, &bytes.Buffer{}); err != nil {
			t.Fatalf("first run: %v", err)
		}
		cfg = reloadLaptopConfig(t)
		args[5] = "eu-west-1,af-south-1"
		if err := runAWS(args, cfg, &bytes.Buffer{}); err != nil {
			t.Fatalf("second run: %v", err)
		}
		saved := reloadLaptopConfig(t)
		var count int
		for _, d := range saved.Remotes[0].AllDelegations() {
			if d.Profile == "acme" {
				count++
				if strings.Join(d.Regions, ",") != "eu-west-1,af-south-1" || d.SourceProfile != "acme-admin" {
					t.Errorf("entry = %+v, want the second run's regions and the admin profile as source", d)
				}
			}
		}
		if count != 1 {
			t.Errorf("acme entries = %d, want exactly one after two runs", count)
		}
		if delegationNamed(saved.Remotes[0], "acme-admin") != nil {
			t.Error("an entry named after the admin profile was written although --delegation-profile was given")
		}
	})

	t.Run("unknown remote is refused before any aws call", func(t *testing.T) {
		fake := installFakeAWS(t, "")
		cfg := writeLaptopConfig(t)
		err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "nowhere"}, cfg, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "nowhere") || !strings.Contains(err.Error(), "devbox") {
			t.Fatalf("err = %v, want one naming the unknown remote and the known ones", err)
		}
		if len(fake.calls) != 0 {
			t.Errorf("aws called %d times for an unknown remote", len(fake.calls))
		}
	})

	t.Run("remote without a config is refused", func(t *testing.T) {
		fake := installFakeAWS(t, "")
		err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox"}, nil, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "config") {
			t.Fatalf("err = %v, want one saying the config could not be loaded", err)
		}
		if len(fake.calls) != 0 {
			t.Errorf("aws called %d times with no config to write", len(fake.calls))
		}
	})

	t.Run("dry-run shows the entry and writes nothing", func(t *testing.T) {
		installFakeAWS(t, "")
		cfg := writeLaptopConfig(t)
		before, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".aiman", "config.yaml"))
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox", "--dry-run"}, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		after, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".aiman", "config.yaml"))
		if !bytes.Equal(before, after) {
			t.Error("dry-run changed config.yaml")
		}
		for _, want := range []string{"devbox", "profile: acme-admin", "sso_role_name: aiman-delegated"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("dry-run lacks %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("json reports the written entry", func(t *testing.T) {
		installFakeAWS(t, "")
		cfg := writeLaptopConfig(t)
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox", "--json"}, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, out.String())
		}
		written, _ := got["config"].(map[string]any)
		if written["remote"] != "devbox" || written["profile"] != "acme-admin" || !strings.HasSuffix(written["path"].(string), "config.yaml") {
			t.Errorf("config = %v, want remote, profile and path", written)
		}
		if _, ok := got["permission_set_arn"]; !ok {
			t.Errorf("json lost the apply result: %v", got)
		}
	})

	t.Run("a failed apply writes nothing", func(t *testing.T) {
		installFakeAWS(t, "sso-admin put-inline-policy-to-permission-set")
		cfg := writeLaptopConfig(t)
		err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox"}, cfg, &bytes.Buffer{})
		if err == nil {
			t.Fatal("runAWS succeeded, want the CLI error")
		}
		if delegationNamed(reloadLaptopConfig(t).Remotes[0], "acme-admin") != nil {
			t.Error("the delegation was written although the apply failed")
		}
	})
}
