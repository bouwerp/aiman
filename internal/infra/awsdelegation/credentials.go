package awsdelegation

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// SessionCredentials matches the fields returned by `aws sts get-session-token` or `assume-role`.
type SessionCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
}

type getSessionTokenOutput struct {
	Credentials SessionCredentials `json:"Credentials"`
}

// CredentialOptions carries optional restrictions for temporary credential minting.
// When SessionPolicy or DurationSeconds are set, assume-role is used instead of
// get-session-token, because get-session-token does not support inline policies.
type CredentialOptions struct {
	// SessionPolicy is an inline JSON IAM policy that further restricts the
	// temporary credentials. Passed as --policy to sts assume-role.
	SessionPolicy string
	// DurationSeconds is the credential lifetime (900–43200). 0 means AWS default.
	DurationSeconds int
	// RoleARN is required when SessionPolicy or DurationSeconds are set, so that
	// assume-role is called with an explicit role to scope down.
	RoleARN string
	// SessionName is the role session name for assume-role (defaults to "aiman" when empty).
	SessionName string
}

// GetTemporaryCredentials obtains temporary AWS credentials locally.
// When opts specifies a SessionPolicy, DurationSeconds, or RoleARN, it calls
// `aws sts assume-role`; otherwise it calls `aws sts get-session-token`.
func GetTemporaryCredentials(ctx context.Context, profile string, opts ...CredentialOptions) (*SessionCredentials, error) {
	var o CredentialOptions
	if len(opts) > 0 {
		o = opts[0]
	}

	useAssumeRole := strings.TrimSpace(o.RoleARN) != "" ||
		strings.TrimSpace(o.SessionPolicy) != "" ||
		o.DurationSeconds > 0

	if useAssumeRole {
		roleARN := strings.TrimSpace(o.RoleARN)
		if roleARN == "" {
			return nil, fmt.Errorf("assume-role requires a role ARN when session_policy or duration_seconds is set")
		}
		sessionName := strings.TrimSpace(o.SessionName)
		if sessionName == "" {
			sessionName = "aiman"
		}
		return getAssumeRoleCreds(ctx, roleARN, sessionName, profile, o.SessionPolicy, o.DurationSeconds)
	}

	p := strings.TrimSpace(profile)
	args := []string{"sts", "get-session-token", "--output", "json"}
	if p != "" {
		args = append(args, "--profile", p)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// If get-session-token fails, it might be because the profile is already an assumed role
		// or using SSO. We don't try to handle every case, but we provide the error.
		return nil, fmt.Errorf("aws sts get-session-token: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var tok getSessionTokenOutput
	if err := json.Unmarshal(out, &tok); err != nil {
		return nil, fmt.Errorf("parse aws JSON: %w", err)
	}
	return &tok.Credentials, nil
}

// GetAssumeRoleCredentials runs `aws sts assume-role` locally to obtain temporary tokens for a specific role.
func GetAssumeRoleCredentials(ctx context.Context, roleARN, sessionName, profile string) (*SessionCredentials, error) {
	return getAssumeRoleCreds(ctx, roleARN, sessionName, profile, "", 0)
}

// getAssumeRoleCreds is the shared implementation for assume-role calls.
func getAssumeRoleCreds(ctx context.Context, roleARN, sessionName, profile, sessionPolicy string, durationSeconds int) (*SessionCredentials, error) {
	args := []string{"sts", "assume-role",
		"--role-arn", roleARN,
		"--role-session-name", sessionName,
		"--output", "json",
	}
	if p := strings.TrimSpace(profile); p != "" {
		args = append(args, "--profile", p)
	}
	if sp := strings.TrimSpace(sessionPolicy); sp != "" {
		args = append(args, "--policy", sp)
	}
	if durationSeconds > 0 {
		args = append(args, "--duration-seconds", fmt.Sprintf("%d", durationSeconds))
	}

	cmd := exec.CommandContext(ctx, "aws", args...) // #nosec G204
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("aws sts assume-role: %w — %s", err, strings.TrimSpace(string(out)))
	}

	var o getSessionTokenOutput
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("parse aws JSON: %w", err)
	}
	return &o.Credentials, nil
}

// FormatCredentialsSection returns the [profile] or [default] section for ~/.aws/credentials.
func FormatCredentialsSection(profileName string, creds *SessionCredentials) string {
	if creds == nil {
		return ""
	}
	name := strings.TrimSpace(profileName)
	if name == "" {
		name = "default"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\n", name)
	fmt.Fprintf(&b, "aws_access_key_id = %s\n", creds.AccessKeyID)
	fmt.Fprintf(&b, "aws_secret_access_key = %s\n", creds.SecretAccessKey)
	fmt.Fprintf(&b, "aws_session_token = %s\n", creds.SessionToken)
	return b.String()
}
