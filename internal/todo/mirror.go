package todo

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bouwerp/aiman/internal/domain"
)

// matchThreshold is the minimum token Jaccard similarity for a fuzzy match.
const matchThreshold = 0.8

// Normalize lowercases text, strips punctuation and collapses whitespace.
func Normalize(text string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
	return strings.Join(strings.Fields(stripped), " ")
}

// Similarity is the Jaccard index of the token sets of the normalised texts.
func Similarity(a, b string) float64 {
	return jaccard(tokens(Normalize(a)), tokens(Normalize(b)))
}

func tokens(norm string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(norm) {
		set[w] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

type snapEntry struct {
	text  string
	norm  string
	state domain.TodoState
}

// Retext records the text a non-agent author replaced, so a snapshot that
// still carries the old text is recognised as the same item.
type Retext struct {
	ID string `json:"id"`
	// OldText is the replaced text, normalised.
	OldText string    `json:"old_text"`
	At      time.Time `json:"at"`
}

// Merge folds an agent's task-list snapshot into list. Items the user, aiman
// or a verifier changed after deliveredAt are "undelivered": the agent cannot
// have seen that change yet, so the snapshot must not override it. The
// result's NotInAgent is recomputed; added items have an empty ID.
func Merge(list domain.TodoList, tombs []Tombstone, snap []domain.TodoSnapshotItem, deliveredAt, now time.Time) domain.TodoList {
	return MergeWithRetexts(list, tombs, nil, snap, deliveredAt, now)
}

// MergeWithRetexts is Merge that also matches an unmatched snapshot item to
// an item whose undelivered retext replaced the snapshot item's text.
func MergeWithRetexts(list domain.TodoList, tombs []Tombstone, retexts []Retext, snap []domain.TodoSnapshotItem, deliveredAt, now time.Time) domain.TodoList {
	undelivered := func(it domain.TodoItem) bool {
		return it.UpdatedBy != domain.AuthorAgent && it.UpdatedAt.After(deliveredAt)
	}

	// Snapshot items with no words cannot be matched or stored; they are ignored.
	var entries []snapEntry
	for _, s := range snap {
		norm := Normalize(s.Text)
		if norm == "" {
			continue
		}
		entries = append(entries, snapEntry{
			text:  strings.TrimSpace(s.Text),
			norm:  norm,
			state: domain.NormalizeVendorTodoStatus(string(s.Status)),
		})
	}

	match := matchSnapshot(list.Items, entries) // snapshot index -> existing index, or -1
	matched := make([]bool, len(list.Items))
	for _, ei := range match {
		if ei >= 0 {
			matched[ei] = true
		}
	}
	matchRetexts(list.Items, entries, retexts, deliveredAt, match, matched)

	items := append([]domain.TodoItem(nil), list.Items...)
	// An undelivered active item stays the one active item: the snapshot's
	// active marker cannot outrank a choice the agent has not seen.
	anyUndelivered, userActive := false, false
	for _, it := range items {
		if undelivered(it) {
			anyUndelivered = true
			userActive = userActive || it.State == domain.TodoActive
		}
	}
	snapState := func(st domain.TodoState) domain.TodoState {
		if userActive && st == domain.TodoActive {
			return domain.TodoPending
		}
		return st
	}

	var notInAgent []string
	for i := range items {
		it := &items[i]
		switch {
		case matched[i]:
		case it.UpdatedBy == domain.AuthorAgent:
			it.State = domain.TodoDropped
		case !undelivered(*it):
			notInAgent = append(notInAgent, it.ID)
		}
	}

	removed := map[string]bool{}
	for _, tb := range tombs {
		if tb.RemovedAt.After(deliveredAt) {
			removed[Normalize(tb.Text)] = true
		}
	}

	// ordered holds the matched and new items in snapshot order, as indices
	// into items (existing) or into added (offset by len(items)).
	var added []domain.TodoItem
	var ordered []int
	for si, e := range entries {
		ei := match[si]
		if ei < 0 {
			if removed[e.norm] {
				continue
			}
			added = append(added, domain.TodoItem{Text: e.text, State: snapState(e.state), UpdatedBy: domain.AuthorAgent, UpdatedAt: now})
			ordered = append(ordered, len(items)+len(added)-1)
			continue
		}
		it := &items[ei]
		if it.UpdatedBy == domain.AuthorAgent {
			it.Text, it.State = e.text, snapState(e.state)
		} else if !undelivered(*it) {
			it.State = snapState(e.state)
		}
		ordered = append(ordered, ei)
	}

	out := list
	out.NotInAgent = notInAgent
	if anyUndelivered {
		items = append(items, added...)
		out.Items = items
		return out
	}
	out.Items = snapshotOrder(items, added, matched, ordered)
	return out
}

// matchSnapshot pairs snapshot entries with existing items: exact normalised
// text first, then the highest similarity at or above matchThreshold. Each
// existing item matches at most once.
func matchSnapshot(existing []domain.TodoItem, entries []snapEntry) []int {
	match := make([]int, len(entries))
	for i := range match {
		match[i] = -1
	}
	norms := make([]string, len(existing))
	for i, it := range existing {
		norms[i] = Normalize(it.Text)
	}
	taken := make([]bool, len(existing))
	for si, e := range entries {
		for ei, n := range norms {
			if !taken[ei] && n == e.norm {
				match[si], taken[ei] = ei, true
				break
			}
		}
	}

	type pair struct {
		si, ei int
		score  float64
	}
	var pairs []pair
	for si, e := range entries {
		if match[si] >= 0 {
			continue
		}
		st := tokens(e.norm)
		for ei, n := range norms {
			if taken[ei] {
				continue
			}
			if score := jaccard(st, tokens(n)); score >= matchThreshold {
				pairs = append(pairs, pair{si, ei, score})
			}
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].score > pairs[b].score })
	for _, p := range pairs {
		if match[p.si] < 0 && !taken[p.ei] {
			match[p.si], taken[p.ei] = p.ei, true
		}
	}
	return match
}

// matchRetexts matches each still-unmatched snapshot entry whose text equals
// the old text of a retext made after deliveredAt to the retexted item.
func matchRetexts(existing []domain.TodoItem, entries []snapEntry, retexts []Retext, deliveredAt time.Time, match []int, matched []bool) {
	index := make(map[string]int, len(existing))
	for i, it := range existing {
		index[it.ID] = i
	}
	for si, e := range entries {
		if match[si] >= 0 {
			continue
		}
		for _, r := range retexts {
			ei, ok := index[r.ID]
			if ok && !matched[ei] && r.At.After(deliveredAt) && r.OldText == e.norm {
				match[si], matched[ei] = ei, true
				break
			}
		}
	}
}

// snapshotOrder lays out matched and new items in snapshot order and places
// each unmatched existing item right after the item that matched its nearest
// matched predecessor in the old list (or first, when it had none).
func snapshotOrder(items, added []domain.TodoItem, matched []bool, ordered []int) []domain.TodoItem {
	after := map[int][]int{} // existing index of anchor (-1 = start) -> unmatched existing indices
	anchor := -1
	for i := range items {
		if matched[i] {
			anchor = i
			continue
		}
		after[anchor] = append(after[anchor], i)
	}
	at := func(k int) domain.TodoItem {
		if k < len(items) {
			return items[k]
		}
		return added[k-len(items)]
	}
	out := make([]domain.TodoItem, 0, len(items)+len(added))
	for _, k := range after[-1] {
		out = append(out, items[k])
	}
	for _, k := range ordered {
		out = append(out, at(k))
		if k < len(items) {
			for _, u := range after[k] {
				out = append(out, items[u])
			}
		}
	}
	return out
}
