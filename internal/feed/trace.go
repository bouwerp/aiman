package feed

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/bouwerp/aiman/internal/domain"
)

// Trace limits (design, Trace).
const (
	maxTraceEntries = 32
	maxTraceBytes   = 64 << 10
	maxExcerpt      = 2 << 10
)

const (
	traceEvent    domain.FeedTraceKind = "event"
	traceContext  domain.FeedTraceKind = "context"
	traceMemory   domain.FeedTraceKind = "memory"
	traceDecision domain.FeedTraceKind = "decision"
	traceBatch    domain.FeedTraceKind = "batch"

	roleAnswers domain.FeedTraceRole = "answers"
	roleCause   domain.FeedTraceRole = "cause"
	roleContext domain.FeedTraceRole = "context"
)

// BuildTrace builds an action's trace: the events of the answered events'
// threads (cause, oldest first), the answered events (answers, body whole),
// the context they carried, the cited events, memory bullets and decisions,
// and the batch. Over 32 entries or 64 KiB of excerpts it drops context
// entries first, then the oldest memory and decision entries, then cited
// events; answers and cause entries always stay, with cause excerpts cut
// (oldest first) when nothing else is left to drop.
func BuildTrace(answers []domain.FeedEvent, threads map[string][]domain.FeedEvent, cited []domain.FeedEvent,
	memory []MemoryBullet, decisions []Decision, batch domain.FeedBatch) []domain.FeedTraceEntry {
	seen := map[int64]bool{}
	for _, a := range answers {
		seen[a.ID] = true
	}
	var causes []domain.FeedEvent
	keys := map[string]bool{}
	for _, a := range answers {
		if keys[a.ThreadKey] {
			continue
		}
		keys[a.ThreadKey] = true
		for _, e := range threads[a.ThreadKey] {
			if !seen[e.ID] {
				seen[e.ID] = true
				causes = append(causes, e)
			}
		}
	}
	sort.SliceStable(causes, func(i, j int) bool { return causes[i].ID < causes[j].ID })

	var out []domain.FeedTraceEntry
	for _, e := range causes {
		out = append(out, eventEntry(e, roleCause, maxExcerpt))
	}
	for _, a := range answers {
		out = append(out, eventEntry(a, roleAnswers, -1))
	}
	for _, a := range answers {
		if a.Enrichment != "" {
			out = append(out, domain.FeedTraceEntry{Kind: traceContext, Ref: strconv.FormatInt(a.ID, 10), At: a.At,
				Role: roleContext, Excerpt: cutBytes(a.Enrichment, maxExcerpt), URL: a.URL})
		}
	}
	for _, e := range cited {
		if !seen[e.ID] {
			seen[e.ID] = true
			out = append(out, eventEntry(e, roleContext, maxExcerpt))
		}
	}
	for _, m := range memory {
		out = append(out, domain.FeedTraceEntry{Kind: traceMemory, Ref: m.ID, At: m.At, Role: roleContext, Excerpt: cutBytes(m.Text, maxExcerpt)})
	}
	for _, d := range decisions {
		out = append(out, domain.FeedTraceEntry{Kind: traceDecision, Ref: strconv.FormatInt(d.ActionID, 10), At: d.At,
			Role: roleContext, Excerpt: cutBytes(decisionLine(d), maxExcerpt)})
	}
	out = append(out, domain.FeedTraceEntry{Kind: traceBatch, Ref: strconv.FormatInt(batch.ID, 10), At: batch.StartedAt,
		Role: roleContext, Excerpt: fmt.Sprintf("batch %d, agent %s, model %s, attempt %d", batch.ID, batch.Agent, batch.Model, batch.Attempts)})
	return fitTrace(out)
}

func eventEntry(e domain.FeedEvent, role domain.FeedTraceRole, max int) domain.FeedTraceEntry {
	ex := e.Body
	if max >= 0 {
		ex = cutBytes(ex, max)
	}
	return domain.FeedTraceEntry{Kind: traceEvent, Ref: strconv.FormatInt(e.ID, 10), At: e.At, Role: role, Excerpt: ex, URL: e.URL}
}

func decisionLine(d Decision) string {
	s := fmt.Sprintf("%s %s", d.Kind, d.Outcome)
	if d.Outcome == "approved" {
		s += fmt.Sprintf(" option %d", d.Option)
		if d.Edited {
			s += " (edited)"
		}
	}
	if d.Reason != "" {
		s += " reason " + string(d.Reason)
	}
	if d.Note != "" {
		s += " note: " + d.Note
	}
	return s
}

func traceSize(t []domain.FeedTraceEntry) int {
	n := 0
	for _, e := range t {
		n += len(e.Excerpt)
	}
	return n
}

func overLimits(t []domain.FeedTraceEntry) bool {
	return len(t) > maxTraceEntries || traceSize(t) > maxTraceBytes
}

func fitTrace(t []domain.FeedTraceEntry) []domain.FeedTraceEntry {
	drop := func(pick func(e domain.FeedTraceEntry) bool, oldestFirst bool) {
		for overLimits(t) {
			idx := -1
			for i, e := range t {
				if !pick(e) {
					continue
				}
				if idx < 0 || (oldestFirst && e.At.Before(t[idx].At)) {
					idx = i
				}
			}
			if idx < 0 {
				return
			}
			t = append(t[:idx], t[idx+1:]...)
		}
	}
	drop(func(e domain.FeedTraceEntry) bool { return e.Kind == traceContext }, false)
	drop(func(e domain.FeedTraceEntry) bool { return e.Kind == traceMemory || e.Kind == traceDecision }, true)
	drop(func(e domain.FeedTraceEntry) bool { return e.Kind == traceEvent && e.Role == roleContext }, false)
	// Only answers, causes and the batch are left: ids stay, so cut cause
	// excerpts, oldest first, until the bytes fit.
	for i := range t {
		over := traceSize(t) - maxTraceBytes
		if over <= 0 {
			break
		}
		if t[i].Role != roleCause {
			continue
		}
		t[i].Excerpt = cutBytes(t[i].Excerpt, max(0, len(t[i].Excerpt)-over))
	}
	return t
}
