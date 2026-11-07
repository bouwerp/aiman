package transcript

import "unicode/utf8"

// Truncate cuts s to at most max bytes on a UTF-8 boundary and reports
// whether it cut.
func Truncate(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	if max <= 0 {
		return "", true
	}
	// A rune is at most UTFMax bytes, so stop backing up after that: input that
	// is not UTF-8 is still cut, rather than emptied.
	for cut := max; cut > 0 && cut > max-utf8.UTFMax; cut-- {
		if utf8.RuneStart(s[cut]) {
			return s[:cut], true
		}
	}
	return s[:max], true
}
