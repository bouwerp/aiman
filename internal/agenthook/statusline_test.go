package agenthook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func statusRoot(cmd string) map[string]any {
	return map[string]any{"model": "opus", "statusLine": map[string]any{"type": "command", "command": cmd, "padding": float64(2), "refreshInterval": float64(5)}}
}

func statusCommand(t *testing.T, root map[string]any) string {
	t.Helper()
	sl, ok := root["statusLine"].(map[string]any)
	if !ok {
		t.Fatalf("statusLine is %T", root["statusLine"])
	}
	cmd, _ := sl["command"].(string)
	return cmd
}

func TestStatusLineWrapKeepsTheObjectAndRestoresExactly(t *testing.T) {
	home := t.TempDir()
	root := statusRoot("bash ~/.claude/statusline-command.sh")
	want := statusRoot("bash ~/.claude/statusline-command.sh")
	changed, err := WrapStatusLine(home, root)
	if err != nil || !changed {
		t.Fatalf("wrap changed=%v err=%v", changed, err)
	}
	sl := root["statusLine"].(map[string]any)
	if sl["padding"] != float64(2) || sl["refreshInterval"] != float64(5) || sl["type"] != "command" {
		t.Fatalf("other keys lost: %v", sl)
	}
	if statusCommand(t, root) == "bash ~/.claude/statusline-command.sh" {
		t.Fatal("command not wrapped")
	}
	changed, err = WrapStatusLine(home, root)
	if err != nil || changed {
		t.Fatalf("second wrap changed=%v err=%v, want no write", changed, err)
	}
	changed, err = UnwrapStatusLine(home, root)
	if err != nil || !changed || !reflect.DeepEqual(root, want) {
		t.Fatalf("restore changed=%v err=%v root=%v", changed, err, root)
	}
}

func TestStatusLineNewCommandBecomesTheOriginal(t *testing.T) {
	home := t.TempDir()
	root := statusRoot("first")
	if _, err := WrapStatusLine(home, root); err != nil {
		t.Fatal(err)
	}
	replaced := statusRoot("second")
	if changed, err := WrapStatusLine(home, replaced); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := UnwrapStatusLine(home, replaced); err != nil {
		t.Fatal(err)
	}
	if got := statusCommand(t, replaced); got != "second" {
		t.Fatalf("restored %q", got)
	}
}

func TestStatusLineUnsetStaysRestorable(t *testing.T) {
	home := t.TempDir()
	root := map[string]any{"model": "opus"}
	if changed, err := WrapStatusLine(home, root); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := UnwrapStatusLine(home, root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["statusLine"]; ok {
		t.Fatalf("restored a status line that never existed: %v", root)
	}
}

// runWrapper runs the wrapped command as Claude Code does, through a shell.
func runWrapper(t *testing.T, home, aimanBin, original, stdin string) (string, int) {
	t.Helper()
	if _, err := WriteStatusLineWrapper(home, aimanBin); err != nil {
		t.Fatal(err)
	}
	root := statusRoot(original)
	if _, err := WrapStatusLine(home, root); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", statusCommand(t, root))
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

func TestStatusLineWrapperPassesInputAndOutputThrough(t *testing.T) {
	home := t.TempDir()
	in := `{"rate_limits":{"five_hour":{"used_percentage":1}},"weird":"b\\ack \"q\" é"}` + "\n"
	got, code := runWrapper(t, home, "/bin/true", `cat; printf 'line two\n'; exit 3`, in)
	if got != in+"line two\n" || code != 3 {
		t.Fatalf("out %q code %d", got, code)
	}
}

func TestStatusLineWrapperPaintsWhenRecordingFails(t *testing.T) {
	home := t.TempDir()
	got, code := runWrapper(t, home, filepath.Join(home, "no-such-binary"), `printf painted`, `{"rate_limits":{}}`)
	if got != "painted" || code != 0 {
		t.Fatalf("out %q code %d", got, code)
	}
}

func TestStatusLineWrapperDoesNotWaitForRecording(t *testing.T) {
	home := t.TempDir()
	slow := filepath.Join(home, "slow")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		got, _ := runWrapper(t, home, slow, `printf painted`, `{"rate_limits":{}}`)
		done <- got
	}()
	select {
	case got := <-done:
		if got != "painted" {
			t.Errorf("out %q", got)
		}
	case <-timeAfter2s():
		t.Fatal("status line waited for the recorder")
	}
}

func timeAfter2s() <-chan time.Time { return time.After(2 * time.Second) }
