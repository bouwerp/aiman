//go:build acceptance

package awsdelegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// P3 (docs/specs/aws-permission-set-apply.md, rev 8): an admin profile without
// SSO keys discovers the Identity Center region, refuses an account instance
// and derives and verifies the access portal URL.

const iamProfileSection = "[profile acme-admin]\nregion = us-east-1\n"

// noSSOSpec is fastSpec with a profile that holds no sso_* keys.
func noSSOSpec(t *testing.T) ApplySpec {
	t.Helper()
	spec := fastSpec(t)
	spec.AWSConfigPath = writeAWSConfig(t, iamProfileSection)
	return spec
}

// discovery wraps a world with the calls P3 adds. Fields left zero give the
// happy path: one instance in eu-west-1 with permission sets enabled, owned by
// the organization's management account.
type discovery struct {
	w *world

	allRegionsErr string           // non-empty: list-instances --region-scope fails with this stderr line
	allRegions    []map[string]any // answer to list-instances --region-scope ALL_REGIONS; nil: the default instance with PrimaryRegion eu-west-1
	byRegion      map[string][]map[string]any
	regionErr     map[string]string // list-instances --region <r> fails with this line
	regions       []string          // account list-regions answer
	regionsErr    string            // non-empty: list-regions fails

	psEnabled   *bool  // describe-instance PermissionSetsEnabled; nil: field absent
	describeErr string // non-empty: describe-instance fails
	orgErr      string // non-empty: describe-organization fails with this line
	master      string // MasterAccountId; empty: the default instance owner
	owner       string // OwnerAccountId of the instance; empty: the caller account

	scoped, scanned []string // regions asked with --region-scope and without
}

func newDiscovery(w *world) *discovery {
	t := true
	return &discovery{w: w, psEnabled: &t}
}

func (d *discovery) instance() map[string]any {
	owner := d.owner
	if owner == "" {
		owner = d.w.callerAccount
	}
	return map[string]any{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": owner, "PrimaryRegion": "eu-west-1", "Status": "ACTIVE"}
}

func (d *discovery) handle(args []string) (string, error) {
	j := func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	}
	switch op(args) {
	case "sso-admin list-instances":
		region := flagValue(args, "--region")
		if scope := flagValue(args, "--region-scope"); scope != "" {
			d.scoped = append(d.scoped, region)
			if scope != "ALL_REGIONS" {
				return "", fmt.Errorf("An error occurred (ValidationException) when calling the ListInstances operation: bad RegionScope %s", scope)
			}
			if d.allRegionsErr != "" {
				return "", errors.New(d.allRegionsErr)
			}
			if d.allRegions != nil {
				return j(map[string]any{"Instances": d.allRegions})
			}
			return j(map[string]any{"Instances": []map[string]any{d.instance()}})
		}
		d.scanned = append(d.scanned, region)
		if msg, ok := d.regionErr[region]; ok {
			return "", errors.New(msg)
		}
		if d.byRegion != nil {
			return j(map[string]any{"Instances": d.byRegion[region]})
		}
		return d.w.handle(args)
	case "account list-regions":
		if d.regionsErr != "" {
			return "", errors.New(d.regionsErr)
		}
		var rows []map[string]string
		for _, r := range d.regions {
			rows = append(rows, map[string]string{"RegionName": r, "RegionOptStatus": "ENABLED_BY_DEFAULT"})
		}
		return j(map[string]any{"Regions": rows})
	case "sso-admin describe-instance":
		if d.describeErr != "" {
			return "", errors.New(d.describeErr)
		}
		in := d.instance()
		if d.psEnabled != nil {
			in["PermissionSetsEnabled"] = *d.psEnabled
		}
		return j(in)
	case "organizations describe-organization":
		if d.orgErr != "" {
			return "", errors.New(d.orgErr)
		}
		master := d.master
		if master == "" {
			master = d.instance()["OwnerAccountId"].(string)
		}
		return j(map[string]any{"Organization": map[string]string{"Id": "o-abc", "MasterAccountId": master}})
	}
	return d.w.handle(args)
}

