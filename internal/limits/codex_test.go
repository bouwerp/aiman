package limits

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const codexLimitsLine = `{"timestamp":"2026-11-05T18:51:00.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","plan_type":"plus","primary":{"used_percent":2.0,"window_minutes":300,"resets_at":1793913986},"secondary":{"used_percent":30.0,"window_minutes":10080,"resets_at":1794296504}}}}`

func TestCodexReadsTheLastLimitsOfTheNewestTranscript(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Home, ".codex/sessions/2026/11/01/rollout-old.jsonl"),
		strings.ReplaceAll(codexLimitsLine, "30.0", "99.0")+"\n")
	padding := strings.Repeat(`{"type":"response_item","payload":{"text":"`+strings.Repeat("x", 500)+`"}}`+"\n", 1000)
	writeFile(t, filepath.Join(env.Home, ".codex/sessions/2026/11/05/rollout-new.jsonl"),
		strings.ReplaceAll(codexLimitsLine, "30.0", "11.0")+"\n"+codexLimitsLine+"\n"+padding[:1000]+"\n")
	r := NewCodexSource(env).Read(context.Background())
	if r.State != StateOK || r.Plan != "plus" {
		t.Fatalf("reading %+v", r)
	}
	if len(r.Windows) != 2 || r.Windows[0].Label != "5h" || r.Windows[0].UsedPercent != 2 ||
		r.Windows[1].Label != "week" || r.Windows[1].UsedPercent != 30 {
		t.Fatalf("windows %+v", r.Windows)
	}
	if !r.Windows[1].ResetsAt.Equal(time.Unix(1794296504, 0)) {
		t.Fatalf("resets_at %v", r.Windows[1].ResetsAt)
	}
	if want := time.Date(2026, 11, 5, 18, 51, 0, 0, time.UTC); !r.ObservedAt.Equal(want) {
		t.Fatalf("observed %v, want the line's own time", r.ObservedAt)
	}
}

func TestCodexBoundsItsRead(t *testing.T) {
	env := testEnv(t)
	huge := strings.Repeat(fmt.Sprintf("%s\n", strings.Repeat("y", 4096)), 1024)
	writeFile(t, filepath.Join(env.Home, ".codex/sessions/2026/11/05/rollout.jsonl"), huge+codexLimitsLine+"\n")
	if r := NewCodexSource(env).Read(context.Background()); r.State != StateOK {
		t.Fatalf("limits at the tail of a large file: %+v", r)
	}
}

func TestCodexStates(t *testing.T) {
	env := testEnv(t)
	if r := NewCodexSource(env).Read(context.Background()); r.State != StateUnavailable {
		t.Fatalf("no sessions dir: %+v", r)
	}
	writeFile(t, filepath.Join(env.Home, ".codex/sessions/2026/11/05/rollout.jsonl"), `{"type":"session_meta"}`+"\n")
	if r := NewCodexSource(env).Read(context.Background()); r.State != StateUnknown {
		t.Fatalf("no limits in the tail: %+v", r)
	}
}
