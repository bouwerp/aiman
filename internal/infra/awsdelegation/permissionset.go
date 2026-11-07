package awsdelegation

// PermissionSetPolicy returns BuildRegionPolicy(regions) pretty-printed, for
// pasting into an Identity Center permission set's inline policy.
func PermissionSetPolicy(regions []string) string {
	return ""
}

// PermissionSetSteps returns the one-time Identity Center setup steps for the
// delegated permission set.
func PermissionSetSteps(name, accountID string, regions []string) string {
	return ""
}