// verifyRecorder is an ApplySpec.VerifyStartURL that records its calls.
type verifyRecorder struct {
	calls []string // "<region> <url>"
	err   error
}

func (v *verifyRecorder) verify(_ context.Context, region, url string) error {
	v.calls = append(v.calls, region+" "+url)
	return v.err
}

func runNoSSO(t *testing.T, d *discovery, mutate func(*ApplySpec)) (ApplyResult, *scriptedAWS, *verifyRecorder, string, error) {
	t.Helper()
	cli := &scriptedAWS{handle: d.handle}
	spec := noSSOSpec(t)
	rec := &verifyRecorder{}
	spec.VerifyStartURL = rec.verify
	if mutate != nil {
		mutate(&spec)
	}
	var log strings.Builder
	res, err := Apply(context.Background(), cli, spec, &log)
	return res, cli, rec, log.String(), err
}

func TestApplyNoSSODiscoversThroughAllRegions(t *testing.T) {
	d := newDiscovery(defaultWorld())
	res, cli, rec, log, err := runNoSSO(t, d, nil)
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log)
	}
	if len(cli.calls) == 0 || op(cli.calls[0]) != "sts get-caller-identity" {
		t.Fatalf("first call = %v, want sts get-caller-identity (the partition comes from the caller ARN)", cli.calls)
	}
	if len(d.scoped) != 1 || d.scoped[0] != "us-east-1" {
		t.Errorf("list-instances --region-scope ALL_REGIONS asked in %v, want once in the partition home us-east-1", d.scoped)
	}
	if len(d.scanned) != 0 || len(cli.callsOf("account list-regions")) != 0 {
		t.Errorf("the scan ran although ALL_REGIONS answered: scanned %v, list-regions %d", d.scanned, len(cli.callsOf("account list-regions")))
	}
	for _, c := range cli.calls {
		o := op(c)
		switch {
		case o == "sso-admin list-instances" && flagValue(c, "--region-scope") != "":
		case strings.HasPrefix(o, "sso-admin") || strings.HasPrefix(o, "identitystore"):
			if flagValue(c, "--region") != "eu-west-1" {
				t.Errorf("Identity Center call without the discovered region: %v", c)
			}
		case o == "sts get-caller-identity" || o == "organizations describe-organization":
			if flagValue(c, "--region") != "" {
				t.Errorf("%s carries --region: %v", o, c)
			}
		}
	}
	if n := len(cli.callsOf("sso-admin describe-instance")); n != 1 {
		t.Errorf("describe-instance called %d times, want once (the instance type check)", n)
	}
	if n := len(cli.callsOf("organizations describe-organization")); n != 1 {
		t.Errorf("describe-organization called %d times, want once", n)
	}
	if res.Delegation.StartURL != "https://d-123.awsapps.com/start" || res.Delegation.Region != "eu-west-1" || res.Delegation.AccountID != "123456789012" || res.Delegation.RoleName != DefaultPermissionSetName {
		t.Errorf("Delegation = %+v, want the derived start URL and the discovered region", res.Delegation)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "eu-west-1 https://d-123.awsapps.com/start" {
		t.Errorf("VerifyStartURL calls = %v, want one with the discovered region and the derived URL", rec.calls)
	}
	if !strings.Contains(log, "eu-west-1") || !strings.Contains(log, "https://d-123.awsapps.com/start") {
		t.Errorf("log does not name the region and the verified start URL:\n%s", log)
	}
}

