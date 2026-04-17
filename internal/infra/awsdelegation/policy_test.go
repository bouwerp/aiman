package awsdelegation

import (
	"encoding/json"
	"testing"
)

func TestBuildRegionPolicy_Empty(t *testing.T) {
	if got := BuildRegionPolicy(nil); got != "" {
		t.Errorf("expected empty string for nil regions, got %q", got)
	}
	if got := BuildRegionPolicy([]string{}); got != "" {
		t.Errorf("expected empty string for empty regions, got %q", got)
	}
	if got := BuildRegionPolicy([]string{"  ", ""}); got != "" {
		t.Errorf("expected empty string for whitespace-only regions, got %q", got)
	}
}

func TestBuildRegionPolicy_Single(t *testing.T) {
	got := BuildRegionPolicy([]string{"us-east-2"})
	if got == "" {
		t.Fatal("expected non-empty policy for single region")
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	stmts, _ := p["Statement"].([]any)
	if len(stmts) == 0 {
		t.Fatal("expected at least one statement")
	}
	stmt, _ := stmts[0].(map[string]any)
	cond, _ := stmt["Condition"].(map[string]any)
	eq, _ := cond["StringEquals"].(map[string]any)
	region, ok := eq["aws:RequestedRegion"]
	if !ok {
		t.Fatal("missing aws:RequestedRegion condition")
	}
	if r, ok := region.(string); !ok || r != "us-east-2" {
		t.Errorf("expected string us-east-2, got %v", region)
	}
}

func TestBuildRegionPolicy_Multi(t *testing.T) {
	got := BuildRegionPolicy([]string{"us-east-2", "eu-west-1"})
	if got == "" {
		t.Fatal("expected non-empty policy for multiple regions")
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	stmts, _ := p["Statement"].([]any)
	stmt, _ := stmts[0].(map[string]any)
	cond, _ := stmt["Condition"].(map[string]any)
	eq, _ := cond["StringEquals"].(map[string]any)
	regions, ok := eq["aws:RequestedRegion"].([]any)
	if !ok {
		t.Fatal("expected array for multiple regions")
	}
	if len(regions) != 2 {
		t.Errorf("expected 2 regions, got %d", len(regions))
	}
}

func TestBuildRegionPolicy_TrimsWhitespace(t *testing.T) {
	got := BuildRegionPolicy([]string{"  us-east-2 ", " eu-west-1"})
	if got == "" {
		t.Fatal("expected non-empty policy")
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}
