package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

// P3 (docs/specs/aws-permission-set-apply.md, rev 8): the command works from
// an admin profile that is plain IAM credentials.

// discoveryAWS answers a first-time apply from a profile without SSO keys:
// ALL_REGIONS discovery, the instance type check and the rev 5 sequence.
type discoveryAWS struct {
	happyAWS
	oldCLI bool // list-instances --region-scope is an unknown option
}

func (d *discoveryAWS) Run(ctx context.Context, args ...string) ([]byte, error) {
	o := awsOp(args)
	switch o {
	case "sso-admin list-instances":
		d.calls = append(d.calls, args)
		if scope := awsFlag(args, "--region-scope"); scope != "" {
			if d.oldCLI {
				return nil, errors.New("\nUnknown options: --region-scope, " + scope + "\n")
			}
			return []byte(`{"Instances":[{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-123","OwnerAccountId":"123456789012","PrimaryRegion":"eu-west-1","Status":"ACTIVE"}]}`), nil
		}
		if r := awsFlag(args, "--region"); r != "eu-west-1" && r != "af-south-1" {
			return []byte(`{"Instances":[]}`), nil
		}
		return []byte(`{"Instances":[{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-123","OwnerAccountId":"123456789012"}]}`), nil
	case "account list-regions":
		d.calls = append(d.calls, args)
		return []byte(`{"Regions":[{"RegionName":"us-east-1","RegionOptStatus":"ENABLED_BY_DEFAULT"},{"RegionName":"eu-west-1","RegionOptStatus":"ENABLED_BY_DEFAULT"}]}`), nil
	case "sso-admin describe-instance":
		d.calls = append(d.calls, args)
		return []byte(`{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-123","OwnerAccountId":"123456789012","PermissionSetsEnabled":true,"PrimaryRegion":"eu-west-1","Status":"ACTIVE"}`), nil
	case "organizations describe-organization":
		d.calls = append(d.calls, args)
		return []byte(`{"Organization":{"Id":"o-abc","MasterAccountId":"123456789012"}}`), nil
	case "sts get-caller-identity":
		d.calls = append(d.calls, args)
		return []byte(`{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/admin","UserId":"AIDAEXAMPLE"}`), nil
	}
	return d.happyAWS.Run(ctx, args...)
}

