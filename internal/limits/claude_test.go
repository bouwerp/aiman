package limits

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const statusInput = `{"session_id":"SECRETSESSION","cwd":"/home/u/secret-project","transcript_path":"/home/u/t.jsonl","cost":{"total_cost_usd":3},"rate_limits":{"five_hour":{"used_percentage":41,"resets_at":1794000000},"seven_day":{"used_percentage":12,"resets_at":1794300000}}}`

func claudeFile(home string) string {
	return filepath.Join(home, ".aiman/limits/claude-statusline.json")
}

func TestClaudeRecordStoresOnlyRateLimits(t *testing.T) {
	home := t.TempDir()
	if err := RecordClaude([]byte(statusInput), home, testNow); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(claudeFile(home))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRETSESSION", "secret-project", "t.jsonl", "total_cost"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("stored %q: %s", leak, raw)
		}
	}
	st, err := os.Stat(claudeFile(home))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", st.Mode(), err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["rate_limits"]; !ok {
		t.Fatalf("no rate_limits: %s", raw)
	}
}

func TestClaudeRecordKeepsThePreviousFile(t *testing.T) {
	home := t.TempDir()
	if err := RecordClaude([]byte(statusInput), home, testNow); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(claudeFile(home))
	for _, in := range []string{`{"session_id":"x"}`, `{not json`, ``, `{"rate_limits":{}}`} {
		_ = RecordClaude([]byte(in), home, testNow.Add(time.Hour))
		after, _ := os.ReadFile(claudeFile(home))
		if !bytes.Equal(after, before) {
			t.Fatalf("input %q replaced the file with %s", in, after)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(claudeFile(home)))
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}

func TestClaudeSourceReadsWindows(t *testing.T) {
	env := testEnv(t)
	if err := RecordClaude([]byte(statusInput), env.Home, testNow.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r := NewClaudeSource(env, true).Read(context.Background())
	if r.State != StateOK || len(r.Windows) != 2 {
		t.Fatalf("reading %+v", r)
	}
	if r.Windows[0].Label != "5h" || r.Windows[0].UsedPercent != 41 || r.Windows[1].Label != "week" ||
		!r.Windows[1].ResetsAt.Equal(time.Unix(1794300000, 0)) {
		t.Fatalf("windows %+v", r.Windows)
	}
	if !r.ObservedAt.Equal(testNow.Add(-4 * time.Minute)) {
		t.Fatalf("observed %v", r.ObservedAt)
	}
}

func TestClaudeSourceStates(t *testing.T) {
	env := testEnv(t)
	r := NewClaudeSource(env, false).Read(context.Background())
	if r.State != StateUnavailable || !strings.Contains(r.Reason, "limits.claude_statusline") {
		t.Fatalf("off: %+v", r)
	}
	if r := NewClaudeSource(env, true).Read(context.Background()); r.State != StateUnknown {
		t.Fatalf("on with nothing recorded: %+v", r)
	}
}
