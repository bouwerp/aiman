package agenthook

import "strings"

// NativeIdentityFitsCommand reports whether a vendor transcript path belongs
// to the agent binary in command. An empty path is treated as unknown and
// allowed; a known foreign path (e.g. ~/.claude/… into grok) is rejected so a
// stale sidecar cannot force --resume across agents.
func NativeIdentityFitsCommand(command, path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return true
	}
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return true
	}
	base := strings.ToLower(fields[0])
	vendor := nativePathVendor(path)
	if vendor == "" {
		return true
	}
	return nativeCommandVendor(base) == vendor
}

func nativePathVendor(path string) string {
	switch {
	case strings.Contains(path, "/.claude/") || strings.HasSuffix(path, "/.claude"):
		return "claude"
	case strings.Contains(path, "/.codex/") || strings.HasSuffix(path, "/.codex"):
		return "codex"
	case strings.Contains(path, "/.grok/") || strings.HasSuffix(path, "/.grok"):
		return "grok"
	case strings.Contains(path, "/.cursor/") || strings.HasSuffix(path, "/.cursor"):
		return "cursor"
	case strings.Contains(path, "/.copilot/") || strings.Contains(path, "/copilot/"):
		return "copilot"
	case strings.Contains(path, "/.kilo/") || strings.Contains(path, "/kilo/"):
		return "kilo"
	case strings.Contains(path, "/.pi/") || strings.Contains(path, "/pi/"):
		return "pi"
	case strings.Contains(path, "/.gemini/") || strings.Contains(path, "/antigravity/") || strings.Contains(path, "/agy/"):
		return "agy"
	default:
		return ""
	}
}

func nativeCommandVendor(base string) string {
	switch {
	case strings.Contains(base, "claude"):
		return "claude"
	case strings.Contains(base, "codex"):
		return "codex"
	case strings.Contains(base, "grok"):
		return "grok"
	case strings.Contains(base, "cursor"):
		return "cursor"
	case strings.Contains(base, "copilot"):
		return "copilot"
	case strings.Contains(base, "kilo"):
		return "kilo"
	case base == "pi" || strings.HasPrefix(base, "pi-"):
		return "pi"
	case strings.Contains(base, "agy") || strings.Contains(base, "antigravity") || strings.Contains(base, "gemini"):
		return "agy"
	default:
		return ""
	}
}
