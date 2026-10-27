package agenthook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureOnHostWritesReporterOnlyWhenNoAgents(t *testing.T) {
	home := t.TempDir()
	results, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d %+v", len(results), results)
	}
	if results[0].Action != ActionInstalled {
		t.Fatalf("action %s", results[0].Action)
	}
	body, err := os.ReadFile(reporterPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "report-agent-session --from-stdin") {
		t.Fatalf("script: %s", body)
	}
	todo, err := os.ReadFile(todoReporterPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(todo) != hookScript("todo report --from-stdin") {
		t.Fatalf("todo script:\n%s", todo)
	}
	if !strings.Contains(string(todo), `"$bin" todo report --from-stdin >/dev/null 2>&1 || true`) {
		t.Fatalf("todo script: %s", todo)
	}
}

func TestEnsureClaudeRegistersTodoAndTurnHooks(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	root := readHookRoot(t, filepath.Join(claude, "settings.json"))
	hooks, _ := root["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 1 {
		t.Fatalf("PostToolUse groups=%d", len(post))
	}
	group, _ := post[0].(map[string]any)
	if group["matcher"] != "TodoWrite|TaskCreate|TaskUpdate" {
		t.Fatalf("matcher %v", group["matcher"])
	}
	if hookCommand(group) != todoReporterPath(home) {
		t.Fatalf("todo command %q", hookCommand(group))
	}
	if !strings.Contains(hookCommand(group), TodoMarker) {
		t.Fatalf("todo command %q", hookCommand(group))
	}
	for _, ev := range []string{"Stop", "UserPromptSubmit", "SessionStart"} {
		arr, _ := hooks[ev].([]any)
		if len(arr) != 1 {
			t.Fatalf("%s groups=%d", ev, len(arr))
		}
		g, _ := arr[0].(map[string]any)
		if hookCommand(g) != reporterPath(home) {
			t.Fatalf("%s command %q", ev, hookCommand(g))
		}
		if !strings.Contains(hookCommand(g), Marker) {
			t.Fatalf("%s command %q", ev, hookCommand(g))
		}
	}
}

func TestEnsureOnHostIsIdempotent(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) == 0 {
		t.Fatal("no results")
	}
	for _, r := range again {
		if r.Action != ActionCurrent {
			t.Fatalf("second ensure %s action=%s", r.Path, r.Action)
		}
	}
	root := readHookRoot(t, filepath.Join(claude, "settings.json"))
	hooks, _ := root["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	n := 0
	for _, item := range post {
		group, _ := item.(map[string]any)
		if strings.Contains(hookCommand(group), TodoMarker) {
			n++
		}
	}
	if n != 1 || len(post) != 1 {
		t.Fatalf("PostToolUse aiman groups=%d total=%d", n, len(post))
	}
}

func TestUpsertTodoHookUpdatesMatcher(t *testing.T) {
	cmd := "/tmp/home/.aiman/hooks/todo-report"
	root := map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "echo pre"},
					},
				},
				map[string]any{
					"matcher": "TodoWrite",
					"hooks": []any{
						map[string]any{"type": "command", "command": cmd},
					},
				},
			},
		},
	}
	if !upsertTodoHook(root, cmd, "TodoWrite|update_plan") {
		t.Fatal("expected matcher rewrite")
	}
	if upsertTodoHook(root, cmd, "TodoWrite|update_plan") {
		t.Fatal("expected no change on the second call")
	}
	hooks, _ := root["hooks"].(map[string]any)
	post, _ := hooks["PostToolUse"].([]any)
	if len(post) != 2 {
		t.Fatalf("groups=%d", len(post))
	}
	bash, _ := post[0].(map[string]any)
	if bash["matcher"] != "Bash" || hookCommand(bash) != "echo pre" {
		t.Fatalf("foreign group changed: %#v", bash)
	}
	group, _ := post[1].(map[string]any)
	if group["matcher"] != "TodoWrite|update_plan" {
		t.Fatalf("matcher %v", group["matcher"])
	}
	if hookCommand(group) != cmd {
		t.Fatalf("command %q", hookCommand(group))
	}
}

