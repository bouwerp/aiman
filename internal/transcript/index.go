package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

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
//
// It is safe for concurrent use. Reads of the file and the normaliser are
// serialised, so Refresh and the Page and After calls that refresh take turns
// on the file; Item and FileID never wait on the file. Items returned share
// their ToolCall, Subagent and Process values with the index: do not modify
// them.
type Index struct {
	path     string
	norm     Normaliser
	opts     IndexOptions
	caps     Capabilities
	posKind  string
	startPos int64

	readMu sync.Mutex // guards the fields down to mu, and norm
	loaded bool       // a whole read finished
	fed    bool       // norm has state from a read
	reset  bool       // a reset not yet reported, because its reread failed
	offset int64      // bytes through the last consumed line
	pos    int64      // position of the last consumed line
	last   lastRecord
	fp     string
	gen    string // set by a reset, so a cursor from before it cannot match
	size   int64
	mtime  time.Time

	mu sync.RWMutex
	s  *state
}

// lastRecord is how a rewrite that keeps the head of the file shows: the bytes
// at the last indexed position are no longer the line read there.
type lastRecord struct {
	start  int64
	length int64 // includes the newline; zero when nothing is indexed
	sum    uint32
}

// NewIndex returns an index over path. Nothing is read until Refresh.
func NewIndex(path string, n Normaliser, opts IndexOptions) *Index {
	if opts.MaxFieldBytes <= 0 {
		opts.MaxFieldBytes = DefaultMaxFieldBytes
	}
	if opts.MaxPageBytes <= 0 {
		opts.MaxPageBytes = DefaultMaxPageBytes
	}
	ix := &Index{path: path, norm: n, opts: opts, caps: n.Capabilities(), posKind: n.PositionKind()}
	if ix.posKind == PositionOrdinal {
		ix.startPos = -1 // ordinals start at 0, so 0 means a line was read
	}
	ix.pos = ix.startPos
	ix.s = newState(ix.startPos)
	return ix
}

