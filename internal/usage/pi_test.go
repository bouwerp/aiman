package usage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Lines in the shape Pi 1.4 writes to ~/.pi/agent/sessions/--<cwd>--/<ts>_<id>.jsonl.
const (
	piHeader    = `{"type":"session","version":3,"id":"01a18182-808d-7087-a9b4-a7a8665626b3","timestamp":"2026-10-28T07:56:15.885Z","cwd":"/w"}`
	piModel     = `{"type":"model_change","id":"5d2be66b","parentId":null,"timestamp":"2026-10-28T07:56:16.113Z","provider":"openrouter","modelId":"openai/gpt-oss-20b"}`
	piUser      = `{"type":"message","id":"d598e83d","parentId":"48232bfc","timestamp":"2026-10-28T07:56:16.200Z","message":{"role":"user","content":"hi"}}`
	piAssistant = `{"type":"message","id":"37197771","parentId":"20cf10b1","timestamp":"2026-10-28T07:56:22.090Z","message":{"role":"assistant","content":[],"api":"openai-completions","provider":"openrouter","model":"openai/gpt-oss-20b","usage":{"input":7366,"output":27,"cacheRead":100,"cacheWrite":50,"cacheWrite1h":20,"reasoning":16,"totalTokens":7543,"cost":{"input":0.0001,"output":0,"cacheRead":0,"cacheWrite":0,"total":0.0001}},"stopReason":"stop"}}`
	piZero      = `{"type":"message","id":"aaaa0000","parentId":"37197771","timestamp":"2026-10-28T07:56:23.000Z","message":{"role":"assistant","content":[],"provider":"openrouter","model":"openai/gpt-oss-20b","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0},"stopReason":"aborted"}}`
)

func TestKindForPathPi(t *testing.T) {
	if got := KindForPath("/home/u/.pi/agent/sessions/--w--/2026-10-28T07-56-15-885Z_01a1.jsonl"); got != KindPi {
		t.Fatalf("got %q", got)
	}
	if got := KindForPath("/home/u/.pi/agent/models.jsonl"); got != KindUnknown {
		t.Fatalf("non-session file got %q", got)
	}
}

func TestParsePiAssistantUsage(t *testing.T) {
	in := strings.Join([]string{piHeader, piModel, piUser, piAssistant, piZero, piAssistant}, "\n") + "\n"
	recs, consumed, err := ParsePi(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(in)) {
		t.Fatalf("consumed %d of %d", consumed, len(in))
	}
	// The repeated entry id is counted once; a zero-usage turn is not a record.
	if len(recs) != 1 {
		t.Fatalf("recs %+v", recs)
	}
	r := recs[0]
	want := domain.TokenCounts{Input: 7366, Output: 27, CacheRead: 100, CacheWrite5m: 30, CacheWrite1h: 20}
	if r.Tokens != want || r.Model != "openai/gpt-oss-20b" || r.ID != "37197771" {
		t.Fatalf("rec %+v", r)
	}
	if r.At.IsZero() {
		t.Fatal("no timestamp")
	}
}

func TestTrackerScansPiTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".pi", "agent", "sessions", "--w--", "2026-10-28T07-56-15-885Z_01a18182.jsonl")
	writeLines(t, path, []byte(piHeader), []byte(piModel), []byte(piUser), []byte(piAssistant))
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "pi", AgentSessionPath: path})
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed %v", changed)
	}
	b := env.store.buckets(t)
	if len(b) != 1 || b[0].Tokens.Input != 7366 || b[0].Model != "openai/gpt-oss-20b" {
		t.Fatalf("buckets %+v", b)
	}
}
