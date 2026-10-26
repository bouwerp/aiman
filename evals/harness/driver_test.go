package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/evals/fakeagent"
	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
)

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestDriverStartsAndStops(t *testing.T) {
	h := New(t)
	socket := h.Socket
	h.Close()
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket still exists: %v", err)
	}
}

func TestFakeAgentReportsScriptedHooks(t *testing.T) {
	h := New(t)
	h.RegisterSession(domain.Session{ID: "s1"})
	script := fakeagent.Script{SessionID: "s1", Name: "s1", Steps: []fakeagent.Step{
		{Event: "Stop", State: "idle"},
		{Event: "Stop", State: "working"},
		{Event: "Stop", State: "idle"},
	}}
	if err := h.RunFakeAgent(script); err != nil {
		t.Fatal(err)
	}
	var hooks int
	for _, ev := range h.Events("s1") {
		if ev.Kind == domain.EventHookReported {
			hooks++
		}
	}
	if hooks != 3 {
		t.Fatalf("hook events = %d, want 3", hooks)
	}
}

func TestFakeAgentPreCompactWritesHandover(t *testing.T) {
	h := New(t)
	h.RegisterSession(domain.Session{ID: "s1"})
	if err := h.RunFakeAgent(fakeagent.Script{SessionID: "s1", Steps: []fakeagent.Step{
		{Event: "PreCompact", State: "working"},
	}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.Dir, "sessions", "s1", domain.AimanHandoverFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("handover: %v", err)
	}
	var written bool
	for _, ev := range h.Events("s1") {
		if ev.Kind == domain.EventHandoverWritten {
			written = true
		}
	}
	if !written {
		t.Fatal("handover.written event missing")
	}
}

func TestDriverRecordsTmuxSendKeys(t *testing.T) {
	h := New(t)
	h.RegisterSession(domain.Session{
		ID: "s1", HookState: domain.AgentStateIdle, HookStateAt: time.Now().UTC(),
		HookStateSource: agenthook.SourceIdlePrompt,
	})
	if _, err := h.Call("steer.send", map[string]any{"session": "s1", "text": "go"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return countSendKeys(h.Execs()) >= 2 })
	if typed := countSendKeys(h.Execs()); typed < 2 {
		t.Fatalf("send-keys commands = %d, execs = %v", typed, h.Execs())
	}
}

func countSendKeys(execs []string) int {
	n := 0
	for _, cmd := range execs {
		if strings.Contains(cmd, "tmux send-keys") {
			n++
		}
	}
	return n
}
