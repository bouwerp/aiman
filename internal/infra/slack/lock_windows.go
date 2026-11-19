//go:build windows

package slack

// lockCredentials is a no-op: the Slack CLI runs on Unix hosts, and this file
// only keeps the package building elsewhere. Store still renames atomically.
func lockCredentials(string) (func(), error) { return func() {}, nil }
