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

// DefaultDurationSeconds is the credential lifetime used when no duration is configured (12 hours).
const DefaultDurationSeconds = 43200

// CredentialOptions carries optional restrictions for temporary credential minting.
// When RoleARN or SessionPolicy are set, assume-role is used.
// DurationSeconds alone (without RoleARN/SessionPolicy) passes --duration-seconds to get-session-token.
type CredentialOptions struct {
	// SessionPolicy is an inline JSON IAM policy that further restricts the
	// temporary credentials. Requires RoleARN — get-session-token does not
	// support inline session policies. Passed as --policy to sts assume-role.
	SessionPolicy string
	// DurationSeconds is the credential lifetime (900–43200). 0 uses DefaultDurationSeconds (43200).
	DurationSeconds int
	// RoleARN, when set, causes assume-role to be used instead of get-session-token.
	// Required when SessionPolicy is set.
	RoleARN string
	// SessionName is the role session name for assume-role (defaults to "aiman" when empty).
	SessionName string
}

// GetTemporaryCredentials obtains temporary AWS credentials locally.
//
// Decision table:
//   - RoleARN set OR SessionPolicy set → `aws sts assume-role` (role must exist and trust caller).
//     SessionPolicy alone without a RoleARN is rejected with an actionable error.
//   - Otherwise → `aws sts get-session-token`.
//
// DurationSeconds defaults to DefaultDurationSeconds (43200 = 12 h) when not set.
func GetTemporaryCredentials(ctx context.Context, profile string, opts ...CredentialOptions) (*SessionCredentials, error) {
	var o CredentialOptions
	if len(opts) > 0 {
		o = opts[0]
	}

	// assume-role is needed only when we are switching identity (RoleARN) or
	// further restricting permissions via an inline session policy.
	// DurationSeconds alone can be passed directly to get-session-token.
	useAssumeRole := strings.TrimSpace(o.RoleARN) != "" || strings.TrimSpace(o.SessionPolicy) != ""

	if useAssumeRole {
		roleARN := strings.TrimSpace(o.RoleARN)
		if roleARN == "" {
			return nil, fmt.Errorf(
				"session_policy requires a role_arn / account_id so that assume-role can be used " +
					"(get-session-token does not support inline policies). " +
					"Set account_id in the AWS delegation config or remove the regions / session_policy restriction.")
		}
		sessionName := strings.TrimSpace(o.SessionName)
		if sessionName == "" {
			sessionName = "aiman"
		}
		// Apply DefaultDurationSeconds when no explicit duration is configured.
		// If the role's MaxSessionDuration is shorter, AWS returns a ValidationError —
		// in that case we retry without the flag so the role's own maximum is used.
		dur := o.DurationSeconds
		if dur <= 0 {
			dur = DefaultDurationSeconds
		}
		creds, err := getAssumeRoleCreds(ctx, roleARN, sessionName, profile, o.SessionPolicy, dur)
		if err != nil && strings.Contains(err.Error(), "MaxSessionDuration") && o.DurationSeconds <= 0 {
			// Role's MaxSessionDuration is less than our default; fall back to the role's own max.
			creds, err = getAssumeRoleCreds(ctx, roleARN, sessionName, profile, o.SessionPolicy, 0)
		}
		return creds, err
	}

	// For get-session-token, default to DefaultDurationSeconds (12h) when not configured.
	dur := o.DurationSeconds
	if dur <= 0 {
		dur = DefaultDurationSeconds
	}

	p := strings.TrimSpace(profile)
	args := []string{"sts", "get-session-token", "--output", "json"}
	if p != "" {
		args = append(args, "--profile", p)
	}
	if dur > 0 {
		args = append(args, "--duration-seconds", fmt.Sprintf("%d", dur))
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
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
		errMsg := strings.TrimSpace(string(out))
		if strings.Contains(errMsg, "AccessDenied") || strings.Contains(errMsg, "is not authorized") {
			return nil, fmt.Errorf(
				"aws sts assume-role: AccessDenied for role %s.\n"+
					"The role must exist in the AWS account and its trust policy must allow your IAM principal.\n"+
					"Either create the role with a trust policy granting your user sts:AssumeRole, "+
					"or set a different role_name in the AWS delegation config.\n"+
					"Original error: %s", roleARN, errMsg)
		}
		return nil, fmt.Errorf("aws sts assume-role: %w — %s", err, errMsg)
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