func TestApplyNoSSOFallsBackToTheScanWhenTheCLIIsOld(t *testing.T) {
	d := newDiscovery(defaultWorld())
	d.allRegionsErr = "\nUnknown options: --region-scope, ALL_REGIONS\n"
	d.regions = []string{"us-east-1", "eu-west-1", "af-south-1"}
	d.byRegion = map[string][]map[string]any{"eu-west-1": {{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": "123456789012"}}}
	d.regionErr = map[string]string{"af-south-1": "Could not connect to the endpoint URL: \"https://sso.af-south-1.amazonaws.com/\""}
	res, cli, _, log, err := runNoSSO(t, d, nil)
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log)
	}
	lr := cli.callsOf("account list-regions")
	if len(lr) != 1 || flagValue(lr[0], "--region") != "us-east-1" {
		t.Fatalf("account list-regions calls = %v, want one in the partition home", lr)
	}
	if strings.Join(d.scanned, ",") != "us-east-1,eu-west-1,af-south-1" {
		t.Errorf("scanned %v, want every listed region in order, never stopping early", d.scanned)
	}
	if res.Delegation.Region != "eu-west-1" || res.InstanceARN != "arn:aws:sso:::instance/ssoins-1" {
		t.Errorf("result = %+v, want the instance found in eu-west-1", res)
	}
}

func TestApplyNoSSOScanRefusesTwoInstancesAcrossRegions(t *testing.T) {
	d := newDiscovery(defaultWorld())
	d.allRegionsErr = "Unknown options: --region-scope"
	d.regions = []string{"us-east-1", "eu-west-1"}
	d.byRegion = map[string][]map[string]any{
		"us-east-1": {{"InstanceArn": "arn:aws:sso:::instance/ssoins-acct", "IdentityStoreId": "d-acct", "OwnerAccountId": "123456789012"}},
		"eu-west-1": {{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": "123456789012"}},
	}
	_, cli, _, _, err := runNoSSO(t, d, nil)
	if err == nil || !strings.Contains(err.Error(), "ssoins-acct") || !strings.Contains(err.Error(), "ssoins-1") {
		t.Fatalf("err = %v, want the two instances named (the scan must not take the first region's answer)", err)
	}
	if n := len(cli.callsOf("sso-admin list-permission-sets")); n != 0 {
		t.Errorf("list-permission-sets called %d times after a refused discovery", n)
	}
}

func TestApplyNoSSOScanFailuresAreErrors(t *testing.T) {
	t.Run("regions cannot be listed", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.allRegionsErr = "Unknown options: --region-scope"
		d.regionsErr = "An error occurred (AccessDeniedException) when calling the ListRegions operation: no"
		_, _, _, _, err := runNoSSO(t, d, nil)
		if err == nil || !strings.Contains(err.Error(), "--sso-region") || !strings.Contains(err.Error(), "ListRegions") {
			t.Fatalf("err = %v, want one pointing at --sso-region with the CLI's line", err)
		}
	})
	t.Run("no instance anywhere", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.allRegionsErr = "Unknown options: --region-scope"
		d.regions = []string{"us-east-1", "eu-west-1"}
		d.byRegion = map[string][]map[string]any{}
		d.regionErr = map[string]string{"eu-west-1": "An error occurred (AccessDeniedException) when calling the ListInstances operation: denied"}
		_, _, _, _, err := runNoSSO(t, d, nil)
		if err == nil || !strings.Contains(err.Error(), "2 regions") || !strings.Contains(err.Error(), "organization instance") || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("err = %v, want the region count, the console path and the kept error line", err)
		}
	})
	t.Run("a service validation error is not an old CLI", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.allRegionsErr = "An error occurred (ValidationException) when calling the ListInstances operation: nope"
		d.regions = []string{"eu-west-1"}
		_, cli, _, _, err := runNoSSO(t, d, nil)
		if err == nil || !strings.Contains(err.Error(), "ValidationException") {
			t.Fatalf("err = %v, want the service error surfaced", err)
		}
		if len(cli.callsOf("account list-regions")) != 0 {
			t.Error("the scan ran after a service error that was not an unknown flag")
		}
	})
}

