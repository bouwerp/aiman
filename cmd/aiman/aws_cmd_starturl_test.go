package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

func TestDelegationWriteKeepsTheExistingStartURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	spec := awsdelegation.ApplySpec{Profile: "acme-admin", Regions: []string{"eu-west-1"}}
	hint := awsdelegation.DelegationHint{Region: "eu-west-1", AccountID: "123456789012", RoleName: "aiman-delegated"}
	res := awsdelegation.ApplyResult{Delegation: hint}

	cfg := writeLaptopConfig(t)
	target, err := newDelegationTarget(cfg, "devbox", "beta", spec.Profile)
	if err != nil {
		t.Fatal(err)
	}
	path, err := target.write(spec, hint)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := target.printWritten(res, path, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sso_start_url is still missing") || !strings.Contains(out.String(), "--start-url") {
		t.Errorf("no start URL anywhere, want the missing line naming --start-url:\n%s", out.String())
	}

	cfg = reloadLaptopConfig(t)
	delegationNamed(cfg.Remotes[0], "beta").SSOStartURL = "https://d-123.awsapps.com/start"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg = reloadLaptopConfig(t)
	target, err = newDelegationTarget(cfg, "devbox", "beta", spec.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if path, err = target.write(spec, hint); err != nil {
		t.Fatal(err)
	}
	if got := delegationNamed(reloadLaptopConfig(t).Remotes[0], "beta").SSOStartURL; got != "https://d-123.awsapps.com/start" {
		t.Errorf("SSOStartURL = %q, want the existing URL kept when the hint has none", got)
	}
	out.Reset()
	if err := target.printWritten(res, path, false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "still missing") {
		t.Errorf("the entry has a start URL, want no missing line:\n%s", out.String())
	}
}

func TestDoneBlockNamesAMissingStartURL(t *testing.T) {
	var out bytes.Buffer
	res := awsdelegation.ApplyResult{Delegation: awsdelegation.DelegationHint{Region: "eu-west-1"}}
	if err := printApplyResult(res, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sso_start_url: <pass --start-url; see warning above>") {
		t.Errorf("Done block:\n%s", out.String())
	}
}
