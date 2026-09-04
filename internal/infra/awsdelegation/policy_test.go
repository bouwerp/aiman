package awsdelegation

import (
	"encoding/json"
	"path/filepath"
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

func TestBuildRegionPolicy_LeavesPackedPolicyHeadroom(t *testing.T) {
	// AWS does not publish the packed binary limit, so keep the plaintext well
	// below its separate 2,048-byte request limit.
	const maxPolicyBytes = 1000

	got := BuildRegionPolicy([]string{"us-east-2"})
	if len(got) > maxPolicyBytes {
		t.Fatalf("generated region policy is %d bytes, want at most %d", len(got), maxPolicyBytes)
	}
}

func TestBuildRegionPolicy_AllowsRoute53WithoutRegion(t *testing.T) {
	got := BuildRegionPolicy([]string{"us-east-2"})
	var p struct {
		Statement []struct {
			Action    any            `json:"Action"`
			Condition map[string]any `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(p.Statement) < 2 {
		t.Fatalf("want a Route53 statement besides the region lock, got %d statements", len(p.Statement))
	}
	foundList, foundChange := false, false
	for _, s := range p.Statement {
		if s.Condition != nil {
			continue
		}
		for _, a := range actionList(s.Action) {
			if a == "route53:ListHostedZones" {
				foundList = true
			}
			if a == "route53:ChangeResourceRecordSets" {
				foundChange = true
			}
		}
	}
	if !foundList || !foundChange {
		t.Fatalf("unconditional Route53 DNS access missing, policy=%s", got)
	}
}

func TestBuildRegionPolicy_AllowsIAMInspectWithoutRegion(t *testing.T) {
	got := BuildRegionPolicy([]string{"us-east-2"})
	var p struct {
		Statement []struct {
			Action    any            `json:"Action"`
			Condition map[string]any `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := []string{
		"iam:GetRole",
		"iam:ListRolePolicies",
		"iam:GetRolePolicy",
		"iam:ListAttachedRolePolicies",
		"iam:GetPolicy",
		"iam:GetPolicyVersion",
		"iam:SimulatePrincipalPolicy",
		"iam:CreatePolicyVersion",
		"iam:SetDefaultPolicyVersion",
		"iam:ListPolicyVersions",
		"iam:DeletePolicyVersion",
		"iam:PutRolePolicy",
		"iam:CreateRole",
		"iam:DeleteRole",
		"iam:UpdateRole",
		"iam:UpdateAssumeRolePolicy",
		"iam:DeleteRolePolicy",
		"iam:AttachRolePolicy",
		"iam:DetachRolePolicy",
		"iam:TagRole",
		"iam:UntagRole",
		"iam:ListRoles",
		"iam:PassRole",
		"iam:GetUser",
		"iam:ListUsers",
		"iam:ListUserPolicies",
		"iam:GetUserPolicy",
		"iam:PutUserPolicy",
		"iam:DeleteUserPolicy",
		"iam:ListAttachedUserPolicies",
		"iam:AttachUserPolicy",
		"iam:DetachUserPolicy",
		"iam:TagUser",
		"iam:UntagUser",
	}
	var allowed []string
	for _, s := range p.Statement {
		if s.Condition != nil {
			continue
		}
		allowed = append(allowed, actionList(s.Action)...)
	}
	var missing []string
	for _, a := range want {
		if !actionAllowed(allowed, a) {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unconditional IAM policy access missing %v, policy=%s", missing, got)
	}
}

func actionAllowed(patterns []string, action string) bool {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, action)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func TestBuildRegionPolicy_AllowsS3WithoutRegion(t *testing.T) {
	got := BuildRegionPolicy([]string{"us-east-2"})
	var p struct {
		Statement []struct {
			Action    any            `json:"Action"`
			Condition map[string]any `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := []string{
		"s3:ListAllMyBuckets",
		"s3:CreateBucket",
		"s3:DeleteBucket",
		"s3:ListBucket",
		"s3:GetObject",
		"s3:PutObject",
		"s3:DeleteObject",
	}
	found := map[string]bool{}
	for _, s := range p.Statement {
		if s.Condition != nil {
			continue
		}
		for _, a := range actionList(s.Action) {
			found[a] = true
			if a == "s3:*" {
				return
			}
		}
	}
	var missing []string
	for _, a := range want {
		if !found[a] {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unconditional S3 access missing %v, policy=%s", missing, got)
	}
}

func TestBuildRegionPolicy_AllowsResourceDiscoveryWithoutRegion(t *testing.T) {
	got := BuildRegionPolicy([]string{"us-east-2"})
	var p struct {
		Statement []struct {
			Action    any            `json:"Action"`
			Condition map[string]any `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(got), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := []string{
		"lambda:ListFunctions",
		"lambda:GetFunctionConfiguration",
		"dynamodb:ListTables",
		"dynamodb:GetItem",
		"dynamodb:Query",
		"dynamodb:Scan",
	}
	found := map[string]bool{}
	for _, s := range p.Statement {
		if s.Condition != nil {
			continue
		}
		for _, a := range actionList(s.Action) {
			found[a] = true
		}
	}
	var missing []string
	for _, a := range want {
		if !found[a] {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("unconditional resource-discovery access missing %v, policy=%s", missing, got)
	}
}

func actionList(action any) []string {
	switch v := action.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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
