//go:build windows

package images

// lockFile is a no-op on Windows, where serve does not run the harness.
func lockFile(string) (func(), error) { return func() {}, nil }
