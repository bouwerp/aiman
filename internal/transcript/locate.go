package transcript

import "errors"

// ErrNoTranscript means no transcript file was found for a session.
var ErrNoTranscript = errors.New("no transcript")

// Fingerprint identifies a transcript file by its first complete record:
// the first line with a "uuid" (Claude header lines such as last-prompt and
// ai-title have none and are skipped), else the first complete line, hashed.
func Fingerprint(path string) (string, error) {
	return "", errNotImplemented
}

// Locate finds a session's transcript. A non-empty sessionPath that exists
// wins. Otherwise, by agent: claude ~/.claude/projects/<cwd, every
// non-alphanumeric byte as '-'>/<id>.jsonl; codex
// ~/.codex/sessions/*/*/*/rollout-*-<id>.jsonl; pi
// ~/.pi/agent/sessions/--<cwd without its leading '/', '/' as '-'>--/*_<id>.jsonl;
// grok ~/.grok/sessions/<url.PathEscape(cwd)>/<id>/updates.jsonl. kind is the
// agent kind ("claude", "codex", "pi", "grok"). Nothing found is an error
// matching ErrNoTranscript.
func Locate(home, agent, sessionID, sessionPath, cwd string) (path string, kind string, err error) {
	return "", "", ErrNoTranscript
}
