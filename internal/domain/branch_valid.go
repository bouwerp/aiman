package domain

// ValidBranchName reports whether name is a branch name git's
// `check-ref-format --branch` accepts and that cannot be read as an option.
//
// Stub: accepts everything until the validator lands.
func ValidBranchName(name string) error {
	_ = name
	return nil
}