func TestEnsureCodexTurnHooks(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(dir, "hooks.json")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "update_plan") || strings.Contains(string(raw), TodoMarker) || strings.Contains(string(raw), "PostToolUse") {
		t.Fatalf("codex must not mirror to-dos: %s", raw)
	}
	root := readHookRoot(t, rawPath)
	hooks, _ := root["hooks"].(map[string]any)
	for _, ev := range []string{"Stop", "UserPromptSubmit", "SessionStart"} {
		arr, _ := hooks[ev].([]any)
		if len(arr) != 1 {
			t.Fatalf("%s groups=%d %s", ev, len(arr), raw)
		}
		group, _ := arr[0].(map[string]any)
		if hookCommand(group) != reporterPath(home) {
			t.Fatalf("%s command %q", ev, hookCommand(group))
		}
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Path == rawPath && r.Action != ActionCurrent {
			t.Fatalf("second ensure action=%s", r.Action)
		}
	}
}

func readHookRoot(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func hookCommand(group map[string]any) string {
	hooks, _ := group["hooks"].([]any)
	if len(hooks) == 0 {
		return ""
	}
	item, _ := hooks[0].(map[string]any)
	cmd, _ := item["command"].(string)
	return cmd
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	root := readHookRoot(t, path)
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("no hooks in %s", path)
	}
	return hooks
}

func hookHasMarker(v any) bool {
	return markedHookGroups(v) > 0
}

func markedHookGroups(v any) int {
	arr, _ := v.([]any)
	n := 0
	for _, item := range arr {
		group, _ := item.(map[string]any)
		if strings.Contains(hookCommand(group), Marker) {
			n++
		}
	}
	return n
}

func TestEnsureOnHostRegistersPreCompactForClaudeAndCodexOnly(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".claude", ".codex", ".copilot"} {
		mustMkdir(t, filepath.Join(home, d))
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".claude/settings.json", ".codex/hooks.json"} {
		hooks := readHooks(t, filepath.Join(home, f))
		if !hookHasMarker(hooks["PreCompact"]) {
			t.Errorf("%s: PreCompact missing", f)
		}
	}
	if _, ok := readHooks(t, filepath.Join(home, ".copilot/settings.json"))["PreCompact"]; ok {
		t.Error("copilot must not get PreCompact")
	}
}

func TestEnsureOnHostPreCompactIsIdempotent(t *testing.T) {
	home := t.TempDir()
	mustMkdir(t, filepath.Join(home, ".claude"))
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	found := false
	for _, r := range again {
		if r.Path != settings {
			continue
		}
		found = true
		if r.Action != ActionCurrent {
			t.Fatalf("second ensure %s action=%s", r.Path, r.Action)
		}
	}
	if !found {
		t.Fatal("second ensure did not report .claude/settings.json")
	}
	if n := markedHookGroups(readHooks(t, settings)["PreCompact"]); n != 1 {
		t.Fatalf("PreCompact marked groups=%d", n)
	}
}

func TestEnsureOnHostMergesClaudeWithoutClobber(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	orig := []byte(`{"permissions":{"defaultMode":"auto"},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo pre"}]}]}}`)
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), orig, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if Summarize(results) == "" {
		t.Fatal("empty summary")
	}
	raw, err := os.ReadFile(filepath.Join(claude, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root["permissions"] == nil {
		t.Fatalf("clobbered: %s", raw)
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks["PreToolUse"] == nil {
		t.Fatalf("lost PreToolUse: %s", raw)
	}
	if !strings.Contains(string(raw), Marker) {
		t.Fatalf("missing marker: %s", raw)
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Path == filepath.Join(claude, "settings.json") && r.Action != ActionCurrent {
			t.Fatalf("second ensure action=%s", r.Action)
		}
	}
}

func TestEnsureOnHostSkipsMissingAgentDirs(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); !os.IsNotExist(err) {
		t.Fatalf("cursor dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok")); !os.IsNotExist(err) {
		t.Fatalf("grok dir: %v", err)
	}
}

func TestEnsureOnHostWritesGrokOwnedFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".grok", "hooks", "aiman.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "SessionStart") || !strings.Contains(string(raw), "SessionEnd") {
		t.Fatalf("%s", raw)
	}
	if !strings.Contains(string(raw), "idle_prompt") || !strings.Contains(string(raw), Marker) {
		t.Fatalf("%s", raw)
	}
}

