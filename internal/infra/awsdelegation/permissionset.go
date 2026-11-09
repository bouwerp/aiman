package awsdelegation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultPermissionSetName is the delegated permission set's name when the
// operator does not choose one.
const DefaultPermissionSetName = "aiman-delegated"

// PermissionSetPolicy returns the BuildRegionPolicy document pretty-printed, for
// pasting into an Identity Center permission set's inline policy. It returns an
// empty string when regions is nil or empty.
func PermissionSetPolicy(regions []string) string {
	doc := regionPolicyDocument(regions)
	if doc == nil {
		return ""
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// PermissionSetSteps returns the one-time Identity Center setup steps for the
// delegated permission set. An empty name means DefaultPermissionSetName. An
// empty accountID or regions leaves that step in general terms, since the steps
// are still correct without them.
func PermissionSetSteps(name, accountID string, regions []string) string {
	if name = strings.TrimSpace(name); name == "" {
		name = DefaultPermissionSetName
	}
	account := "the AWS account agents use"
	if id := strings.TrimSpace(accountID); id != "" {
		account = "account " + id
	}
	policy := PermissionSetPolicy(regions)
	if policy == "" {
		policy = "(print it with: aiman aws permission-set-policy --regions <regions>)"
	}
	return fmt.Sprintf(`Or run: aiman aws permission-set-apply --profile <admin profile> --account <id>

Set this up once, in IAM Identity Center:

1. Create a custom permission set named %[1]s, with session duration PT12H.
2. Give it this inline policy:

%[2]s

3. Assign it to your user for %[3]s.
4. In the aiman delegation for that account, set sso_role_name: %[1]s
`, name, policy, account)
}
