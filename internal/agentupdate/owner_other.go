//go:build !unix

package agentupdate

// OwnedByRoot is false where file ownership has no uid; serve runs only on
// Unix.
func OwnedByRoot(string) bool { return false }
