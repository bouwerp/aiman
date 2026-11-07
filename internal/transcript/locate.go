package transcript

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/usage"
)

// ErrNoTranscript means no transcript file was found for a session.
var ErrNoTranscript = errors.New("no transcript")

// fingerprintHead is the prefix read to identify a file, as usage reads it.
const fingerprintHead = 64 << 10

// Fingerprint identifies a transcript file by its first complete record:
// the first line with a "uuid" (Claude header lines such as last-prompt and
// ai-title have none and are skipped), else the first complete line, hashed.
// A file with no complete line yet is "", unless that line already fills the
// head, which is then hashed so the id cannot move when the line completes.
func Fingerprint(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the caller located this transcript
	if err != nil {
		return "", fmt.Errorf("transcript fingerprint: %w", err)
	}
	defer func() { _ = f.Close() }()
	return fingerprintFile(f)
}

func fingerprintFile(r io.ReaderAt) (string, error) {
	buf := make([]byte, fingerprintHead)
	n, err := r.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("transcript fingerprint: %w", err)
	}
	head := buf[:n]
	if fp := usage.ClaudeFingerprint(head); fp != "" {
		return fp, nil
	}
	if n == fingerprintHead {
		// ClaudeFingerprint hashes the first complete line, and with no newline
		// in the head the head is all there is of it.
		return usage.ClaudeFingerprint(append(head, '\n')), nil
	}
	return "", nil
}

// Locate finds a session's transcript. A non-empty sessionPath that exists
// wins. Otherwise, by agent: claude ~/.claude/projects/<cwd, every
// non-alphanumeric byte as '-'>/<id>.jsonl; codex
// ~/.codex/sessions/*/*/*/rollout-*-<id>.jsonl; pi
// ~/.pi/agent/sessions/--<cwd without its leading '/', '/' as '-'>--/*_<id>.jsonl;
// grok ~/.grok/sessions/<url.PathEscape(cwd)>/<id>/updates.jsonl. kind is the
// agent kind ("claude", "codex", "pi", "grok"). Nothing found, or any other
// agent, is an error matching ErrNoTranscript.
func Locate(home, agent, sessionID, sessionPath, cwd string) (path string, kind string, err error) {
	pattern, known := vendorPattern(home, agent, sessionID, cwd)
	if !known {
		return "", "", fmt.Errorf("%w: agent %q has no transcript format", ErrNoTranscript, agent)
	}
	if sessionPath != "" && isFile(sessionPath) {
		return sessionPath, agent, nil
	}
	if pattern != "" {
		// Glob sorts by name. Codex and Pi names start with a date or time, so the
		// last match is the newest.
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 && isFile(matches[len(matches)-1]) {
			return matches[len(matches)-1], agent, nil
		}
	}
	return "", "", fmt.Errorf("%w: %s session %q", ErrNoTranscript, agent, sessionID)
}

// vendorPattern returns the glob for an agent's session. It is empty when the
// id or working directory cannot name a file; known is false for any agent
// with no transcript format.
func vendorPattern(home, agent, id, cwd string) (pattern string, known bool) {
	switch agent {
	case "claude", "codex", "pi", "grok":
	default:
		return "", false
	}
	// A session id comes from a hook payload, so it must stay one path element.
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || (cwd == "" && agent != "codex") {
		return "", true
	}
	base, id := globEscape(home), globEscape(id)
	switch agent {
	case "claude":
		return filepath.Join(base, ".claude/projects", claudeSlug(cwd), id+".jsonl"), true
	case "codex":
		return filepath.Join(base, ".codex/sessions/*/*/*", "rollout-*-"+id+".jsonl"), true
	case "pi":
		dir := "--" + strings.ReplaceAll(strings.TrimPrefix(cwd, "/"), "/", "-") + "--"
		return filepath.Join(base, ".pi/agent/sessions", globEscape(dir), "*_"+id+".jsonl"), true
	default:
		return filepath.Join(base, ".grok/sessions", globEscape(url.PathEscape(cwd)), id, "updates.jsonl"), true
	}
}

// claudeSlug writes every byte of cwd that is not an ASCII letter or digit as
// '-', byte by byte, so a multi-byte rune becomes several.
func claudeSlug(cwd string) string {
	out := []byte(cwd)
	for i, c := range out {
		alnum := c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !alnum {
			out[i] = '-'
		}
	}
	return string(out)
}

var globMeta = strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`)

func globEscape(s string) string {
	return globMeta.Replace(s)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
