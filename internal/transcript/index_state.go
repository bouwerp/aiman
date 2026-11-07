package transcript

import "sort"

// posKey orders every change an index applies. sub counts the changes applied
// at one position, from 1, because one line can produce several.
type posKey struct {
	pos int64
	sub int
}

// pastCursor reports whether k is past c: not yet consumed by a reader at c.
func (k posKey) pastCursor(c Cursor) bool {
	return k.pos > c.Position || (c.sub > 0 && k.pos == c.Position && k.sub > c.sub)
}

// beforeCursor reports whether k is above a before cursor.
func (k posKey) beforeCursor(c Cursor) bool {
	return k.pos < c.Position || (c.sub > 0 && k.pos == c.Position && k.sub < c.sub)
}

type entry struct {
	item  Item
	first posKey
	rev   posKey
	gone  bool
}

// state is what a reader sees. Between Refresh calls it only moves whole reads
// at a time, so a reader's end position never falls short of a change it sees.
type state struct {
	fileID  string
	end     int64
	entries []*entry // ordered by first
	byID    map[string]*entry
	removed map[string]posKey
	subPos  int64
	subN    int
	dirty   bool // entries holds gone entries
}

func newState(start int64) *state {
	return &state{end: start, byID: map[string]*entry{}, removed: map[string]posKey{}, subPos: start}
}

func (s *state) nextKey(pos int64) posKey {
	if pos != s.subPos {
		s.subPos, s.subN = pos, 0
	}
	s.subN++
	return posKey{pos, s.subN}
}

// apply folds one line's deltas in, each stamped with its own key. Items are
// already capped and stamped (Index.prepare).
func (s *state) apply(pos int64, deltas []Delta) {
	for _, d := range deltas {
		key := s.nextKey(pos)
		switch {
		case d.Add != nil:
			s.add(key, *d.Add)
		case d.Update != nil:
			if e, ok := s.byID[d.Update.ID]; ok {
				e.item, e.rev = *d.Update, key
			}
		case d.Remove != "":
			s.remove(key, d.Remove)
		}
	}
}

func (s *state) add(key posKey, it Item) {
	if e, ok := s.byID[it.ID]; ok {
		e.item, e.rev = it, key
		return
	}
	e := &entry{item: it, first: key, rev: key}
	s.entries = append(s.entries, e)
	s.byID[it.ID] = e
}

func (s *state) remove(key posKey, id string) {
	e, ok := s.byID[id]
	if !ok {
		return
	}
	e.gone = true
	s.dirty = true
	delete(s.byID, id)
	s.removed[id] = key
}

// compact drops gone entries, once per read rather than once per removal.
func (s *state) compact() {
	if !s.dirty {
		return
	}
	s.dirty = false
	live := s.entries[:0]
	for _, e := range s.entries {
		if !e.gone {
			live = append(live, e)
		}
	}
	clear(s.entries[len(live):])
	s.entries = live
}

// pageBudget stops a reply at its item limit and byte cap. The first item is
// always taken, so a reply makes progress.
type pageBudget struct {
	limit, maxBytes int
	count, bytes    int
}

func newPageBudget(limit, maxBytes int) *pageBudget {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	// Two JSON arrays' brackets, for Items and Changed.
	return &pageBudget{limit: limit, maxBytes: maxBytes, bytes: 4}
}

func (b *pageBudget) take(it Item) bool {
	if b.count >= b.limit {
		return false
	}
	size := encodedSize(it) + 1 // the comma
	if b.count > 0 && b.bytes+size > b.maxBytes {
		return false
	}
	b.count++
	b.bytes += size
	return true
}

// newest returns the items ending just above end, newest-last, and the cursor
// for the page above them.
func (s *state) newest(end int, b *pageBudget, cur Cursor) ([]Item, *Cursor) {
	lo := end
	for lo > 0 && b.take(s.entries[lo-1].item) {
		lo--
	}
	items := make([]Item, 0, end-lo)
	for _, e := range s.entries[lo:end] {
		items = append(items, e.item)
	}
	if lo == 0 {
		return items, nil
	}
	cur.Position, cur.sub = s.entries[lo].first.pos, s.entries[lo].first.sub
	return items, &cur
}

// above returns how many entries are above a before cursor.
func (s *state) above(c Cursor) int {
	return sort.Search(len(s.entries), func(i int) bool {
		return !s.entries[i].first.beforeCursor(c)
	})
}

const (
	eventNew = iota
	eventChanged
	eventRemoved
)

type event struct {
	key  posKey
	kind int
	item Item
	id   string
}

// eventsPast lists everything a reader at c has not seen, in the order it
// happened. An item first seen past c counts once, as new, at its first key:
// a reader stopped after it picks up any later change as a Changed item.
func (s *state) eventsPast(c Cursor) []event {
	var out []event
	for _, e := range s.entries {
		switch {
		case e.first.pastCursor(c):
			out = append(out, event{key: e.first, kind: eventNew, item: e.item})
		case e.rev.pastCursor(c):
			out = append(out, event{key: e.rev, kind: eventChanged, item: e.item})
		}
	}
	for id, key := range s.removed {
		if key.pastCursor(c) {
			out = append(out, event{key: key, kind: eventRemoved, id: id})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].key, out[j].key
		return a.pos < b.pos || (a.pos == b.pos && a.sub < b.sub)
	})
	return out
}
