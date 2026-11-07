package transcript

import "fmt"

// Index defaults.
const (
	DefaultMaxFieldBytes = 64 << 10
	DefaultMaxPageBytes  = 2 << 20
)

// IndexOptions caps sizes. Zero values take the defaults.
type IndexOptions struct {
	// MaxFieldBytes caps Item.Text, ToolCall.Input and ToolCall.Result,
	// setting Item.Truncated when cut.
	MaxFieldBytes int
	// MaxPageBytes caps the JSON encoding of a page's Items plus Changed.
	// A page always holds at least one item when any remain.
	MaxPageBytes int
}

// Index holds the items of one transcript file.
type Index struct {
	path string
	norm Normaliser
}

// NewIndex returns an index over path. Nothing is read until Refresh.
func NewIndex(path string, n Normaliser, opts IndexOptions) *Index {
	return &Index{path: path, norm: n}
}

// Refresh reads lines appended since the last call. A trailing partial line
// is not consumed. It rereads the file whole and returns reset true when the
// file shrank, its fingerprint changed, or the record at the last indexed
// offset no longer matches its stored length and checksum.
func (ix *Index) Refresh() (reset bool, err error) {
	return false, fmt.Errorf("transcript index %s: %w", ix.path, errNotImplemented)
}

// Page refreshes, then returns up to limit items whose first position is
// before the cursor (the newest page when before is nil). limit <= 0 means
// DefaultLimit; above MaxLimit it is MaxLimit. A cursor for another file id,
// another position kind, or one issued before a reset returns the newest page
// with Reset true.
func (ix *Index) Page(before *Cursor, limit int) (Page, error) {
	return Page{Capabilities: ix.norm.Capabilities()}, errNotImplemented
}

// After refreshes, then returns what changed past c: new items in Items,
// updated older items in Changed, removed ids in Removed. A stale cursor
// returns the newest page with Reset true, as Page does.
func (ix *Index) After(c Cursor, limit int) (Page, error) {
	return Page{}, errNotImplemented
}

// Item returns one indexed item by id.
func (ix *Index) Item(id string) (Item, bool) {
	return Item{}, false
}

// FileID returns the id cursors from this index carry. It changes when the
// file is replaced or rewritten.
func (ix *Index) FileID() string {
	return ""
}
