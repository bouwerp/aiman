// Package shellx holds the one POSIX shell quoting function the module uses.
package shellx

import "strings"

// Quote returns s as one single-quoted POSIX shell word, so the shell expands
// nothing inside it. An embedded single quote closes the word, adds an escaped
// quote and reopens it.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// QuoteHomePath quotes a remote path like Quote, except that a leading "$HOME/"
// or "~/" stays unquoted as "$HOME"/ so the remote shell expands the home
// directory. Everything after it is a quoted word, so nothing else expands.
func QuoteHomePath(p string) string {
	for _, prefix := range []string{"$HOME/", "~/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return `"$HOME"/` + Quote(rest)
		}
	}
	return Quote(p)
}
