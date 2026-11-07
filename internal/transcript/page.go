package transcript

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// Paging limits.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is one reply of session.transcript. Items are newest-last. On an
// After call, Items holds new items, Changed holds items first seen at or
// before the cursor whose rev is past it, and Removed holds ids that left the
// active branch. Apply Removed before Items: an id that left the branch and
// came back is in both. Before is nil at the top. Session is filled by the
// caller.
//
// After is where to resume. An After reply cut by the limit or the byte cap
// resumes short of the end of the file, so call again until a reply is empty.
// A reply to Page with a before cursor carries the end of the file, not what
// the caller has seen: keep the After from the newest page and later After
// calls instead.
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
// offsets just past a complete line; ordinal positions are Codex ordinals. An
// ordinal index with no lines yet is at -1, because Codex ordinals start at 0.
type Cursor struct {
	FileID   string
	Kind     string
	Position int64
	// sub is set when the cursor stops part way through what one line produced
	// (one line can add several items, so a page can end among them): it is the
	// sequence of the last change consumed at Position. Zero means all of it.
	sub int
}

type cursorWire struct {
	FileID   string `json:"f"`
	Kind     string `json:"k"`
	Position int64  `json:"p"`
	Sub      int    `json:"s,omitempty"`
}

// EncodeCursor returns an opaque, URL-safe string for c.
func EncodeCursor(c Cursor) string {
	// A struct of strings and integers always marshals.
	raw, _ := json.Marshal(cursorWire{c.FileID, c.Kind, c.Position, c.sub})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeCursor parses a string from EncodeCursor. A cursor that decodes but is
// out of date is not an error here: Page and After answer it with a reset.
func DecodeCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("transcript cursor: %w", err)
	}
	var w cursorWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Cursor{}, fmt.Errorf("transcript cursor: %w", err)
	}
	if w.Kind != PositionOffset && w.Kind != PositionOrdinal {
		return Cursor{}, errors.New("transcript cursor: unknown position kind")
	}
	if w.Position < -1 || w.Sub < 0 {
		return Cursor{}, errors.New("transcript cursor: position out of range")
	}
	return Cursor{FileID: w.FileID, Kind: w.Kind, Position: w.Position, sub: w.Sub}, nil
}

// MarshalText encodes the cursor as EncodeCursor does, so JSON carries an
// opaque string.
func (c Cursor) MarshalText() ([]byte, error) {
	return []byte(EncodeCursor(c)), nil
}

// UnmarshalText decodes a string from EncodeCursor.
func (c *Cursor) UnmarshalText(b []byte) error {
	got, err := DecodeCursor(string(b))
	if err != nil {
		return err
	}
	*c = got
	return nil
}
