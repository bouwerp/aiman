//go:build windows

package design

import "context"

// lockFile is a no-op: serve runs on Unix hosts, and this file only keeps the
// package building elsewhere.
func lockFile(context.Context, string) (func(), error) { return func() {}, nil }
