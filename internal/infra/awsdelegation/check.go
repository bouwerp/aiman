package awsdelegation

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned by CheckCredentials.
var (
	ErrCredentialsExpired = errors.New("credentials expired")
	ErrProfileNotFound    = errors.New("profile not found on remote")
	ErrSSHFailure         = errors.New("SSH connection failed")
)

// CheckCredentials verifies whether the named AWS profile on the remote has
// valid (non-expired) credentials by running a lightweight STS identity call.
// Returns nil if credentials are valid.
// Returns ErrCredentialsExpired if the token has expired.
// Returns ErrProfileNotFound if the profile doesn't exist on the remote.
// Returns ErrSSHFailure if the SSH connection itself could not be established.
func CheckCredentials(ctx context.Context, r RemoteRunner, profileName string) error {
	out, err := r.Execute(ctx, "aws sts get-caller-identity --profile "+shellQuote(profileName)+" --output text 2>&1")
	lower := strings.ToLower(out)

	// SSH-level failures produce no useful AWS output — detect them first.
	if err != nil && (out == "" || strings.Contains(strings.ToLower(err.Error()), "ssh") ||
		strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "permission denied (publickey")) {
		// Only treat as SSH error if there's no AWS-looking output.
		if !strings.Contains(lower, "arn:") && !strings.Contains(lower, "token") &&
			!strings.Contains(lower, "profile") && !strings.Contains(lower, "credentials") {
			return fmt.Errorf("%w: %v", ErrSSHFailure, err)
		}
	}

	// Profile not found on remote.
	if strings.Contains(lower, "could not be found") || strings.Contains(lower, "no credentials provider") {
		return fmt.Errorf("%w: %s", ErrProfileNotFound, strings.TrimSpace(out))
	}

	if err != nil {
		// Command exited non-zero — check if it's an expiry error.
		if strings.Contains(lower, "expiredtoken") || strings.Contains(lower, "expired token") {
			return fmt.Errorf("%w: %s", ErrCredentialsExpired, strings.TrimSpace(out))
		}
		// Any other non-zero exit with AWS output is also treated as expired.
		return fmt.Errorf("%w: %s", ErrCredentialsExpired, strings.TrimSpace(out))
	}

	// Command succeeded — sanity check the output.
	if strings.Contains(lower, "expiredtoken") || strings.Contains(lower, "expired token") {
		return fmt.Errorf("%w: %s", ErrCredentialsExpired, strings.TrimSpace(out))
	}
	return nil
}

// ListCredentialProfiles returns the profile names present in
// ~/.aws/credentials on the remote. Returns an empty slice (not an error)
// when the file doesn't exist yet. Returns ErrSSHFailure if the SSH
// connection itself could not be established.
func ListCredentialProfiles(ctx context.Context, r RemoteRunner) ([]string, error) {
	out, err := r.Execute(ctx, "grep -oP '(?<=\\[)[^\\]]+' ~/.aws/credentials 2>/dev/null || true")
	lower := strings.ToLower(out)
	if err != nil {
		if !strings.Contains(lower, "arn:") && !strings.Contains(lower, "credentials") {
			return nil, fmt.Errorf("%w: %v", ErrSSHFailure, err)
		}
	}
	var profiles []string
	for _, line := range strings.Split(out, "\n") {
		p := strings.TrimSpace(line)
		if p != "" {
			profiles = append(profiles, p)
		}
	}
	return profiles, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
