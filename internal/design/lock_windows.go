//go:build windows

package design

// lockFile is a no-op: serve runs on Unix hosts, and this file only keeps the
// package building elsewhere.
func lockFile(string) (func(), error) { return func() {}, nil }
