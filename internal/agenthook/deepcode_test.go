package agenthook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeepcodeProjectCodeMatchesCLI(t *testing.T) {
	long := "/home/" + strings.Repeat("a", 70) + "/Repo Name!"
	cases := []struct {
		root, want string
	}{
		{"/home/code/repos/aiman", "-home-code-repos-aiman"},
		{"/tmp/x", "-tmp-x"},
		{long, "Repo-Name-3f3b3014c9b3edb5"},
	}
	for _, c := range cases {
		if got := DeepcodeProjectCode(c.root); got != c.want {
			t.Errorf("DeepcodeProjectCode(%q)=%q want %q", c.root, got, c.want)
		}
	}
}

func TestDeepcodeChooseEntry(t *testing.T) {
	started := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	old := started.Add(-time.Hour)
	fresh := started.Add(time.Second)
	entries := []DeepcodeEntry{
		{ID: "old", Status: "completed", UpdateTime: old},
		{ID: "new", Status: "processing", UpdateTime: fresh, Summary: "fix the index"},
	}
	got, ok := DeepcodeChooseEntry(entries, "", started)
	if !ok || got.ID != "new" {
		t.Fatalf("choose new: %+v ok=%v", got, ok)
	}
	got, ok = DeepcodeChooseEntry(entries, "old", started)
	if !ok || got.ID != "old" {
		t.Fatalf("resume id wins: %+v ok=%v", got, ok)
	}
	if _, ok := DeepcodeChooseEntry(entries[:1], "", started); ok {
		t.Fatal("a session updated before launch is not this process")
	}
}

func TestDeepcodeReportMapsLifecycle(t *testing.T) {
	entry := DeepcodeEntry{
		ID:         "123e4567-e89b-12d3-a456-426614174000",
		Summary:    "wire deepcode",
		Status:     "ask_permission",
		UpdateTime: time.Now().UTC(),
		Ask:        []DeepcodeAsk{{Name: "bash", Command: "git push", Scopes: []string{"network"}}},
	}
	r := DeepcodeReport(entry, "/home/dev/.deepcode/projects/-tmp-x", 3)
	if r.ID != entry.ID || r.State != "waiting_input" || r.Source != SourceLifecycle || r.Seq != 3 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Path, entry.ID+".jsonl") || !strings.Contains(r.Path, "/.deepcode/") {
		t.Fatalf("path %q", r.Path)
	}
	if !strings.Contains(r.Message, "git push") || !strings.Contains(r.Message, "network") {
		t.Fatalf("message %q", r.Message)
	}
	if r.Title != "wire deepcode" {
		t.Fatalf("title %q", r.Title)
	}

	failed := DeepcodeReport(DeepcodeEntry{ID: "i", Status: "failed", FailReason: "boom"}, "/p", 1)
	if failed.State != "errored" || failed.Message != "boom" {
		t.Fatalf("%+v", failed)
	}
	idle := DeepcodeReport(DeepcodeEntry{ID: "i", Status: "completed"}, "/p", 1)
	if idle.State != "idle" {
		t.Fatalf("%+v", idle)
	}
	working := DeepcodeReport(DeepcodeEntry{ID: "i", Status: "processing"}, "/p", 1)
	if working.State != "working" {
		t.Fatalf("%+v", working)
	}
}

func TestWatchDeepcodeEmitsIdentityThenEnd(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	t.Setenv("AIMAN_ENV", "1")
	t.Setenv("AIMAN_ID", "sess-1")
	code := DeepcodeProjectCode(dir)
	index := filepath.Join(home, ".deepcode", "projects", code, "sessions-index.json")
	if err := os.MkdirAll(filepath.Dir(index), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "version": 1,
	  "originalPath": "` + dir + `",
	  "entries": [{
	    "id": "123e4567-e89b-12d3-a456-426614174000",
	    "summary": "first",
	    "status": "processing",
	    "updateTime": "2026-10-05T12:00:02.000Z"
	  }]
	}`
	if err := os.WriteFile(index, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var got []Report
	done := make(chan struct{})
	go func() {
		_ = WatchDeepcode(ctx, DeepcodeWatch{
			Home:     home,
			Dir:      dir,
			Interval: 15 * time.Millisecond,
			Now:      func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) },
			Emit: func(r Report) {
				got = append(got, r)
				if r.State == "working" {
					cancel()
				}
			},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not stop")
	}
	if len(got) < 2 {
		t.Fatalf("want working then ended, got %+v", got)
	}
	if got[0].ID == "" || got[0].State != "working" {
		t.Fatalf("first %+v", got[0])
	}
	last := got[len(got)-1]
	if !last.Ended || last.Seq <= got[0].Seq {
		t.Fatalf("end %+v", last)
	}
}

func TestDeepcodeLaunchScriptReportsAndExecsCLI(t *testing.T) {
	if !strings.Contains(DeepcodeLaunchScript, "session deepcode-watch") || !strings.Contains(DeepcodeLaunchScript, "deepcode \"$@\"") {
		t.Fatalf("script must watch and then run the CLI:\n%s", DeepcodeLaunchScript)
	}
	if strings.Contains(DeepcodeLaunchScript, "exec deepcode") {
		t.Fatal("exec would drop the watcher before it can report session end")
	}
}

// A pending update in update-check.json makes Deep Code open an install
// prompt before the app. The launcher clears it inside aiman sessions only.
func TestDeepcodeLaunchScriptClearsPendingUpdateUnderAiman(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	run := func(aimanEnv string) bool {
		t.Helper()
		home := t.TempDir()
		bin := t.TempDir()
		state := filepath.Join(home, ".deepcode", "update-check.json")
		if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state, []byte(`{"pending":{"latestVersion":"9.9.9"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "deepcode"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(bin, "deepcode-launch.sh")
		if err := os.WriteFile(script, []byte(DeepcodeLaunchScript), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", script)
		cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "AIMAN_ENV=" + aimanEnv}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("launcher: %v\n%s", err, out)
		}
		_, err := os.Stat(state)
		return err == nil
	}
	if run("1") {
		t.Error("AIMAN_ENV=1 must remove update-check.json")
	}
	if !run("") {
		t.Error("outside aiman the user's update-check.json must stay")
	}
}
