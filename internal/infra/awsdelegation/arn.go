package awsdelegation

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultDelegatedRoleName is used when role_name is omitted in config; matches the
// conventional name for Aiman-managed assume-role profiles.
const DefaultDelegatedRoleName = "TemporaryDelegatedRole"

// DefaultDelegatedProfileName is the default [profile …] section name on the remote ~/.aws/config.
const DefaultDelegatedProfileName = "default"

var (
	awsAccountIDRe = regexp.MustCompile(`^\d{12}$`)
	// IAM role name: alphanumeric and +=,.@_-
	iamRoleNameRe = regexp.MustCompile(`^[\w+=,.@-]+$`)
)

// RoleARNFromParts builds arn:aws:iam::{account}:role/{roleName} for ~/.aws/config.
// accountID must be exactly 12 digits. Empty roleName uses DefaultDelegatedRoleName.
func RoleARNFromParts(accountID, roleName string) (string, error) {
	acct := strings.TrimSpace(accountID)
	if !awsAccountIDRe.MatchString(acct) {
		return "", fmt.Errorf("account_id must be exactly 12 digits")
	}
	rn := strings.TrimSpace(roleName)
	if rn == "" {
		rn = DefaultDelegatedRoleName
	}
	if len(rn) > 64 {
		return "", fmt.Errorf("role name too long (max 64)")
	}
	if !iamRoleNameRe.MatchString(rn) {
		return "", fmt.Errorf("role name has invalid characters for IAM")
	}
	return fmt.Sprintf("arn:aws:iam::%s:role/%s", acct, rn), nil
}
