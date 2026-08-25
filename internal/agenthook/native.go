package agenthook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const nativeDirName = "native-sessions"

// Marker is the substring used to find Aiman's hook command in agent configs.
const Marker = "report-agent-session.sh"

// StoredPath is ~/.aiman/native-sessions/<session-id>. sessionID must be a
// single path element (UUID-shaped); anything else is rejected so a crafted
// AIMAN_ID cannot write outside the native-sessions directory.
func StoredPath(aimanDir, sessionID string) (string, error) {
	id := SafeSessionID(sessionID)
	if id == "" {
		return "", fmt.Errorf("invalid session id")
	}
	if strings.TrimSpace(aimanDir) == "" {
		return "", fmt.Errorf("aiman dir is empty")
	}
	return filepath.Join(aimanDir, nativeDirName, id), nil
}

// SafeSessionID returns id when it is a single path-safe token, otherwise "".
func SafeSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 80 {
		return ""
	}
	if strings.Contains(id, "..") {
		return ""
	}
	for _, r := range id {
		if !safeSessionIDRune(r) {
			return ""
		}
	}
	return id
}

func safeSessionIDRune(r rune) bool {
	if r > unicode.MaxASCII {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
}

type storedReport struct {
	ID      string `json:"id,omitempty"`
	Path    string `json:"path,omitempty"`
	State   string `json:"state,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message,omitempty"`
	Title   string `json:"title,omitempty"`
	Ended   bool   `json:"ended,omitempty"`
	Seq     int64  `json:"seq,omitempty"`
	At      string `json:"at,omitempty"`
}

// WriteStored records vendor identity and the latest hook facts for resume.
func WriteStored(aimanDir, sessionID string, r Report) error {
	path, err := StoredPath(aimanDir, sessionID)
	if err != nil {
		return err
	}
	if r.empty() {
		return fmt.Errorf("empty report")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	body, err := json.Marshal(storedFromReport(r))
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %s: %w", path, err)
	}
	return nil
}

func storedFromReport(r Report) storedReport {
	return storedReport{
		ID:      strings.TrimSpace(r.ID),
		Path:    strings.TrimSpace(r.Path),
		State:   string(r.State),
		Source:  r.Source,
		Message: r.Message,
		Title:   r.Title,
		Ended:   r.Ended,
		Seq:     r.Seq,
	}
}

func reportFromStored(s storedReport) Report {
	return Report{
		Native:  Native{ID: strings.TrimSpace(s.ID), Path: strings.TrimSpace(s.Path)},
		State:   normalizeState(s.State),
		Source:  strings.TrimSpace(s.Source),
		Message: strings.TrimSpace(s.Message),
		Title:   strings.TrimSpace(s.Title),
		Ended:   s.Ended,
		Seq:     s.Seq,
	}
}

// ParseStored reads a sidecar file or a hook payload.
func ParseStored(raw []byte) Report {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return Report{}
	}
	var s storedReport
	if err := json.Unmarshal(raw, &s); err == nil && (s.ID != "" || s.State != "" || s.Ended || s.Title != "") {
		return reportFromStored(s)
	}
	return ExtractReport(raw)
}

// vendorPathHints maps a substring of a hook-reported transcript/session path
// to the domain.Agent catalog name (internal/infra/agent.knownAgents) that
// owns it. Only vendors whose config directory install.go actually installs
// hooks into, and that unambiguously belong to one known agent, are listed —
// this is a conservative best-effort guess, not exhaustive: OpenCode and Pi
// report no path today, so a session run under either falls through to the
// manual agent picker instead of being guessed at.
var vendorPathHints = []struct {
	substr string
	name   string
}{
	{"/.claude/", "Claude Code"},
	{"/.codex/", "Codex CLI"},
	{"/.copilot/", "GitHub Copilot CLI"},
	{"/.cursor/", "Cursor"},
	{"/.grok/", "Grok Build CLI"},
}

// InferAgentName makes a best-effort guess at which known agent produced a
// hook-reported transcript/session path, for reviving a session whose
// AgentName was never recorded (or was lost along with its database row).
// Returns "" when the path carries no recognizable vendor hint — callers
// must fall back to asking, not guessing.
func InferAgentName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	for _, hint := range vendorPathHints {
		if strings.Contains(path, hint.substr) {
			return hint.name
		}
	}
	return ""
}

// ListSidecarsCmd dumps every native-session sidecar in one SSH round trip.
const ListSidecarsCmd = `for f in "$HOME"/.aiman/native-sessions/*; do [ -f "$f" ] || continue; printf 'ID %s\n' "$(basename "$f")"; cat "$f"; printf '\nEND\n'; done`

// ParseSidecarDump parses ListSidecarsCmd output.
func ParseSidecarDump(raw string) map[string]Report {
	out := map[string]Report{}
	var id string
	var buf []string
	flush := func() {
		if id == "" {
			return
		}
		r := ParseStored([]byte(strings.Join(buf, "\n")))
		if !r.empty() {
			out[id] = r
		}
		id, buf = "", nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "ID ") {
			flush()
			id = SafeSessionID(strings.TrimSpace(strings.TrimPrefix(line, "ID ")))
			continue
		}
		if line == "END" {
			flush()
			continue
		}
		if id != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}
