//go:build acceptance

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// happyAWS answers the aws calls of a complete first-time apply.
type happyAWS struct {
	profile string
	calls   [][]string
	fail    string // "<service> <subcommand>" that fails, when set
}

func awsOp(args []string) string {
	var words []string
	for i := 0; i < len(args) && len(words) < 2; i++ {
		if strings.HasPrefix(args[i], "--") {
			i++
			continue
		}
		words = append(words, args[i])
	}
	return strings.Join(words, " ")
}

func (h *happyAWS) Run(_ context.Context, args ...string) ([]byte, error) {
	h.calls = append(h.calls, args)
	o := awsOp(args)
	if o == h.fail {
		return nil, errors.New("An error occurred (AccessDeniedException) when calling the operation: not authorized")
	}
	answers := map[string]string{
		"sso-admin list-instances":                              `{"Instances":[{"InstanceArn":"arn:aws:sso:::instance/ssoins-1","IdentityStoreId":"d-123"}]}`,
		"sts get-caller-identity":                               `{"Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_Admin_abc/pieter@acme.io","UserId":"AROA:pieter@acme.io"}`,
		"sso-admin list-permission-sets":                        `{"PermissionSets":[]}`,
		"sso-admin create-permission-set":                       `{"PermissionSet":{"PermissionSetArn":"arn:aws:sso:::permissionSet/ssoins-1/ps-new","Name":"aiman-delegated","SessionDuration":"PT12H"}}`,
		"sso-admin put-inline-policy-to-permission-set":         ``,
		"identitystore list-users":                              `{"Users":[{"UserId":"u-1","UserName":"pieter@acme.io"}]}`,
		"sso-admin list-account-assignments":                    `{"AccountAssignments":[]}`,
		"sso-admin create-account-assignment":                   `{"AccountAssignmentCreationStatus":{"RequestId":"req-1","Status":"IN_PROGRESS"}}`,
		"sso-admin describe-account-assignment-creation-status": `{"AccountAssignmentCreationStatus":{"RequestId":"req-1","Status":"SUCCEEDED"}}`,
	}
	out, ok := answers[o]
	if !ok {
		return nil, errors.New("unexpected aws call: " + strings.Join(args, " "))
	}
	return []byte(out), nil
}

// installFakeAWS points the apply command at a fake CLI and gives the test a
// HOME whose ~/.aws/config holds an SSO admin profile.
func installFakeAWS(t *testing.T, fail string) *happyAWS {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[profile acme-admin]\nsso_start_url = https://d-123.awsapps.com/start\nsso_region = eu-west-1\nsso_account_id = 123456789012\nsso_role_name = AdministratorAccess\n"
	if err := os.WriteFile(filepath.Join(home, ".aws", "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &happyAWS{fail: fail}
	prev := newAWSCLI
	newAWSCLI = func(profile string) awsdelegation.AWSCLI {
		fake.profile = profile
		return fake
	}
	t.Cleanup(func() { newAWSCLI = prev })
	return fake
}

func TestAWSPermissionSetApplyCommand(t *testing.T) {
	t.Run("profile is required", func(t *testing.T) {
		installFakeAWS(t, "")
		var out bytes.Buffer
		err := runAWS([]string{"permission-set-apply", "--regions", "eu-west-1"}, nil, &out)
		if !errors.Is(err, errUsage) {
			t.Fatalf("err = %v, want errUsage", err)
		}
	})

	t.Run("applies and prints the delegation", func(t *testing.T) {
		fake := installFakeAWS(t, "")
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1"}, nil, &out); err != nil {
			t.Fatalf("runAWS: %v\n%s", err, out.String())
		}
		if fake.profile != "acme-admin" {
			t.Errorf("CLI built for profile %q, want acme-admin", fake.profile)
		}
		for _, want := range []string{
			"sso_start_url: https://d-123.awsapps.com/start",
			"sso_region: eu-west-1",
			"sso_account_id: 123456789012",
			"sso_role_name: aiman-delegated",
			"remotes[].aws_delegation",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output lacks %q:\n%s", want, out.String())
			}
		}
		if len(fake.calls) < 8 {
			t.Errorf("aws called %d times, want the full sequence", len(fake.calls))
		}
	})

	t.Run("dry-run prints the aws commands and calls nothing", func(t *testing.T) {
		fake := installFakeAWS(t, "")
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--dry-run"}, nil, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		if len(fake.calls) != 0 {
			t.Errorf("dry-run called aws %d times", len(fake.calls))
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		var awsLines int
		for _, l := range lines {
			if strings.HasPrefix(l, "aws --profile acme-admin ") {
				awsLines++
			}
		}
		if awsLines < 8 {
			t.Errorf("dry-run printed %d aws lines, want the sequence:\n%s", awsLines, out.String())
		}
		for _, want := range []string{"sso-admin create-permission-set", "--session-duration PT12H", "identitystore list-users", "<permission-set-arn>"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("dry-run lacks %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		installFakeAWS(t, "")
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--json"}, nil, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%s", err, out.String())
		}
		for _, key := range []string{"instance_arn", "identity_store_id", "permission_set_arn", "principal_id", "user_name", "account_id", "created_permission_set", "created_assignment", "delegation"} {
			if _, ok := got[key]; !ok {
				t.Errorf("json lacks %q: %v", key, got)
			}
		}
		del, _ := got["delegation"].(map[string]any)
		if del["sso_role_name"] != "aiman-delegated" || del["sso_account_id"] != "123456789012" || del["sso_start_url"] != "https://d-123.awsapps.com/start" {
			t.Errorf("delegation = %v", del)
		}
	})

	t.Run("regions from config", func(t *testing.T) {
		fake := installFakeAWS(t, "")
		cfg := &config.Config{Remotes: []config.Remote{{
			Name: "one", Host: "h1",
			AWSDelegation: &config.AWSDelegation{Profile: "default", SyncCredentials: true, Regions: []string{"af-south-1"}},
		}}}
		var out bytes.Buffer
		if err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin"}, cfg, &out); err != nil {
			t.Fatalf("runAWS: %v", err)
		}
		var put []string
		for _, c := range fake.calls {
			if awsOp(c) == "sso-admin put-inline-policy-to-permission-set" {
				put = c
			}
		}
		if put == nil || !strings.Contains(strings.Join(put, " "), "af-south-1") {
			t.Errorf("inline policy does not use the config's region: %v", put)
		}
	})

	t.Run("no regions anywhere", func(t *testing.T) {
		fake := installFakeAWS(t, "")
		var out bytes.Buffer
		err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin"}, nil, &out)
		if err == nil || !strings.Contains(err.Error(), "--regions") {
			t.Fatalf("err = %v, want one naming --regions", err)
		}
		if len(fake.calls) != 0 {
			t.Errorf("aws called %d times without regions", len(fake.calls))
		}
	})

	t.Run("a failing step is the error", func(t *testing.T) {
		installFakeAWS(t, "sso-admin put-inline-policy-to-permission-set")
		var out bytes.Buffer
		err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1"}, nil, &out)
		if err == nil || !strings.Contains(err.Error(), "AccessDeniedException") {
			t.Fatalf("err = %v, want the CLI's error", err)
		}
		if strings.Contains(out.String(), "Done.") {
			t.Errorf("output claims success after a failure:\n%s", out.String())
		}
	})

	t.Run("usage lists both subcommands", func(t *testing.T) {
		for _, want := range []string{"permission-set-policy", "permission-set-apply", "--dry-run"} {
			if !strings.Contains(awsUsage, want) {
				t.Errorf("awsUsage lacks %q", want)
			}
		}
	})
}