// Refresh reads lines appended since the last call. A trailing partial line
// is not consumed. It rereads the file whole and returns reset true when the
// file shrank, its fingerprint changed, or the record at the last indexed
// offset no longer matches its stored length and checksum. A missing file is
// an error matching os.ErrNotExist, and the items already read stay.
func (ix *Index) Refresh() (reset bool, err error) {
	ix.readMu.Lock()
	defer ix.readMu.Unlock()

	f, err := os.Open(ix.path)
	if err != nil {
		return false, fmt.Errorf("transcript index %s: %w", ix.path, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("transcript index %s: %w", ix.path, err)
	}
	stale, fp, err := ix.checkStale(f, info)
	if err != nil {
		return false, fmt.Errorf("transcript index %s: %w", ix.path, err)
	}
	if stale || !ix.loaded {
		err = ix.reread(f, info, fp, stale || ix.reset)
	} else {
		err = ix.readAppended(f, info, fp)
	}
	if err != nil {
		return false, fmt.Errorf("transcript index %s: %w", ix.path, err)
	}
	reset, ix.reset = ix.reset, false
	return reset, nil
}

// checkStale decides whether what is indexed still describes the file. The
// last record is checked on every call, because a same-size rewrite can land
// within one tick of the mtime; the fingerprint only once the file has moved.
func (ix *Index) checkStale(f *os.File, info os.FileInfo) (stale bool, fp string, err error) {
	fp = ix.fp
	if !ix.loaded {
		fp, err = fingerprintFile(f)
		return false, fp, err
	}
	if info.Size() < ix.offset {
		return true, "", nil
	}
	if info.Size() != ix.size || !info.ModTime().Equal(ix.mtime) {
		fp, err = fingerprintFile(f)
		if err != nil {
			return false, "", err
		}
		// An empty fingerprint had no complete line behind it, so a first one
		// replaces nothing.
		if ix.fp != "" && fp != ix.fp {
			return true, fp, nil
		}
	}
	same, err := ix.last.matches(f)
	if err != nil {
		return false, "", err
	}
	return !same, fp, nil
}

func (r lastRecord) matches(f io.ReaderAt) (bool, error) {
	if r.length == 0 {
		return true, nil
	}
	buf := make([]byte, r.length)
	if _, err := f.ReadAt(buf, r.start); err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	last := len(buf) - 1
	return buf[last] == '\n' && crc32.ChecksumIEEE(buf[:last]) == r.sum, nil
}

// reread builds a new state on the side and swaps it in whole, so a reader
// never sees a half-read file. A failed read leaves the old state and the next
// Refresh rereads again.
func (ix *Index) reread(f *os.File, info os.FileInfo, fp string, reset bool) error {
	ix.reset = reset
	ix.loaded = false
	if ix.fed {
		ix.norm.Reset()
	}
	ix.fed = true
	if fp == "" {
		got, err := fingerprintFile(f)
		if err != nil {
			return err
		}
		fp = got
	}
	ix.offset, ix.pos, ix.last = 0, ix.startPos, lastRecord{}

	fresh := newState(ix.startPos)
	err := ix.readLinesFrom(f, info, func(pos int64, deltas []Delta) {
		fresh.apply(pos, deltas)
	})
	if err != nil {
		return err
	}
	fresh.compact()
	if reset {
		ix.gen = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	ix.loaded = true
	ix.noteFile(info, fp)
	fresh.end, fresh.fileID = ix.pos, ix.fileID()
	ix.mu.Lock()
	ix.s = fresh
	ix.mu.Unlock()
	return nil
}

type lineChange struct {
	pos    int64
	deltas []Delta
}

// readAppended applies a whole read in one step, so a cursor handed out never
// sits at a position whose changes are only partly applied. Lines consumed
// before a read error still apply, because the normaliser has seen them.
func (ix *Index) readAppended(f *os.File, info os.FileInfo, fp string) error {
	var changes []lineChange
	err := ix.readLinesFrom(f, info, func(pos int64, deltas []Delta) {
		changes = append(changes, lineChange{pos, deltas})
	})
	ix.noteFile(info, fp)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, c := range changes {
		ix.s.apply(c.pos, c.deltas)
	}
	ix.s.compact()
	ix.s.end, ix.s.fileID = ix.pos, ix.fileID()
	return err
}

// readLinesFrom feeds the complete lines between the current offset and the
// size stat saw, then flushes when it consumed any. emit gets prepared deltas,
// and only for lines that produced some.
func (ix *Index) readLinesFrom(f *os.File, info os.FileInfo, emit func(int64, []Delta)) error {
	if info.Size() <= ix.offset {
		return nil
	}
	read := false
	err := readLines(io.NewSectionReader(f, ix.offset, info.Size()-ix.offset), func(line []byte) error {
		read = true
		length := int64(len(line)) + 1
		ix.last = lastRecord{start: ix.offset, length: length, sum: crc32.ChecksumIEEE(line)}
		ix.offset += length
		if len(line) == 0 {
			return nil
		}
		ix.pos = ix.nextPos(line)
		if deltas := ix.norm.Feed(ix.pos, line); len(deltas) > 0 {
			emit(ix.pos, ix.prepare(ix.pos, deltas))
		}
		return nil
	})
	if read {
		if deltas := ix.norm.Flush(); len(deltas) > 0 {
			emit(ix.pos, ix.prepare(ix.pos, deltas))
		}
	}
	return err
}

// nextPos returns a line's position. An ordinal is forced past the previous
// one: a cursor at a position stands for all of it, so two lines must not
// share one.
func (ix *Index) nextPos(line []byte) int64 {
	if ix.posKind != PositionOrdinal {
		return ix.offset
	}
	var probe struct {
		Ordinal *int64 `json:"ordinal"`
	}
	if err := json.Unmarshal(line, &probe); err == nil && probe.Ordinal != nil && *probe.Ordinal > ix.pos {
		return *probe.Ordinal
	}
	return ix.pos + 1
}

func (ix *Index) noteFile(info os.FileInfo, fp string) {
	ix.size, ix.mtime, ix.fp = info.Size(), info.ModTime(), fp
}

func (ix *Index) fileID() string {
	if ix.gen == "" {
		return ix.fp
	}
	return ix.fp + "~" + ix.gen
}

// prepare copies deltas at the moment a normaliser returned them, because it
// may reuse what they point at for its next line.
func (ix *Index) prepare(pos int64, deltas []Delta) []Delta {
	out := make([]Delta, len(deltas))
	for i, d := range deltas {
		out[i].Remove = d.Remove
		if d.Add != nil {
			it := ix.capItem(*d.Add)
			it.Rev = pos
			out[i].Add = &it
		}
		if d.Update != nil {
			it := ix.capItem(*d.Update)
			it.Rev = pos
			out[i].Update = &it
		}
	}
	return out
}

// capItem returns the stored copy of it: fields capped, and pointers cloned so
// neither the normaliser nor the cap changes the other's value.
func (ix *Index) capItem(it Item) Item {
	var cut bool
	it.Text, cut = Truncate(it.Text, ix.opts.MaxFieldBytes)
	it.Truncated = it.Truncated || cut
	if it.ToolCall != nil {
		tc := *it.ToolCall
		var inCut, resCut bool
		tc.Input, inCut = Truncate(tc.Input, ix.opts.MaxFieldBytes)
		tc.Result, resCut = Truncate(tc.Result, ix.opts.MaxFieldBytes)
		it.Truncated = it.Truncated || inCut || resCut
		it.ToolCall = &tc
	}
	if it.Subagent != nil {
		sub := *it.Subagent
		it.Subagent = &sub
	}
	if it.Process != nil {
		proc := *it.Process
		it.Process = &proc
	}
	return it
}

func encodedSize(it Item) int {
	raw, err := json.Marshal(it)
	if err != nil {
		return 0
	}
	return len(raw)
}

// Page refreshes, then returns up to limit items whose first position is
// before the cursor (the newest page when before is nil). limit <= 0 means
// DefaultLimit; above MaxLimit it is MaxLimit. A cursor for another file id,
// another position kind, or one issued before a reset returns the newest page
// with Reset true. A reset found by this call's own refresh also sets Reset.
func (ix *Index) Page(before *Cursor, limit int) (Page, error) {
	reset, err := ix.Refresh()
	if err != nil {
		return ix.header(ix.current()), err
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := ix.s
	page := ix.header(s)
	page.Reset = reset
	end := len(s.entries)
	if before != nil {
		if ix.staleCursor(s, *before) {
			page.Reset = true
		} else {
			end = s.above(*before)
		}
	}
	budget := newPageBudget(limit, ix.opts.MaxPageBytes)
	page.Items, page.Before = s.newest(end, budget, page.After)
	return page, nil
}

// After refreshes, then returns what changed past c: new items in Items,
// updated older items in Changed, removed ids in Removed. A stale cursor
// returns the newest page with Reset true, as Page does. A reply cut by the
// limit or the byte cap has an After short of the file's end; the limit counts
// Items and Changed together.
func (ix *Index) After(c Cursor, limit int) (Page, error) {
	if _, err := ix.Refresh(); err != nil {
		return ix.header(ix.current()), err
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := ix.s
	page := ix.header(s)
	budget := newPageBudget(limit, ix.opts.MaxPageBytes)
	if ix.staleCursor(s, c) {
		page.Reset = true
		page.Items, page.Before = s.newest(len(s.entries), budget, page.After)
		return page, nil
	}
	if c.Position == s.end && c.sub == 0 {
		return page, nil // nothing past the end; the common idle poll
	}
	fillAfter(&page, s.eventsPast(c), budget)
	return page, nil
}

// fillAfter delivers events in order until the budget stops it, and points
// After at the last one delivered. Stopping anywhere else could turn an
// undelivered new item into a Changed item the reader ignores.
func fillAfter(page *Page, events []event, budget *pageBudget) {
	for i, ev := range events {
		if ev.kind != eventRemoved && !budget.take(ev.item) {
			page.After.Position, page.After.sub = events[i-1].key.pos, events[i-1].key.sub
			return
		}
		switch ev.kind {
		case eventNew:
			page.Items = append(page.Items, ev.item)
		case eventChanged:
			page.Changed = append(page.Changed, ev.item)
		default:
			page.Removed = append(page.Removed, ev.id)
		}
	}
}

func (ix *Index) current() *state {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.s
}

func (ix *Index) header(s *state) Page {
	return Page{
		Type:         PageType,
		Source:       SourceTranscript,
		Capabilities: ix.caps,
		Items:        []Item{},
		After:        Cursor{FileID: s.fileID, Kind: ix.posKind, Position: s.end},
	}
}

// staleCursor reports a cursor this file cannot answer. A position past the
// end means a file that shrank before this index was built.
func (ix *Index) staleCursor(s *state, c Cursor) bool {
	return c.FileID != s.fileID || c.Kind != ix.posKind || c.Position > s.end
}

// Item returns one indexed item by id.
func (ix *Index) Item(id string) (Item, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	e, ok := ix.s.byID[id]
	if !ok {
		return Item{}, false
	}
	return e.item, true
}

// FileID returns the id cursors from this index carry. It changes when the
// file is replaced or rewritten.
func (ix *Index) FileID() string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.s.fileID
}
