//go:build feedaccept

package feed_test

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
)

func replyWithOptions(n int) string {
	opts := make([]string, n)
	for i := range opts {
		opts[i] = `{"label":"o","params":{"url":"https://x","why":"w"}}`
	}
	return `{"actions":[{"kind":"read","priority":"low","rationale":"r","answers":[1],"options":[` + strings.Join(opts, ",") + `]}],"summary":"s"}`
}

func TestParseReplyTakesTheLastObject(t *testing.T) {
	stdout := "Thinking about it.\n" +
		`{"summary":"draft one"}` + "\nOn reflection:\n```json\n" +
		`{
  "actions": [{
    "kind": "slack_draft", "priority": "high", "rationale": "asked a question",
    "answers": [811], "cites": {"events": [790], "memory": ["m12"], "decisions": [40]},
    "options": [
      {"label": "Not yet", "params": {"channel": "C1", "text": "not yet"}, "why": "branch open"},
      {"label": "Later", "kind": "todo_add", "params": {"session": "s1", "text": "answer"}}
    ]
  }],
  "digest": [{"event": 812, "line": "deploy finished"}],
  "memory_ops": [{"op": "add", "text": "prefers short answers"}, {"op": "remove", "id": "m3"}],
  "summary": "one question"
}` + "\n```\nDone.\n"
	r, err := feed.ParseReply(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary != "one question" {
		t.Errorf("Summary = %q, want the last object's", r.Summary)
	}
	if len(r.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(r.Actions))
	}
	a := r.Actions[0]
	if a.Kind != domain.FeedSlackDraft || a.Priority != domain.FeedPriority("high") || a.Rationale != "asked a question" {
		t.Errorf("action = %+v", a)
	}
	if len(a.Answers) != 1 || a.Answers[0] != 811 {
		t.Errorf("Answers = %v", a.Answers)
	}
	if len(a.Cites.Events) != 1 || a.Cites.Events[0] != 790 || a.Cites.Memory[0] != "m12" || a.Cites.Decisions[0] != 40 {
		t.Errorf("Cites = %+v", a.Cites)
	}
	if len(a.Options) != 2 || a.Options[1].EffectiveKind(a.Kind) != domain.FeedTodoAdd || a.Options[0].EffectiveKind(a.Kind) != domain.FeedSlackDraft {
		t.Errorf("Options = %+v", a.Options)
	}
	if len(r.Digest) != 1 || r.Digest[0].Event != 812 || r.Digest[0].Line != "deploy finished" {
		t.Errorf("Digest = %+v", r.Digest)
	}
	if len(r.MemoryOps) != 2 || r.MemoryOps[0].Op != "add" || r.MemoryOps[0].Text == "" || r.MemoryOps[1].ID != "m3" {
		t.Errorf("MemoryOps = %+v", r.MemoryOps)
	}
}

func TestParseReplyRefuses(t *testing.T) {
	cases := map[string]string{
		"no json":     "I could not decide.",
		"broken json": `{"actions": [`,
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := feed.ParseReply(stdout); err == nil {
				t.Error("ParseReply succeeded, want an error")
			}
		})
	}
	// A bad action is dropped and the rest of the reply stands (design,
	// Behaviour 5): 0 or more than 4 options drop only that action.
	for n, want := range map[int]int{0: 0, 1: 1, 4: 1, 5: 0} {
		r, err := feed.ParseReply(replyWithOptions(n))
		if err != nil {
			t.Errorf("%d options: %v", n, err)
			continue
		}
		if len(r.Actions) != want {
			t.Errorf("%d options: %d actions, want %d", n, len(r.Actions), want)
		}
	}
}
