package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestBudgetThresholdsEmitOnce(t *testing.T) {
	cases := []struct {
		used int64
		want string
	}{
		{69, ""}, {71, "warn"}, {75, ""}, {101, "over"},
	}
	last := ""
	for _, tc := range cases {
		emit, next := NextBudgetEmit(last, 100, tc.used)
		if emit != tc.want {
			t.Fatalf("used %d emit = %q, want %q (last %q)", tc.used, emit, tc.want, last)
		}
		last = next
	}
	// Already over: no repeats.
	if emit, _ := NextBudgetEmit("over", 100, 150); emit != "" {
		t.Fatalf("over repeat emit = %q", emit)
	}
	if got := BudgetFor(100, 71); got.State != "warn" || got.Pct < 0.70 || got.Pct > 0.72 {
		t.Fatalf("BudgetFor = %+v", got)
	}
}

func TestBudgetZeroIsNone(t *testing.T) {
	if got := BudgetFor(0, 10_000); got.State != "" || got.Tokens != 0 {
		t.Fatalf("zero budget = %+v, want none", got)
	}
	if emit, _ := NextBudgetEmit("", 0, 10_000); emit != "" {
		t.Fatalf("zero budget emit = %q", emit)
	}
}

func TestBudgetIsPerSession(t *testing.T) {
	dir := claudeProjectDir(t)
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1 := write("00000000-0000-4000-8000-000000000001.jsonl", assistantLine("a", "claude-opus-5-5", `"input_tokens":80,"output_tokens":0`))
	p2 := write("00000000-0000-4000-8000-000000000002.jsonl", assistantLine("b", "claude-opus-5-5", `"input_tokens":10,"output_tokens":0`))
	env := &scanEnv{store: newMemStore(), sessions: &memSessions{list: []domain.Session{
		{ID: "s1", AgentName: "claude", AgentSessionPath: p1, BudgetTokens: 100},
		{ID: "s2", AgentName: "claude", AgentSessionPath: p2, BudgetTokens: 100},
	}}}
	var emits []string
	env.tracker = NewTracker(env.store, env.sessions, nil)
	env.tracker.SetBudgetHandler(func(id, state string) { emits = append(emits, id+":"+state) })
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(emits) != 1 || emits[0] != "s1:warn" {
		t.Fatalf("emits = %v, want only s1 warn", emits)
	}
	// Rescan with no new tokens: no repeat.
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(emits) != 1 {
		t.Fatalf("emits after rescan = %v, want no repeat", emits)
	}
}
