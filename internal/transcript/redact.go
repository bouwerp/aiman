package transcript

// Redact returns one JSON line with every string value replaced, except the
// values of the enum-like keys type, role, name, status, subtype,
// sessionUpdate, kind, state and method. Equal strings map to equal
// replacements, so ids still pair. Keys, numbers, booleans and structure are
// kept. Fixtures under testdata are passed through it.
func Redact(line []byte) []byte {
	return nil
}
