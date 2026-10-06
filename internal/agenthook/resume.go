package agenthook

import (
	"strings"
	"unicode"
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
	prefix, argv := splitCommand(command)
	if len(argv) == 0 {
		return command
	}
	base := strings.ToLower(strings.Trim(argv[0], `"'`))
	var resumed string
	switch {
	case strings.Contains(base, "codex"):
		resumed = argv[0] + " resume " + nativeID + restArgs(argv[1:])
	case strings.Contains(base, "copilot"):
		resumed = argv[0] + " --resume=" + nativeID + restArgs(argv[1:])
	case strings.Contains(base, "agy") || strings.Contains(base, "antigravity"):
		resumed = argv[0] + " --conversation " + nativeID + restArgs(argv[1:])
	case strings.Contains(base, "kilo"):
		resumed = argv[0] + " --session " + nativeID + restArgs(argv[1:])
	case strings.Contains(base, "pi"):
		resumed = argv[0] + " --session " + nativeID + restArgs(argv[1:])
	case base == "muse":
		// Root flags have to precede `resume`. `muse resume <id>` with the
		// bypass flags after the id still opens the permission UI first, and
		// that UI exits when it cannot read the cursor. `--resume` is not a flag.
		resumed = argv[0] + restArgs(argv[1:]) + " resume " + nativeID
	default:
		resumed = argv[0] + " --resume " + nativeID + restArgs(argv[1:])
	}
	if prefix == "" {
		return resumed
	}
	return prefix + " " + resumed
}

// splitCommand peels leading KEY=VALUE assignments off a shell command so the
// binary is still the token resume flags attach to. Deep Code takes its model
// and reasoning effort that way; its CLI rejects unknown flags.
func splitCommand(command string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(command))
	i := 0
	for i < len(fields) && isShellEnvAssign(fields[i]) {
		i++
	}
	prefix := ""
	if i > 0 {
		prefix = strings.Join(fields[:i], " ")
	}
	return prefix, fields[i:]
}

func isShellEnvAssign(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return false
	}
	for _, r := range s[:eq] {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	first := rune(s[0])
	return first == '_' || unicode.IsLetter(first)
}

func restArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return " " + strings.Join(args, " ")
}
