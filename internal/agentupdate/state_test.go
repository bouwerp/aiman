package agentupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "agent-versions.json")
	at := time.Date(2026, 10, 28, 3, 15, 0, 0, time.UTC)
	s := NewState()
	s.Agents["claude"] = AgentState{
		Agent: "claude", Installed: "2.1.312", Latest: "2.1.315", Method: MethodNative,
		Path: "/x/claude", CheckedAt: at,
		LastUpdate: &LastUpdate{At: at, From: "2.1.311", To: "2.1.312", OK: true, Log: "/x/log"},
	}
	if !s.MarkNotified("claude", "2.1.315") {
		t.Fatal("first mark should be new")
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Agents["claude"]
	if c.Installed != "2.1.312" || c.Latest != "2.1.315" || c.Method != MethodNative || !c.CheckedAt.Equal(at) {
		t.Errorf("agent: %+v", c)
	}
	if c.LastUpdate == nil || c.LastUpdate.From != "2.1.311" || !c.LastUpdate.OK {
		t.Errorf("last update: %+v", c.LastUpdate)
	}
	if !got.WasNotified("claude", "2.1.315") || got.WasNotified("claude", "2.1.316") {
		t.Error("notified set not preserved")
	}
	if got.MarkNotified("claude", "2.1.315") {
		t.Error("second mark should not be new")
	}
}

func TestLoadStateMissingIsEmpty(t *testing.T) {
	s, err := LoadState(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Agents == nil || len(s.Agents) != 0 {
		t.Errorf("%+v", s)
	}
}

func TestStateSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-versions.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"old":{"agent":"old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewState()
	s.Agents["new"] = AgentState{Agent: "new"}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
	got, _ := LoadState(path)
	if _, ok := got.Agents["old"]; ok {
		t.Error("old content survived")
	}

	if os.Geteuid() == 0 {
		return
	}
	// A save that cannot complete leaves the previous file intact.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	s.Agents["other"] = AgentState{Agent: "other"}
	if err := s.Save(path); err == nil {
		t.Fatal("expected save error in read-only dir")
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Agents["new"]; !ok {
		t.Error("previous content lost")
	}
}

func TestStateOutdated(t *testing.T) {
	a := AgentState{Installed: "1.0.46", Latest: "1.0.50"}
	if !a.Outdated() {
		t.Error("should be outdated")
	}
	for _, b := range []AgentState{{Installed: "1.0.50", Latest: "1.0.50"}, {Installed: "1.0.50", Latest: ""}, {Installed: "", Latest: "1.0"}, {Installed: "1.0.51", Latest: "1.0.50"}} {
		if b.Outdated() {
			t.Errorf("%+v should not be outdated", b)
		}
	}
}
