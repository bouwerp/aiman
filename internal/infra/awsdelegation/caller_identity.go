package awsdelegation

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type getCallerIdentityOutput struct {
	Account string `json:"Account"`
}

// AccountIDFromLocalProfile runs `aws sts get-caller-identity` (optionally with --profile)
// on this machine and returns the 12-digit account ID.
func AccountIDFromLocalProfile(ctx context.Context, profile string) (string, error) {
	p := strings.TrimSpace(profile)
	args := []string{"sts", "get-caller-identity", "--output", "json"}
	if p != "" {
		args = append(args, "--profile", p)
	}

	// #nosec G204 — profile comes from local ~/.aws section names or user input in TUI (not remote).
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("aws sts get-caller-identity: %w — %s", err, strings.TrimSpace(string(out)))
	}
	var o getCallerIdentityOutput
	if err := json.Unmarshal(out, &o); err != nil {
		return "", fmt.Errorf("parse aws JSON: %w", err)
	}
	if !awsAccountIDRe.MatchString(o.Account) {
		return "", fmt.Errorf("aws returned invalid account ID %q", o.Account)
	}
	return o.Account, nil
}