func TestApplyNoSSOPrimaryRegionFallsBackToTheRegionsList(t *testing.T) {
	t.Run("Regions[] carries the primary", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.allRegions = []map[string]any{{
			"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": "123456789012",
			"Regions": []map[string]any{{"RegionName": "us-east-2", "IsPrimaryRegion": false, "Status": "ACTIVE"}, {"RegionName": "eu-west-1", "IsPrimaryRegion": true, "Status": "ACTIVE"}},
		}}
		res, cli, _, log, err := runNoSSO(t, d, nil)
		if err != nil {
			t.Fatalf("Apply: %v\nlog:\n%s", err, log)
		}
		if res.Delegation.Region != "eu-west-1" || len(cli.callsOf("account list-regions")) != 0 {
			t.Errorf("Region = %q with %d list-regions calls, want eu-west-1 from Regions[] and no scan", res.Delegation.Region, len(cli.callsOf("account list-regions")))
		}
	})
	t.Run("an old payload without a region falls through to the scan", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.allRegions = []map[string]any{{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": "123456789012"}}
		d.regions = []string{"us-east-1", "eu-west-1"}
		d.byRegion = map[string][]map[string]any{"eu-west-1": {{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": "123456789012"}}}
		res, _, _, log, err := runNoSSO(t, d, nil)
		if err != nil {
			t.Fatalf("Apply: %v\nlog:\n%s", err, log)
		}
		if res.Delegation.Region != "eu-west-1" {
			t.Errorf("Region = %q, want eu-west-1 located by the scan", res.Delegation.Region)
		}
	})
}

func TestApplyNoSSORefusesAnAccountInstance(t *testing.T) {
	noPS := func(t *testing.T, d *discovery, wants ...string) {
		t.Helper()
		_, cli, _, _, err := runNoSSO(t, d, nil)
		if err == nil {
			t.Fatal("Apply succeeded against an account instance")
		}
		for _, w := range wants {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("err = %v, want it to mention %q", err, w)
			}
		}
		if n := len(cli.callsOf("sso-admin list-permission-sets")) + len(cli.callsOf("sso-admin create-permission-set")); n != 0 {
			t.Errorf("%d permission-set calls after the refusal, want none", n)
		}
	}
	t.Run("permission sets disabled", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		f := false
		d.psEnabled = &f
		noPS(t, d, "permission sets disabled", "organization instance", "email invitation")
	})
	t.Run("not in an organization", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.orgErr = "An error occurred (AWSOrganizationsNotInUseException) when calling the DescribeOrganization operation: Your account is not a member of an organization."
		noPS(t, d, "not in an organization", "aws organizations create-organization --feature-set ALL --profile acme-admin", "organization instance")
	})
	t.Run("owned by a member account", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.master = "999999999999"
		noPS(t, d, "999999999999", "123456789012", "management account")
	})
	t.Run("organizations access denied is a warning", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.orgErr = "An error occurred (AccessDeniedException) when calling the DescribeOrganization operation: denied"
		res, _, _, log, err := runNoSSO(t, d, nil)
		if err != nil {
			t.Fatalf("Apply: %v\nlog:\n%s", err, log)
		}
		if !strings.Contains(log, "warning:") || res.Delegation.StartURL == "" {
			t.Errorf("want a warning line and a finished apply; log:\n%s\nres: %+v", log, res)
		}
	})
	t.Run("payload without the flag passes", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.psEnabled = nil
		if _, _, _, log, err := runNoSSO(t, d, nil); err != nil {
			t.Fatalf("Apply: %v (a missing PermissionSetsEnabled is not false)\nlog:\n%s", err, log)
		}
	})
	t.Run("describe-instance failure is the CLI error", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		d.describeErr = "An error occurred (AccessDeniedException) when calling the DescribeInstance operation: denied"
		_, _, _, _, err := runNoSSO(t, d, nil)
		if err == nil || !strings.Contains(err.Error(), "DescribeInstance") {
			t.Fatalf("err = %v, want the describe-instance error", err)
		}
	})
}

