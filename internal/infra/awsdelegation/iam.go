package awsdelegation

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// passthroughPolicy is an IAM policy that allows all actions on all resources.
// When attached to an assumed role, effective permissions are the intersection
// of this policy and the caller's own permissions — i.e. the caller retains
// exactly the permissions they already have, nothing more.
//
// TODO: replace with fine-grained permission configuration once the flow is proven.
const passthroughPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`

// passthroughPolicyName is the inline policy name stored on the managed role.
const passthroughPolicyName = "aiman-passthrough"

// EnsureRole creates the IAM role identified by accountID+roleName if it does
// not already exist, then ensures a passthrough inline policy is in place.
// The trust policy allows the IAM principal of sourceProfile to assume the role.
//
// It is safe to call on every credential push — all operations are idempotent.
// Returns the role ARN on success.
func EnsureRole(ctx context.Context, sourceProfile, accountID, roleName string) (string, error) {
	roleARN, err := RoleARNFromParts(accountID, roleName)
	if err != nil {
		return "", fmt.Errorf("build role ARN: %w", err)
	}

	callerARN, err := callerIdentityARN(ctx, sourceProfile)
	if err != nil {
		return "", fmt.Errorf("get caller identity: %w", err)
	}

	trustPolicy, err := buildTrustPolicy(callerARN)
	if err != nil {
		return "", fmt.Errorf("build trust policy: %w", err)
	}

	if err := createRoleIfMissing(ctx, sourceProfile, roleName, trustPolicy); err != nil {
		return "", fmt.Errorf("ensure role exists: %w", err)
	}

	// Always update the trust policy — the role may have been created by a
	// previous run with a different caller ARN, or the user's ARN may have changed.
	if err := updateTrustPolicy(ctx, sourceProfile, roleName, trustPolicy); err != nil {
		return "", fmt.Errorf("update trust policy: %w", err)
	}

	if err := putInlinePolicy(ctx, sourceProfile, roleName); err != nil {
		return "", fmt.Errorf("put inline policy: %w", err)
	}

	return roleARN, nil
}

// DeleteManagedRole removes the inline passthrough policy then deletes the role.
// Does not fail if the role does not exist.
func DeleteManagedRole(ctx context.Context, sourceProfile, roleName string) error {
	// Remove inline policy first (role deletion fails when policies are present).
	_ = runIAM(ctx, sourceProfile, "delete-role-policy",
		"--role-name", roleName,
		"--policy-name", passthroughPolicyName,
	)
	if err := runIAM(ctx, sourceProfile, "delete-role", "--role-name", roleName); err != nil {
		if strings.Contains(err.Error(), "NoSuchEntity") {
			return nil
		}
		return fmt.Errorf("delete role %s: %w", roleName, err)
	}
	return nil
}

// --- helpers ---

type callerIdentityFull struct {
	UserID  string `json:"UserId"`
	Account string `json:"Account"`
	Arn     string `json:"Arn"`
}

func callerIdentityARN(ctx context.Context, profile string) (string, error) {
	args := []string{"sts", "get-caller-identity", "--output", "json"}
	if p := strings.TrimSpace(profile); p != "" {
		args = append(args, "--profile", p)
	}
	// #nosec G204
	out, err := exec.CommandContext(ctx, "aws", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("aws sts get-caller-identity: %w — %s", err, strings.TrimSpace(string(out)))
	}
	var o callerIdentityFull
	if err := json.Unmarshal(out, &o); err != nil {
		return "", fmt.Errorf("parse caller identity JSON: %w", err)
	}
	if o.Arn == "" {
		return "", fmt.Errorf("empty ARN in caller identity response")
	}
	return o.Arn, nil
}

func buildTrustPolicy(principalARN string) (string, error) {
	type statement struct {
		Effect    string            `json:"Effect"`
		Principal map[string]string `json:"Principal"`
		Action    string            `json:"Action"`
	}
	type policy struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}
	p := policy{
		Version: "2012-10-17",
		Statement: []statement{{
			Effect:    "Allow",
			Principal: map[string]string{"AWS": principalARN},
			Action:    "sts:AssumeRole",
		}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func createRoleIfMissing(ctx context.Context, profile, roleName, trustPolicy string) error {
	err := runIAM(ctx, profile, "create-role",
		"--role-name", roleName,
		"--assume-role-policy-document", trustPolicy,
	)
	if err == nil {
		return nil
	}
	// EntityAlreadyExists → role is already there, nothing to do.
	if strings.Contains(err.Error(), "EntityAlreadyExists") {
		return nil
	}
	return err
}

func putInlinePolicy(ctx context.Context, profile, roleName string) error {
	return runIAM(ctx, profile, "put-role-policy",
		"--role-name", roleName,
		"--policy-name", passthroughPolicyName,
		"--policy-document", passthroughPolicy,
	)
}

func updateTrustPolicy(ctx context.Context, profile, roleName, trustPolicy string) error {
	return runIAM(ctx, profile, "update-assume-role-policy",
		"--role-name", roleName,
		"--policy-document", trustPolicy,
	)
}

// runIAM runs an `aws iam <subcommand>` with optional --profile and extra args.
func runIAM(ctx context.Context, profile, subcommand string, args ...string) error {
	cmdArgs := []string{"iam", subcommand}
	if p := strings.TrimSpace(profile); p != "" {
		cmdArgs = append(cmdArgs, "--profile", p)
	}
	cmdArgs = append(cmdArgs, args...)
	// #nosec G204
	out, err := exec.CommandContext(ctx, "aws", cmdArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("aws iam %s: %w — %s", subcommand, err, strings.TrimSpace(string(out)))
	}
	return nil
}
