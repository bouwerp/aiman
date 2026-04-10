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

// GetTemporaryCredentials runs `aws sts get-session-token` locally to obtain temporary tokens.
func GetTemporaryCredentials(ctx context.Context, profile string) (*SessionCredentials, error) {
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

	var o getSessionTokenOutput
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("parse aws JSON: %w", err)
	}
	return &o.Credentials, nil
}

// GetAssumeRoleCredentials runs `aws sts assume-role` locally to obtain temporary tokens for a specific role.
func GetAssumeRoleCredentials(ctx context.Context, roleARN, sessionName, profile string) (*SessionCredentials, error) {
	args := []string{"sts", "assume-role", "--role-arn", roleARN, "--role-session-name", sessionName, "--output", "json"}
	if p := strings.TrimSpace(profile); p != "" {
		args = append(args, "--profile", p)
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
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
