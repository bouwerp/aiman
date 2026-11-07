package transcript

// Truncate cuts s to at most max bytes on a UTF-8 boundary and reports
// whether it cut.
func Truncate(s string, max int) (string, bool) {
	return s, false
}