func TestEnsureOnHostWritesKiloPlugin(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "kilo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "kilo", "plugin", "aiman-native-session.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "report-agent-session") || !strings.Contains(string(raw), "session.idle") {
		t.Fatalf("%s", raw)
	}
}

func TestEnsureOnHostMergesMuseSettings(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "muse")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	orig := []byte("{\n  \"schema_version\": 1,\n  \"model\": \"muse-spark-1.2\"\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), orig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root["model"] != "muse-spark-1.2" {
		t.Fatalf("clobbered model: %s", raw)
	}
	if root["schema_version"] != float64(1) {
		t.Fatalf("schema_version: %s", raw)
	}
	for _, ev := range []string{"SessionStart", "SessionEnd", "Stop", "UserPromptSubmit"} {
		if !strings.Contains(string(raw), ev) {
			t.Fatalf("missing %s: %s", ev, raw)
		}
	}
	if !strings.Contains(string(raw), Marker) {
		t.Fatalf("missing reporter: %s", raw)
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Path == filepath.Join(dir, "settings.json") && r.Action != ActionCurrent {
			t.Fatalf("second ensure action=%s", r.Action)
		}
	}
}

func TestEnsureOnHostDoesNotTouchAgeni(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ageni"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureOnHost(home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".ageni"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("ageni was touched: %v", entries)
	}
}

// The reporter must find the aiman binary without being handed a path.
//
// AIMAN_BIN_PATH used to be injected by whoever created the session — the
// laptop, for a remote session — so remote hooks were pointed at a path that
// did not exist there and every report silently failed. The script now
// resolves the binary where it actually runs, and still honours an explicit
// override when one is valid.
func TestReporterScriptResolvesBinaryWithoutInjectedPath(t *testing.T) {
	for _, want := range []string{
		`command -v aiman`,
		`$HOME/.local/bin/aiman`,
		`${AIMAN_BIN_PATH:-}`,
	} {
		if !strings.Contains(reporterScript, want) {
			t.Errorf("reporter script should contain %q so it works on the session's own host", want)
		}
	}
	// Still a no-op outside an aiman session, and still never fails the agent.
	if !strings.Contains(reporterScript, `[ "${AIMAN_ENV:-}" = 1 ] || exit 0`) {
		t.Error("reporter must stay inert outside an aiman session")
	}
}

func TestEnsureOnHostSecondRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".claude", ".grok", ".cursor", ".codex", ".copilot", ".gemini", ".pi",
		filepath.Join(".config", "kilo"), filepath.Join(".config", "muse")} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	first, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_000_000_000, 0)
	for _, r := range first {
		if err := os.Chtimes(r.Path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	again, err := EnsureOnHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(first) {
		t.Fatalf("results %d then %d", len(first), len(again))
	}
	for _, r := range again {
		if r.Action != ActionCurrent {
			t.Errorf("second ensure %s action=%s", r.Path, r.Action)
		}
		st, err := os.Stat(r.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().Equal(old) {
			t.Errorf("second ensure rewrote %s", r.Path)
		}
	}
}

func TestUpsertJSONFileSameBytesIsCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := "{\n  \"a\": 1\n}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	// mutate reports a change but leaves the document as it was.
	res, err := upsertJSONFile(path, func(root map[string]any) bool {
		root["a"] = 1
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCurrent {
		t.Fatalf("action=%s, want %s", res.Action, ActionCurrent)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(old) {
		t.Fatal("file was rewritten")
	}
}
