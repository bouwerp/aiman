//go:build windows

package gateway

import "context"

// lockFile is a no-op: the gateway runs on Unix hosts, and this file only
// keeps the package building elsewhere. Writes stay atomic through rename.
func lockFile(context.Context, string) (func(), error) { return func() {}, nil }