func TestApplyNoSSOHonoursTheOverrides(t *testing.T) {
	d := newDiscovery(defaultWorld())
	d.byRegion = map[string][]map[string]any{"af-south-1": {{"InstanceArn": "arn:aws:sso:::instance/ssoins-1", "IdentityStoreId": "d-123", "OwnerAccountId": "123456789012"}}}
	res, cli, rec, log, err := runNoSSO(t, d, func(s *ApplySpec) {
		s.SSORegion = "af-south-1"
		s.StartURL = "https://acme.awsapps.com/start"
	})
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log)
	}
	if len(d.scoped) != 0 || len(cli.callsOf("account list-regions")) != 0 {
		t.Errorf("discovery ran although --sso-region was given: scoped %v", d.scoped)
	}
	for _, c := range cli.calls {
		if o := op(c); (strings.HasPrefix(o, "sso-admin") || strings.HasPrefix(o, "identitystore")) && flagValue(c, "--region") != "af-south-1" {
			t.Errorf("Identity Center call without the given region: %v", c)
		}
	}
	if res.Delegation.StartURL != "https://acme.awsapps.com/start" || res.Delegation.Region != "af-south-1" {
		t.Errorf("Delegation = %+v, want the given start URL and region", res.Delegation)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "af-south-1 https://acme.awsapps.com/start" {
		t.Errorf("VerifyStartURL calls = %v, want the given URL verified in the given region", rec.calls)
	}
}

func TestApplyNoSSOStartURLVerification(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		_, _, _, _, err := runNoSSO(t, d, func(s *ApplySpec) {
			rec := &verifyRecorder{err: ErrStartURLRejected}
			s.VerifyStartURL = rec.verify
		})
		if err == nil || !strings.Contains(err.Error(), "OIDC rejected start URL https://d-123.awsapps.com/start") || !strings.Contains(err.Error(), "--start-url") {
			t.Fatalf("err = %v, want the rejection naming the URL and --start-url", err)
		}
		if err != nil && strings.Contains(err.Error(), "customiz") {
			t.Errorf("err = %v, must not claim the portal was customized", err)
		}
	})
	t.Run("other failure is a warning", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		res, _, _, log, err := runNoSSO(t, d, func(s *ApplySpec) {
			rec := &verifyRecorder{err: errors.New("oidc: transport")}
			s.VerifyStartURL = rec.verify
		})
		if err != nil {
			t.Fatalf("Apply: %v (a transient OIDC failure must not fail a finished apply)", err)
		}
		if !strings.Contains(log, "warning:") || res.Delegation.StartURL != "https://d-123.awsapps.com/start" {
			t.Errorf("want a warning and the URL kept; log:\n%s\nres: %+v", log, res.Delegation)
		}
	})
	t.Run("nil verifier skips", func(t *testing.T) {
		d := newDiscovery(defaultWorld())
		res, _, _, _, err := runNoSSO(t, d, func(s *ApplySpec) { s.VerifyStartURL = nil })
		if err != nil || res.Delegation.StartURL != "https://d-123.awsapps.com/start" {
			t.Fatalf("res = %+v, err = %v, want the derived URL unverified", res.Delegation, err)
		}
	})
}

func TestApplyNoSSORefusesOtherPartitions(t *testing.T) {
	w := defaultWorld()
	w.callerARN = "arn:aws-cn:iam::123456789012:user/admin"
	d := newDiscovery(w)
	_, cli, _, _, err := runNoSSO(t, d, nil)
	if err == nil || !strings.Contains(err.Error(), "aws partition only") || !strings.Contains(err.Error(), "arn:aws-cn:") {
		t.Fatalf("err = %v, want the partition limit naming the caller", err)
	}
	if len(cli.callsOf("sso-admin list-instances")) != 0 {
		t.Error("list-instances called for a partition the sign-in cannot use")
	}
}

