// Package harnessscanner carries the Dockerfile of the harness scanner image,
// which serve builds on the host from these pinned inputs.
package harnessscanner

import _ "embed"

// Dockerfile is the scanner image's Dockerfile. Its build args pin every
// tool's version and sha256.
//
//go:embed Dockerfile
var Dockerfile []byte
