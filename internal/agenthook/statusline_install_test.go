package agenthook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const installedOriginal = `{"statusLine":{"type":"command","command":"bash ~/.claude/line.sh","padding":2}}`

// claudeHome returns a temp home whose Claude settings hold installedOriginal.
func claudeHome(t *testing.T) (home, settings string) {
	t.Helper()
	home = t.TempDir()
	settings = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(installedOriginal), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, settings
}

func settingsStatusLine(t *testing.T, settings string) any {
	t.Helper()
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root["statusLine"]
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestEnsureOnHostWithWrapsOnceAndRestores(t *testing.T) {
	home, settings := claudeHome(t)
	if _, err := EnsureOnHostWith(home, Options{ClaudeStatusline: true}); err != nil {
		t.Fatal(err)
	}
	line, _ := settingsStatusLine(t, settings).(map[string]any)
	command, _ := line["command"].(string)
	if !strings.Contains(command, statusLineMarker) || line["padding"] != float64(2) {
		t.Fatalf("not wrapped: %v", line)
	}
	st, err := os.Stat(statusLineScriptPath(home))
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("script mode %v err %v", st, err)
	}

	before, _ := os.ReadFile(settings)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(settings, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHostWith(home, Options{ClaudeStatusline: true}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(settings)
	st, _ = os.Stat(settings)
	if !bytes.Equal(after, before) || !st.ModTime().Equal(old) {
		t.Fatalf("second run rewrote settings: mtime %v\n%s", st.ModTime(), after)
	}

	if _, err := EnsureOnHostWith(home, Options{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "command", "command": "bash ~/.claude/line.sh", "padding": float64(2)}
	if got := settingsStatusLine(t, settings); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored %v", got)
	}
	if exists(statusLineScriptPath(home)) {
		t.Fatal("script left behind after restoring")
	}
}

func TestEnsureOnHostOffLeavesUnwrappedSettingsAlone(t *testing.T) {
	home, settings := claudeHome(t)
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(settings)
	stale := statusLineSavedPath(home)
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte(`{"existed":true,"statusLine":{"command":"stale"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHostWith(home, Options{}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(settings); !bytes.Equal(after, before) {
		t.Fatalf("settings changed:\n%s", after)
	}
}

func TestEnsureOnHostOffCorruptSavedFileKeepsTheWrapper(t *testing.T) {
	home, settings := claudeHome(t)
	if _, err := EnsureOnHostWith(home, Options{ClaudeStatusline: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusLineSavedPath(home), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHostWith(home, Options{}); err == nil {
		t.Fatal("want an error for a corrupt saved file")
	}
	if !exists(statusLineScriptPath(home)) {
		t.Fatal("script removed while settings still point at it")
	}
	line, _ := settingsStatusLine(t, settings).(map[string]any)
	if command, _ := line["command"].(string); !strings.Contains(command, statusLineMarker) {
		t.Fatalf("wrapped line lost: %v", line)
	}
}

func TestEnsureOnHostScriptFailureSkipsTheWrapButWritesHooks(t *testing.T) {
	home, settings := claudeHome(t)
	// A directory at the script path makes the script write fail.
	if err := os.MkdirAll(statusLineScriptPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHostWith(home, Options{ClaudeStatusline: true}); err == nil {
		t.Fatal("want the script error")
	}
	line, _ := settingsStatusLine(t, settings).(map[string]any)
	if line["command"] != "bash ~/.claude/line.sh" {
		t.Fatalf("wrapped without a script: %v", line)
	}
	if raw, _ := os.ReadFile(settings); !strings.Contains(string(raw), reporterName) {
		t.Fatalf("other hooks not written:\n%s", raw)
	}
}

func TestStatusLineWrapRefusesANonObject(t *testing.T) {
	home := t.TempDir()
	root := map[string]any{"statusLine": "bash line.sh"}
	if changed, err := WrapStatusLine(home, root); err == nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if root["statusLine"] != "bash line.sh" || exists(statusLineSavedPath(home)) {
		t.Fatalf("changed something: %v", root)
	}
}

func TestStatusLineUnwrapMissingSavedFileKeepsRoot(t *testing.T) {
	home := t.TempDir()
	root := statusRoot(": " + statusLineMarker + "; true")
	if changed, err := UnwrapStatusLine(home, root); err == nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(statusCommand(t, root), statusLineMarker) {
		t.Fatalf("root changed: %v", root)
	}
}

func TestStatusLineWrappedOriginalMayEndInAComment(t *testing.T) {
	home := t.TempDir()
	got, code := runWrapper(t, home, "/bin/true", "printf painted # trailing comment", "{}")
	if got != "painted" || code != 0 {
		t.Fatalf("out %q code %d", got, code)
	}
}

func TestStatusLineWrapperHandlesAQuoteInThePaths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "o'brien")
	got, code := runWrapper(t, home, filepath.Join(home, "no'bin"), "cat", "in")
	if got != "in" || code != 0 {
		t.Fatalf("out %q code %d", got, code)
	}
}

func TestStatusLineWrappedBlankOriginalKeepsItsOutputEmpty(t *testing.T) {
	home := t.TempDir()
	got, code := runWrapper(t, home, "/bin/true", "  ", `{"a":1}`)
	if got != "" || code != 0 {
		t.Fatalf("out %q code %d", got, code)
	}
}