func TestApplySSOProfileKeepsRevFiveOrder(t *testing.T) {
	d := newDiscovery(defaultWorld())
	cli := &scriptedAWS{handle: d.handle}
	spec := fastSpec(t) // acme-admin with sso_* keys
	spec.SSORegion = "af-south-1"
	spec.StartURL = "https://other.awsapps.com/start"
	rec := &verifyRecorder{}
	spec.VerifyStartURL = rec.verify
	var log strings.Builder
	res, err := Apply(context.Background(), cli, spec, &log)
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log.String())
	}
	if op(cli.calls[0]) != "sso-admin list-instances" || flagValue(cli.calls[0], "--region-scope") != "" || flagValue(cli.calls[0], "--region") != "eu-west-1" {
		t.Errorf("first call = %v, want list-instances --region eu-west-1 as rev 5", cli.calls[0])
	}
	for _, name := range []string{"sso-admin describe-instance", "organizations describe-organization", "account list-regions"} {
		if n := len(cli.callsOf(name)); n != 0 {
			t.Errorf("%s called %d times for an SSO profile", name, n)
		}
	}
	if res.Delegation.StartURL != "https://d-123.awsapps.com/start" || res.Delegation.Region != "eu-west-1" {
		t.Errorf("Delegation = %+v, want the profile's facts, not the overrides", res.Delegation)
	}
	if len(rec.calls) != 0 {
		t.Errorf("VerifyStartURL called %v for an SSO profile", rec.calls)
	}
	if !strings.Contains(log.String(), "warning:") {
		t.Errorf("overrides on an SSO profile should warn; log:\n%s", log.String())
	}
}

func TestApplyUnreadableConfigIsNotDiscovery(t *testing.T) {
	d := newDiscovery(defaultWorld())
	cli := &scriptedAWS{handle: d.handle}
	spec := fastSpec(t)
	spec.AWSConfigPath = filepath.Join(t.TempDir(), "missing", "config")
	rec := &verifyRecorder{}
	spec.VerifyStartURL = rec.verify
	var log strings.Builder
	res, err := Apply(context.Background(), cli, spec, &log)
	if err != nil {
		t.Fatalf("Apply: %v\nlog:\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), "warning:") {
		t.Errorf("an unreadable config must warn; log:\n%s", log.String())
	}
	if len(d.scoped) != 0 || len(cli.callsOf("account list-regions")) != 0 || len(rec.calls) != 0 {
		t.Errorf("discovery or verification ran for an unreadable config: scoped %v, verify %v", d.scoped, rec.calls)
	}
	for _, c := range cli.calls {
		if flagValue(c, "--region") != "" {
			t.Errorf("--region passed with no region to take it from: %v", c)
		}
	}
	if res.Delegation.StartURL != "" || res.Delegation.Region != "" {
		t.Errorf("Delegation = %+v, want no derived facts for an unreadable config", res.Delegation)
	}
}

func TestPlanNoSSOListsDiscovery(t *testing.T) {
	spec := noSSOSpec(t)
	steps, err := Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 || op(steps[0].Args) != "sts get-caller-identity" {
		t.Fatalf("first planned step = %+v, want sts get-caller-identity", steps)
	}
	joined := make([]string, 0, len(steps))
	for _, s := range steps {
		joined = append(joined, s.Name+": "+strings.Join(s.Args, " "))
	}
	all := strings.Join(joined, "\n")
	for _, want := range []string{
		"sso-admin list-instances --region-scope ALL_REGIONS",
		"account list-regions",
		"sso-admin describe-instance",
		"organizations describe-organization",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q:\n%s", want, all)
		}
	}
	var scoped, listRegions bool
	for _, s := range steps {
		if op(s.Args) == "sso-admin list-instances" && flagValue(s.Args, "--region-scope") != "" {
			scoped = true
			if flagValue(s.Args, "--region") != "us-east-1" {
				t.Errorf("ALL_REGIONS step not in the partition home: %v", s.Args)
			}
		}
		if op(s.Args) == "account list-regions" {
			listRegions = true
			if flagValue(s.Args, "--region") != "us-east-1" {
				t.Errorf("list-regions step not in the partition home: %v", s.Args)
			}
		}
	}
	if !scoped || !listRegions {
		t.Errorf("plan lacks the ALL_REGIONS or the list-regions step:\n%s", all)
	}
	last := steps[len(steps)-1]
	if !strings.Contains(strings.ToLower(last.Name), "verification") || len(last.Args) != 0 {
		t.Errorf("last step = %+v, want the in-process start URL verification with no aws call", last)
	}
}
