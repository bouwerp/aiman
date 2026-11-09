package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitRegions(t *testing.T) {
	for in, want := range map[string][]string{
		"":                       nil,
		"eu-west-1":              {"eu-west-1"},
		"eu-west-1, af-south-1":  {"eu-west-1", "af-south-1"},
		" eu-west-1 ,,":          {"eu-west-1"},
		"eu-west-1,af-south-1, ": {"eu-west-1", "af-south-1"},
	} {
		if got := splitRegions(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitRegions(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAWSPermissionSetApplySavesTrimmedRegions(t *testing.T) {
	installFakeAWS(t, "")
	cfg := writeLaptopConfig(t)
	args := []string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1, af-south-1,", "--remote", "devbox"}
	if err := runAWS(args, cfg, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAWS: %v", err)
	}
	d := delegationNamed(reloadLaptopConfig(t).Remotes[0], "acme-admin")
	if d == nil || strings.Join(d.Regions, ",") != "eu-west-1,af-south-1" {
		t.Fatalf("entry = %+v, want regions eu-west-1,af-south-1", d)
	}
}

func TestAWSPermissionSetApplySaveFailureSaysRerunIsSafe(t *testing.T) {
	installFakeAWS(t, "")
	cfg := writeLaptopConfig(t)
	dir := filepath.Join(os.Getenv("HOME"), ".aiman")
	if err := os.Remove(filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "config.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runAWS([]string{"permission-set-apply", "--profile", "acme-admin", "--regions", "eu-west-1", "--remote", "devbox"}, cfg, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Identity Center setup is done") || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("err = %v, want one saying the apply is done and a rerun with --remote is safe", err)
	}
}
