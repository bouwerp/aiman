package awsdelegation

import (
	"encoding/json"
	"testing"
)

func TestBuildTrustPolicy(t *testing.T) {
	principalARN := "arn:aws:iam::123456789012:user/dev"
	got, err := buildTrustPolicy(principalARN)
	if err != nil {
		t.Fatalf("buildTrustPolicy error: %v", err)
	}

	var p struct {
		Version   string `json:"Version"`
		Statement []struct {
			Effect    string            `json:"Effect"`
			Principal map[string]string `json:"Principal"`
			Action    string            `json:"Action"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if p.Version != "2012-10-17" {
		t.Errorf("Version = %q, want 2012-10-17", p.Version)
	}
	if len(p.Statement) != 1 {
		t.Fatalf("len(Statement) = %d, want 1", len(p.Statement))
	}
	s := p.Statement[0]
	if s.Effect != "Allow" {
		t.Errorf("Effect = %q, want Allow", s.Effect)
	}
	if s.Action != "sts:AssumeRole" {
		t.Errorf("Action = %q, want sts:AssumeRole", s.Action)
	}
	if s.Principal["AWS"] != principalARN {
		t.Errorf("Principal.AWS = %q, want %q", s.Principal["AWS"], principalARN)
	}
}

func TestPassthroughPolicyIsValidJSON(t *testing.T) {
	var v interface{}
	if err := json.Unmarshal([]byte(passthroughPolicy), &v); err != nil {
		t.Errorf("passthroughPolicy is not valid JSON: %v", err)
	}
}

func TestEnsureRoleARNConstruction(t *testing.T) {
	// EnsureRole uses RoleARNFromParts — verify the ARN shape without AWS access.
	arn, err := RoleARNFromParts("123456789012", "MyRole")
	if err != nil {
		t.Fatalf("RoleARNFromParts error: %v", err)
	}
	want := "arn:aws:iam::123456789012:role/MyRole"
	if arn != want {
		t.Errorf("ARN = %q, want %q", arn, want)
	}
}
