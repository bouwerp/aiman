package transcript

// Paging limits.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is one reply of session.transcript. Items are newest-last. On an
// After call, Items holds new items, Changed holds items first seen at or
// before the cursor whose rev is past it, and Removed holds ids that left the
// active branch. Before is nil at the top. Session is filled by the caller.
type Page struct {
	Type         string       `json:"type"`
	Session      string       `json:"session"`
	Subagent     string       `json:"subagent,omitempty"`
	Source       string       `json:"source"`
	Capabilities Capabilities `json:"capabilities"`
	Items        []Item       `json:"items"`
	Changed      []Item       `json:"changed,omitempty"`
	Removed      []string     `json:"removed,omitempty"`
	Before       *Cursor      `json:"before"`
	After        Cursor       `json:"after"`
	Reset        bool         `json:"reset"`
}

// Cursor position kinds. The two never compare.
const (
	PositionOffset  = "offset"
	PositionOrdinal = "ordinal"
)

// Cursor is a position in one transcript file. Offset positions are byte
// offsets just past a complete line; ordinal positions are Codex ordinals.
type Cursor struct {
	FileID   string
	Kind     string
	Position int64
}

// EncodeCursor returns an opaque, URL-safe string for c.
func EncodeCursor(c Cursor) string {
	return ""
}

// DecodeCursor parses a string from EncodeCursor.
func DecodeCursor(s string) (Cursor, error) {
	return Cursor{}, errNotImplemented
}

// MarshalText encodes the cursor as EncodeCursor does, so JSON carries an
// opaque string.
func (c Cursor) MarshalText() ([]byte, error) {
	return nil, errNotImplemented
}

// UnmarshalText decodes a string from EncodeCursor.
func (c *Cursor) UnmarshalText(b []byte) error {
	return errNotImplemented
}
