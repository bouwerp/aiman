//go:build acceptance

package awsdelegation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPermissionSetPolicy(t *testing.T) {
	for _, regions := range [][]string{
		{"us-east-2"},
		{"us-east-2", "eu-west-1"},
		{" us-east-2 ", "", "eu-west-1"},
	} {
		got := PermissionSetPolicy(regions)
		if got == "" {
			t.Fatalf("PermissionSetPolicy(%q) is empty", regions)
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("PermissionSetPolicy(%q) is not valid JSON:\n%s", regions, got)
		}
		var gotDoc, wantDoc any
		if err := json.Unmarshal([]byte(got), &gotDoc); err != nil {
			t.Fatalf("unmarshal printed policy: %v", err)
		}
		if err := json.Unmarshal([]byte(BuildRegionPolicy(regions)), &wantDoc); err != nil {
			t.Fatalf("unmarshal BuildRegionPolicy: %v", err)
		}
		if !reflect.DeepEqual(gotDoc, wantDoc) {
			t.Fatalf("PermissionSetPolicy(%q) differs from BuildRegionPolicy:\n got %v\nwant %v", regions, gotDoc, wantDoc)
		}
		if !strings.Contains(got, "\n  ") && !strings.Contains(got, "\n\t") {
			t.Fatalf("PermissionSetPolicy(%q) is not indented:\n%s", regions, got)
		}
	}
	for _, empty := range [][]string{nil, {}, {"", "  "}} {
		if got := PermissionSetPolicy(empty); got != "" {
			t.Fatalf("PermissionSetPolicy(%q) = %q, want empty like BuildRegionPolicy", empty, got)
		}
	}
}

func TestPermissionSetSteps(t *testing.T) {
	regions := []string{"us-east-2"}

	def := PermissionSetSteps("", "123456789012", regions)
	for _, want := range []string{"aiman-delegated", "PT12H", "123456789012"} {
		if !strings.Contains(def, want) {
			t.Errorf("default steps missing %q:\n%s", want, def)
		}
	}
	if !strings.Contains(strings.ToLower(def), "inline policy") {
		t.Errorf("steps do not mention the inline policy:\n%s", def)
	}

	named := PermissionSetSteps("team-delegated", "210987654321", regions)
	for _, want := range []string{"team-delegated", "PT12H", "210987654321"} {
		if !strings.Contains(named, want) {
			t.Errorf("named steps missing %q:\n%s", want, named)
		}
	}
	if strings.Contains(named, "aiman-delegated") {
		t.Errorf("an explicit name must replace the default:\n%s", named)
	}
}
