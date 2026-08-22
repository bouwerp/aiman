package agenthook

import (
	"strings"
)

// WithResume appends the vendor resume flag or subcommand when nativeID is set
// and the command does not already resume.
func WithResume(command, nativeID string) string {
	command = strings.TrimSpace(command)
	nativeID = strings.TrimSpace(nativeID)
	if command == "" || nativeID == "" {
		return command
	}
	lower := strings.ToLower(command)
	if strings.Contains(lower, "--resume") || strings.Contains(lower, " resume ") ||
		strings.Contains(lower, "--session") || strings.Contains(lower, "--conversation") {
		return command
	}
	fields := strings.Fields(command)
	base := ""
	if len(fields) > 0 {
		base = strings.ToLower(fields[0])
	}
	switch {
	case strings.Contains(base, "codex"):
		return fields[0] + " resume " + nativeID + restArgs(fields[1:])
	case strings.Contains(base, "copilot"):
		return fields[0] + " --resume=" + nativeID + restArgs(fields[1:])
	case strings.Contains(base, "agy") || strings.Contains(base, "antigravity"):
		return fields[0] + " --conversation " + nativeID + restArgs(fields[1:])
	case strings.Contains(base, "opencode"):
		return fields[0] + " --session " + nativeID + restArgs(fields[1:])
	case strings.Contains(base, "pi"):
		return fields[0] + " --session " + nativeID + restArgs(fields[1:])
	default:
		return fields[0] + " --resume " + nativeID + restArgs(fields[1:])
	}
}

func restArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return " " + strings.Join(args, " ")
}
