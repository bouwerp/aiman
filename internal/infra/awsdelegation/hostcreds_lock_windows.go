//go:build windows

package awsdelegation

// lockHostCredentials is a no-op: serve runs on Unix hosts, and this file only keeps
// the package building elsewhere. Writes stay atomic through rename.
func lockHostCredentials(string) (func(), error) { return func() {}, nil }
