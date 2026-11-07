// Package shellx holds the one POSIX shell quoting function the module uses.
package shellx

import "strings"

// Quote returns s as one single-quoted POSIX shell word, so the shell expands
// nothing inside it. An embedded single quote closes the word, adds an escaped
// quote and reopens it.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
