package awsdelegation

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors returned by CheckCredentials.
var (
	ErrCredentialsExpired  = errors.New("credentials expired")
	ErrProfileNotFound     = errors.New("profile not found on remote")
)

// CheckCredentials verifies whether the named AWS profile on the remote has
// valid (non-expired) credentials by running a lightweight STS identity call.
// Returns nil if credentials are valid.
// Returns ErrCredentialsExpired if the token has expired.
// Returns ErrProfileNotFound if the profile doesn't exist on the remote.
func CheckCredentials(ctx context.Context, r RemoteRunner, profileName string) error {
	out, err := r.Execute(ctx, "aws sts get-caller-identity --profile "+shellQuote(profileName)+" --output text 2>&1")
	lower := strings.ToLower(out)

	// Check for "profile not found" first (regardless of err).
	if strings.Contains(lower, "could not be found") || strings.Contains(lower, "no credentials") {
		return fmt.Errorf("%w: %s", ErrProfileNotFound, strings.TrimSpace(out))
	}

	if err != nil {
		// If stderr mentions expiry, classify as expired.
		if strings.Contains(lower, "expiredtoken") || strings.Contains(lower, "expired") {
			return fmt.Errorf("%w: %s", ErrCredentialsExpired, strings.TrimSpace(out))
		}
		return err
	}

	// Output present and no error — but double-check for expiry strings in output.
	if strings.Contains(lower, "expiredtoken") || strings.Contains(lower, "expired") || strings.Contains(lower, "invalid") {
		return fmt.Errorf("%w: %s", ErrCredentialsExpired, strings.TrimSpace(out))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
