// Package remotefile writes files through a RemoteExecutor only when their
// content differs, so scaffolding files in an agent's cached prompt prefix
// keep their bytes and mtime across session starts.
package remotefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Writer is the part of domain.RemoteExecutor that WriteIfChanged needs.
type Writer interface {
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
}

// WriteIfChanged hashes the remote file in one round trip and writes content
// only when the hash differs. Anything short of a matching hash (an Execute
// error, a missing or unreadable file, a remote without sha256sum, malformed
// output) counts as changed, so a file is never skipped on a guess.
func WriteIfChanged(ctx context.Context, w Writer, path string, content []byte) (changed bool, err error) {
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	out, execErr := w.Execute(ctx, "sha256sum -- "+shellQuote(path)+" 2>/dev/null || true")
	if execErr == nil && remoteHash(out) == want {
		return false, nil
	}
	if err := w.WriteFile(ctx, path, content); err != nil {
		return false, err
	}
	return true, nil
}

// remoteHash returns the first field of the last non-empty output line. A
// login shell may print profile noise before the sha256sum line.
func remoteHash(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// shellQuote wraps s in single quotes for POSIX sh, escaping any single quote
// it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