func awsFlag(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// verifyCalls records what the command passed to the start URL verifier.
type verifyCalls struct {
	calls []string
	err   error
}

func (v *verifyCalls) verify(_ context.Context, region, url string) error {
	v.calls = append(v.calls, region+" "+url)
	return v.err
}

// installIAMProfile points the command at a discoveryAWS fake and a HOME whose
// ~/.aws/config holds an admin profile with no sso_* keys, and replaces the
// start URL verifier.
func installIAMProfile(t *testing.T, oldCLI bool, verifyErr error) (*discoveryAWS, *verifyCalls) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte("[profile acme-admin]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &discoveryAWS{oldCLI: oldCLI}
	prevCLI := newAWSCLI
	newAWSCLI = func(profile string) awsdelegation.AWSCLI {
		fake.profile = profile
		return fake
	}
	rec := &verifyCalls{err: verifyErr}
	prevVerify := verifyStartURL
	verifyStartURL = rec.verify
	t.Cleanup(func() {
		newAWSCLI = prevCLI
		verifyStartURL = prevVerify
	})
	return fake, rec
}

func TestAWSPermissionSetApplyFromAnIAMProfile(t *testing.T) {
	t.Run("discovers and prints the derived facts", func(t *testing.T) {
		fake, rec := installIAMProfile(t, false, nil)
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--user", "pieter"}, nil, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		for _, want := range []string{
			"sso_start_url: https://d-123.awsapps.com/start",
			"sso_region: eu-west-1",
			"sso_account_id: 123456789012",
			"sso_role_name: aiman-delegated",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
		if len(rec.calls) != 1 || rec.calls[0] != "eu-west-1 https://d-123.awsapps.com/start" {
			t.Errorf("verifier calls = %v, want the discovered region and derived URL once", rec.calls)
		}
		var scoped int
		for _, c := range fake.calls {
			o := awsOp(c)
			switch {
			case o == "sso-admin list-instances" && awsFlag(c, "--region-scope") == "ALL_REGIONS":
				scoped++
				if awsFlag(c, "--region") != "us-east-1" {
					t.Errorf("ALL_REGIONS call not in the partition home: %v", c)
				}
			case strings.HasPrefix(o, "sso-admin") || strings.HasPrefix(o, "identitystore"):
				if awsFlag(c, "--region") != "eu-west-1" {
					t.Errorf("Identity Center call without the discovered region: %v", c)
				}
			case o == "account list-regions":
				t.Errorf("the scan ran although ALL_REGIONS answered: %v", c)
			}
		}
		if scoped != 1 {
			t.Errorf("ALL_REGIONS asked %d times, want once", scoped)
		}
	})

	t.Run("old CLI falls back to the scan", func(t *testing.T) {
		fake, _ := installIAMProfile(t, true, nil)
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--user", "pieter"}, nil, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		var listRegions, scanned int
		for _, c := range fake.calls {
			switch {
			case awsOp(c) == "account list-regions":
				listRegions++
			case awsOp(c) == "sso-admin list-instances" && awsFlag(c, "--region-scope") == "":
				scanned++
			}
		}
		if listRegions != 1 || scanned != 2 {
			t.Errorf("list-regions %d, per-region list-instances %d; want 1 and 2 (every listed region)", listRegions, scanned)
		}
		if !strings.Contains(out.String(), "sso_region: eu-west-1") {
			t.Errorf("output lacks the region found by the scan:\n%s", out.String())
		}
	})

	t.Run("sso-region and start-url skip discovery and derivation", func(t *testing.T) {
		fake, rec := installIAMProfile(t, false, nil)
		var out bytes.Buffer
		args := []string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--user", "pieter", "--sso-region", "af-south-1", "--start-url", "https://acme.awsapps.com/start"}
		if err := runAWS(args, nil, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		for _, c := range fake.calls {
			if awsFlag(c, "--region-scope") != "" || awsOp(c) == "account list-regions" {
				t.Errorf("discovery ran although --sso-region was given: %v", c)
			}
			if o := awsOp(c); (strings.HasPrefix(o, "sso-admin") || strings.HasPrefix(o, "identitystore")) && awsFlag(c, "--region") != "af-south-1" {
				t.Errorf("Identity Center call without the given region: %v", c)
			}
		}
		if !strings.Contains(out.String(), "sso_start_url: https://acme.awsapps.com/start") || !strings.Contains(out.String(), "sso_region: af-south-1") {
			t.Errorf("output lacks the given facts:\n%s", out.String())
		}
		if len(rec.calls) != 1 || rec.calls[0] != "af-south-1 https://acme.awsapps.com/start" {
			t.Errorf("verifier calls = %v, want the given URL verified in the given region", rec.calls)
		}
	})

	t.Run("a rejected start URL fails with --start-url advice", func(t *testing.T) {
		installIAMProfile(t, false, awsdelegation.ErrStartURLRejected)
		var out bytes.Buffer
		err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--user", "pieter"}, nil, &out)
		if err == nil || !strings.Contains(err.Error(), "--start-url") || !strings.Contains(err.Error(), "https://d-123.awsapps.com/start") {
			t.Fatalf("err = %v, want the rejection naming the URL and --start-url", err)
		}
	})

	t.Run("remote writes the discovered facts", func(t *testing.T) {
		installIAMProfile(t, false, nil)
		cfg := writeLaptopConfig(t)
		var out bytes.Buffer
		args := []string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--user", "pieter", "--remote", "devbox", "--delegation-profile", "acme"}
		if err := runAWS(args, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		d := delegationNamed(reloadLaptopConfig(t).Remotes[0], "acme")
		if d == nil {
			t.Fatal("no aws_delegations entry acme on devbox")
		}
		if d.SSOStartURL != "https://d-123.awsapps.com/start" || d.SSORegion != "eu-west-1" || d.SSOAccountID != "123456789012" || d.SSORoleName != "aiman-delegated" || d.SourceProfile != "acme-admin" {
			t.Errorf("entry = %+v, want the discovered and derived facts with the admin profile as source", d)
		}
	})

	t.Run("dry-run plans discovery, calls nothing and shows placeholders", func(t *testing.T) {
		fake, rec := installIAMProfile(t, false, nil)
		cfg := writeLaptopConfig(t)
		var out bytes.Buffer
		args := []string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--user", "pieter", "--remote", "devbox", "--dry-run"}
		if err := runAWS(args, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		if len(fake.calls) != 0 || len(rec.calls) != 0 {
			t.Errorf("dry-run made %d aws calls and %d verifications", len(fake.calls), len(rec.calls))
		}
		for _, want := range []string{
			"--region-scope ALL_REGIONS",
			"account list-regions",
			"sso-admin describe-instance",
			"organizations describe-organization",
			"verification",
			"sso_start_url: <derived from the identity store id and verified>",
			"sso_region: <discovered>",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("dry-run lacks %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("usage names the new flags", func(t *testing.T) {
		var out bytes.Buffer
		err := runAWS([]string{"permission-set-apply", "--regions", "eu-west-1"}, nil, &out)
		if !errors.Is(err, errUsage) {
			t.Fatalf("err = %v, want errUsage", err)
		}
		for _, want := range []string{"--sso-region", "--start-url"} {
			if !strings.Contains(awsUsage, want) {
				t.Errorf("awsUsage lacks %q", want)
			}
		}
	})
}
