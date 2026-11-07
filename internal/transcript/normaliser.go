package transcript

// Delta is one change a normaliser makes: exactly one field is set. Add
// appends an item, Update replaces the item with the same id (its rev moves),
// Remove drops an id from the active branch.
type Delta struct {
	Add    *Item
	Update *Item
	Remove string
}

// Normaliser turns one vendor's transcript lines into item deltas.
//
// Kind is the agent kind ("claude", "codex", "pi", "grok"). PositionKind is
// PositionOffset or PositionOrdinal. Feed receives each complete line with
// its position: for offset normalisers the byte offset just past the line
// (newline included); for ordinal normalisers the line's top-level "ordinal"
// field. Flush returns deltas held back for an open group (Grok chunks) and
// is called after the last line of a read. Reset drops all state; the index
// calls it before rereading a file whole.
type Normaliser interface {
	Kind() string
	Capabilities() Capabilities
	PositionKind() string
	Feed(pos int64, line []byte) []Delta
	Flush() []Delta
	Reset()
}
