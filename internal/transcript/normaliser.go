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
// is called after the last line of a read that consumed at least one line.
// Reset drops all state; the index calls it before rereading a file whole.
//
// line is valid only for the duration of Feed: copy what must outlive it. A
// blank line is not fed. On an ordinal index a position is forced above the
// previous one, so a line with no ordinal, or one out of order, still moves.
//
// The index applies deltas in the order returned, stamps Item.Rev with the
// position it applied the delta at, and caps text fields on its own copy. An
// Add for a live id acts as an Update, an Update or Remove for an unknown id
// is ignored, and an Add for a removed id is a new item at the current
// position. A normaliser is called by one goroutine at a time.
type Normaliser interface {
	Kind() string
	Capabilities() Capabilities
	PositionKind() string
	Feed(pos int64, line []byte) []Delta
	Flush() []Delta
	Reset()
}
