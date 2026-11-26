package feed_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
)

const (
	traceAnswers = domain.FeedTraceRole("answers")
	traceCause   = domain.FeedTraceRole("cause")
)

func traceBatch() domain.FeedBatch {
	return domain.FeedBatch{ID: 9, Agent: "claude", Model: "opus", StartedAt: feedT0}
}

func entriesOf(trace []domain.FeedTraceEntry, kind domain.FeedTraceKind) []domain.FeedTraceEntry {
	var out []domain.FeedTraceEntry
	for _, e := range trace {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func entryRef(trace []domain.FeedTraceEntry, kind domain.FeedTraceKind, ref string) (domain.FeedTraceEntry, bool) {
	for _, e := range trace {
		if e.Kind == kind && e.Ref == ref {
			return e, true
		}
	}
	return domain.FeedTraceEntry{}, false
}

func TestBuildTraceEntries(t *testing.T) {
	answered := ev(50, func(e *domain.FeedEvent) {
		e.ThreadKey, e.Body, e.Enrichment = "slack:C1/40", strings.Repeat("a", 3000), "thread around 50"
	})
	threads := map[string][]domain.FeedEvent{"slack:C1/40": {
		ev(40, func(e *domain.FeedEvent) { e.ThreadKey = "slack:C1/40" }),
		ev(45, func(e *domain.FeedEvent) { e.ThreadKey, e.Body = "slack:C1/40", strings.Repeat("b", 3000) }),
	}}
	cited := []domain.FeedEvent{ev(30, nil)}
	memory := []feed.MemoryBullet{{ID: "m12", Text: "prefers drafts", At: feedT0}}
	decisions := []feed.Decision{{ActionID: 7, Kind: domain.FeedRead, Outcome: "approved", At: feedT0}}

	trace := feed.BuildTrace([]domain.FeedEvent{answered}, threads, cited, memory, decisions, traceBatch())

	a, ok := entryRef(trace, domain.FeedTraceKind("event"), "50")
	if !ok || a.Role != traceAnswers {
		t.Fatalf("answered event entry = %+v, %v", a, ok)
	}
	if a.Excerpt != answered.Body {
		t.Errorf("answered body cut to %d bytes, want it whole", len(a.Excerpt))
	}
	var causes []string
	for _, e := range entriesOf(trace, domain.FeedTraceKind("event")) {
		if e.Role == traceCause {
			causes = append(causes, e.Ref)
		}
		if e.Ref != "50" && len(e.Excerpt) > 2048 {
			t.Errorf("event %s excerpt %d bytes, want at most 2 KiB", e.Ref, len(e.Excerpt))
		}
	}
	if strings.Join(causes, ",") != "40,45" {
		t.Errorf("cause events = %v, want 40,45 oldest first", causes)
	}
	if _, ok := entryRef(trace, domain.FeedTraceKind("event"), "30"); !ok {
		t.Error("cited event 30 missing")
	}
	if len(entriesOf(trace, domain.FeedTraceKind("context"))) == 0 {
		t.Error("no context entry for the answered event's enrichment")
	}
	if m, ok := entryRef(trace, domain.FeedTraceKind("memory"), "m12"); !ok || m.Excerpt != "prefers drafts" {
		t.Errorf("memory entry = %+v, %v", m, ok)
	}
	if _, ok := entryRef(trace, domain.FeedTraceKind("decision"), "7"); !ok {
		t.Error("decision 7 missing")
	}
	if _, ok := entryRef(trace, domain.FeedTraceKind("batch"), "9"); !ok {
		t.Error("batch 9 missing")
	}
	n := 0
	for _, e := range trace {
		if e.Kind == domain.FeedTraceKind("event") && e.Ref == "50" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("answered event listed %d times, want once", n)
	}
}

func TestBuildTraceLimits(t *testing.T) {
	answered := ev(100, func(e *domain.FeedEvent) {
		e.ThreadKey, e.Body, e.Enrichment = "slack:C1/60", strings.Repeat("a", 4000), strings.Repeat("c", 8000)
	})
	var thread []domain.FeedEvent
	for i := int64(60); i < 70; i++ {
		thread = append(thread, ev(i, func(e *domain.FeedEvent) { e.ThreadKey, e.Body = "slack:C1/60", strings.Repeat("t", 3000) }))
	}
	var memory []feed.MemoryBullet
	for i := range 30 {
		memory = append(memory, feed.MemoryBullet{ID: fmt.Sprintf("m%d", i), Text: strings.Repeat("m", 300), At: feedT0.Add(time.Duration(i) * time.Minute)})
	}
	var decisions []feed.Decision
	for i := range 10 {
		decisions = append(decisions, feed.Decision{ActionID: int64(i + 1), Kind: domain.FeedRead, Outcome: "dismissed", Note: strings.Repeat("d", 2000), At: feedT0.Add(time.Duration(i) * time.Minute)})
	}

	trace := feed.BuildTrace([]domain.FeedEvent{answered}, map[string][]domain.FeedEvent{"slack:C1/60": thread}, nil, memory, decisions, traceBatch())

	if len(trace) > 32 {
		t.Errorf("%d entries, want at most 32", len(trace))
	}
	total := 0
	for _, e := range trace {
		total += len(e.Excerpt)
	}
	if total > 64<<10 {
		t.Errorf("excerpts total %d bytes, want at most 64 KiB", total)
	}
	if len(entriesOf(trace, domain.FeedTraceKind("context"))) != 0 {
		t.Error("context entries kept while over the limits; they go first")
	}
	if a, ok := entryRef(trace, domain.FeedTraceKind("event"), "100"); !ok || len(a.Excerpt) != 4000 {
		t.Errorf("answered event = %v, %d bytes; it stays whole", ok, len(a.Excerpt))
	}
	for i := int64(60); i < 70; i++ {
		if _, ok := entryRef(trace, domain.FeedTraceKind("event"), strconv.FormatInt(i, 10)); !ok {
			t.Errorf("cause event %d dropped; cause events stay", i)
		}
	}
	keptMin, droppedMax := map[domain.FeedTraceKind]time.Time{}, map[domain.FeedTraceKind]time.Time{}
	kept := map[string]bool{}
	for _, e := range trace {
		kept[string(e.Kind)+"/"+e.Ref] = true
	}
	check := func(kind domain.FeedTraceKind, ref string, at time.Time) {
		if kept[string(kind)+"/"+ref] {
			if k, ok := keptMin[kind]; !ok || at.Before(k) {
				keptMin[kind] = at
			}
		} else if at.After(droppedMax[kind]) {
			droppedMax[kind] = at
		}
	}
	for _, m := range memory {
		check(domain.FeedTraceKind("memory"), m.ID, m.At)
	}
	for _, d := range decisions {
		check(domain.FeedTraceKind("decision"), strconv.FormatInt(d.ActionID, 10), d.At)
	}
	for _, kind := range []domain.FeedTraceKind{"memory", "decision"} {
		if k, ok := keptMin[kind]; ok && !droppedMax[kind].IsZero() && droppedMax[kind].After(k) {
			t.Errorf("%s: a newer entry was dropped before an older one", kind)
		}
	}
}

func TestBuildTraceKeepsEveryCauseID(t *testing.T) {
	answered := ev(200, func(e *domain.FeedEvent) { e.ThreadKey = "slack:C1/100" })
	var thread []domain.FeedEvent
	for i := int64(100); i < 140; i++ {
		thread = append(thread, ev(i, func(e *domain.FeedEvent) { e.ThreadKey, e.Body = "slack:C1/100", strings.Repeat("t", 2048) }))
	}
	trace := feed.BuildTrace([]domain.FeedEvent{answered}, map[string][]domain.FeedEvent{"slack:C1/100": thread}, nil, nil, nil, traceBatch())
	for i := int64(100); i < 140; i++ {
		if _, ok := entryRef(trace, domain.FeedTraceKind("event"), strconv.FormatInt(i, 10)); !ok {
			t.Errorf("cause event %d missing; ids always stay", i)
		}
	}
	if _, ok := entryRef(trace, domain.FeedTraceKind("event"), "200"); !ok {
		t.Error("answered event missing")
	}
}
