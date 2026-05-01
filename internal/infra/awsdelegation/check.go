package awsdelegation

import (
	"context"
	"fmt"
	"strings"
)

// CheckCredentials verifies whether the named AWS profile on the remote has
// valid (non-expired) credentials by running a lightweight STS identity call.
// Returns nil if credentials are valid, non-nil if expired or missing.
func CheckCredentials(ctx context.Context, r RemoteRunner, profileName string) error {
	out, err := r.Execute(ctx, "aws sts get-caller-identity --profile "+shellQuote(profileName)+" --output text 2>&1")
	if err != nil {
		return err
	}
	lower := strings.ToLower(out)
	if strings.Contains(lower, "expiredtoken") || strings.Contains(lower, "expired") || strings.Contains(lower, "invalid") {
		return fmt.Errorf("credentials expired: %s", strings.TrimSpace(out))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
