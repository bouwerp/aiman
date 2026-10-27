package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestTrackerContextUsesExplicitWindow(t *testing.T) {
	mainPath := copyClaudeTree(t)
	env := newScanEnv(domain.Session{
		ID: "s1", AgentName: "claude", AgentSessionPath: mainPath,
	})
	env.tracker.SetContextWindow(123_456)
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := env.store.meta("s1")
	if got.LastInputTokens != 2+71787 || got.ContextWindow != 123_456 || got.ContextSource != "explicit" {
		t.Fatalf("meta = %+v", got)
	}
}

func TestTrackerScansClaudeAndSubagents(t *testing.T) {
	mainPath := copyClaudeTree(t)
	env := newScanEnv(domain.Session{
		ID:               "s1",
		Name:             "alpha",
		Group:            "work",
		IssueKey:         "PROJ-6",
		AgentName:        "claude",
		AgentSessionPath: mainPath,
	})
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{"s1"}) {
		t.Fatalf("changed = %#v", changed)
	}
	if !reflect.DeepEqual(env.changes, []string{"s1"}) {
		t.Fatalf("onChange = %#v", env.changes)
	}
	wantMeta := domain.UsageSessionMeta{
		SessionID: "s1", Name: "alpha", Group: "work", IssueKey: "PROJ-6", AgentName: "claude",
		LastInputTokens: 2 + 71787, ContextWindow: 200_000, ContextSource: "fallback",
	}
	if got := env.store.meta("s1"); got != wantMeta {
		t.Fatalf("meta = %+v, want %+v", got, wantMeta)
	}
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := env.store.meta("s1"); got != wantMeta {
		t.Fatalf("second scan meta = %+v, want %+v", got, wantMeta)
	}
	want := []domain.UsageBucket{
		{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC),
			Model:     "claude-opus-5-5",
			Tokens:    domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005},
		},
		{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC),
			Model:     "claude-sonnet-5",
			Tokens:    domain.TokenCounts{Input: 3, Output: 50, CacheRead: 2000, CacheWrite5m: 1000},
		},
	}
	if got := env.store.buckets(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("buckets = %+v, want %+v", got, want)
	}
	info, err := os.Stat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	cur := env.store.cursor(t, mainPath)
	if cur.Offset != info.Size() || cur.Size != info.Size() || cur.State != "" {
		t.Fatalf("main cursor = %+v, size %d", cur, info.Size())
	}
	if cur.Fingerprint != "uuid:00000000-0000-4000-8000-000000000101" {
		t.Fatalf("fingerprint = %s", cur.Fingerprint)
	}
	sub := claudeSubagentPath(mainPath)
	subInfo, err := os.Stat(sub)
	if err != nil {
		t.Fatal(err)
	}
	subCur := env.store.cursor(t, sub)
	if subCur.Offset != subInfo.Size() || subCur.Fingerprint != "uuid:00000000-0000-4000-8000-000000000201" {
		t.Fatalf("sub cursor = %+v", subCur)
	}
}

func TestTrackerSecondScanIsNoop(t *testing.T) {
	mainPath := copyClaudeTree(t)
	env := newScanEnv(domain.Session{ID: "s1", IssueKey: "PROJ-6", AgentName: "claude", AgentSessionPath: mainPath})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	applies := env.store.applyCount()
	if applies == 0 {
		t.Fatal("first scan did not apply")
	}
	env.changes = nil
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %#v", changed)
	}
	if env.store.applyCount() != applies {
		t.Fatalf("applies = %d, want %d", env.store.applyCount(), applies)
	}
	if len(env.changes) != 0 {
		t.Fatalf("onChange = %#v", env.changes)
	}
}

func TestTrackerCountsMessageOnceAcrossScans(t *testing.T) {
	lines := fixtureLines(t, claudeFixture)
	aLines := linesContaining(lines, "msg_fixture_A")
	bLines := linesContaining(lines, "msg_fixture_B")
	if len(aLines) != 2 || len(bLines) != 1 {
		t.Fatalf("A lines %d, B lines %d", len(aLines), len(bLines))
	}
	path := filepath.Join(claudeProjectDir(t), "once.jsonl")
	writeLines(t, path, aLines[0])
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "claude", AgentSessionPath: path})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	appendLines(t, path, aLines[1], bLines[0])
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005}
	if got := sumBuckets(env.store.buckets(t)); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
}

func TestTrackerPicksUpCompletedLine(t *testing.T) {
	lines := fixtureLines(t, claudeFixture)
	aLines := linesContaining(lines, "msg_fixture_A")
	bLines := linesContaining(lines, "msg_fixture_B")
	if len(aLines) < 1 || len(bLines) != 1 {
		t.Fatalf("A lines %d, B lines %d", len(aLines), len(bLines))
	}
	path := filepath.Join(claudeProjectDir(t), "partial.jsonl")
	writeLines(t, path, aLines[0])
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "claude", AgentSessionPath: path})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := env.store.cursor(t, path)
	half := len(bLines[0]) / 2
	appendBytes(t, path, bLines[0][:half])
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("partial changed = %#v", changed)
	}
	mid := env.store.cursor(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Offset != before.Offset || mid.Offset >= info.Size() || mid.Size != info.Size() {
		t.Fatalf("partial cursor offset %d size %d, before %d, file %d", mid.Offset, mid.Size, before.Offset, info.Size())
	}
	if got := sumBuckets(env.store.buckets(t)); got.Output != 214 {
		t.Fatalf("partial tokens = %+v", got)
	}
	appendBytes(t, path, append(append([]byte{}, bLines[0][half:]...), '\n'))
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005}
	if got := sumBuckets(env.store.buckets(t)); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
}

func TestTrackerRewriteDoesNotDoubleCount(t *testing.T) {
	path := copyClaudeMain(t)
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "claude", AgentSessionPath: path})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	applies := env.store.applyCount()
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.store.applyCount() != applies+1 {
		t.Fatalf("rewrite applies = %d, want %d", env.store.applyCount(), applies+1)
	}
	wantMain := domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005}
	if got := sumBuckets(env.store.buckets(t)); got != wantMain {
		t.Fatalf("after same-bytes rewrite = %+v", got)
	}

	const newUUID = "00000000-0000-4000-8000-000000000199"
	rewritten := bytes.Replace(body, []byte("00000000-0000-4000-8000-000000000101"), []byte(newUUID), 1)
	rewritten = append(rewritten, []byte(assistantLine("msg_fixture_C", "claude-opus-5-5", `"input_tokens":7,"output_tokens":8`))...)
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := wantMain.Add(domain.TokenCounts{Input: 7, Output: 8})
	if got := sumBuckets(env.store.buckets(t)); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cur := env.store.cursor(t, path)
	if cur.Fingerprint != "uuid:"+newUUID || cur.Offset != info.Size() || cur.Size != info.Size() {
		t.Fatalf("cursor = %+v, file size %d", cur, info.Size())
	}
}

func TestTrackerCodexContinuesThreadInNewFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "user", ".codex", "sessions", "2026", "10", "22")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "rollout-1.jsonl")
	second := filepath.Join(dir, "rollout-2.jsonl")
	copyFile(t, codexFixture, first)
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "codex", AgentSessionPath: first})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := decodeCursorState(t, env.store.cursor(t, first).State)
	wantLast := CodexTotals{Input: 64880, Cached: 49184, CacheWrite: 0, Output: 733}
	if st.Model != "gpt-5.6-sol" || st.Last != wantLast || st.Seen != 65613 {
		t.Fatalf("seed state = %+v", st)
	}
	rawState := env.store.cursor(t, first).State
	for _, key := range []string{`"model":"gpt-5.6-sol"`, `"seen":65613`, `"input":64880`, `"cached":49184`, `"cachewrite":0`, `"output":733`} {
		if !strings.Contains(rawState, key) {
			t.Fatalf("state %s missing %s", rawState, key)
		}
	}
	before := sumModelBuckets(env.store.buckets(t), "gpt-5.6-sol")
	terra := sumModelBuckets(env.store.buckets(t), "gpt-5.6-terra")

	body, err := os.ReadFile(codexFixture)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, _ := bytes.Cut(body, []byte("\n"))
	var buf bytes.Buffer
	buf.Write(meta)
	buf.WriteByte('\n')
	next := wantLast
	next.Input += 1000
	next.Output += 10
	buf.WriteString(codexCountLine("2026-10-22T07:11:00Z", next.Input, next.Cached, next.CacheWrite, next.Output, next.Input+next.Output))
	if err := os.WriteFile(second, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// A lower watermark for this thread must lose. A higher one for another thread must not apply.
	lower := st
	lower.Last.Input -= 1000
	lower.Seen -= 1000
	lowerRaw, err := json.Marshal(lower)
	if err != nil {
		t.Fatal(err)
	}
	env.store.seedCursor(domain.UsageCursor{
		SessionID: "s1", Path: filepath.Join(dir, "older.jsonl"),
		Fingerprint: env.store.cursor(t, first).Fingerprint, State: string(lowerRaw),
	})
	higher := st
	higher.Last.Input += 50000
	higher.Seen += 50000
	higherRaw, err := json.Marshal(higher)
	if err != nil {
		t.Fatal(err)
	}
	env.store.seedCursor(domain.UsageCursor{
		SessionID: "s1", Path: filepath.Join(dir, "other-thread.jsonl"),
		Fingerprint: "thread:other", State: string(higherRaw),
	})
	env.sessions.list[0].AgentSessionPath = second
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{"s1"}) {
		t.Fatalf("changed = %#v", changed)
	}
	gotSol := sumModelBuckets(env.store.buckets(t), "gpt-5.6-sol")
	wantSol := before.Add(domain.TokenCounts{Input: 1000, Output: 10})
	if gotSol != wantSol {
		t.Fatalf("sol = %+v, want %+v", gotSol, wantSol)
	}
	if sumModelBuckets(env.store.buckets(t), "gpt-5.6-terra") != terra {
		t.Fatal("terra changed")
	}
	if sumModelBuckets(env.store.buckets(t), CodexUnknownModel) != (domain.TokenCounts{}) {
		t.Fatal("unknown model counted")
	}
	gotState := decodeCursorState(t, env.store.cursor(t, second).State)
	if gotState.Model != "gpt-5.6-sol" || gotState.Last != next || gotState.Seen != next.Input+next.Output {
		t.Fatalf("continued state = %+v, want last %+v seen %d", gotState, next, next.Input+next.Output)
	}
	if env.store.cursor(t, first).Path != first {
		t.Fatal("old cursor dropped")
	}
}

func TestTrackerCodexResumesSameFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "user", ".codex", "sessions", "2026", "10", "22")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout.jsonl")
	copyFile(t, codexFixture, path)
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "codex", AgentSessionPath: path})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	beforeSol := sumModelBuckets(env.store.buckets(t), "gpt-5.6-sol")
	beforeTerra := sumModelBuckets(env.store.buckets(t), "gpt-5.6-terra")
	next := CodexTotals{Input: 64880 + 1000, Cached: 49184, Output: 733 + 10}
	line := codexCountLine("2026-10-22T07:12:00Z", next.Input, next.Cached, next.CacheWrite, next.Output, next.Input+next.Output)
	appendBytes(t, path, []byte(line))
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := sumModelBuckets(env.store.buckets(t), "gpt-5.6-sol")
	if got != beforeSol.Add(domain.TokenCounts{Input: 1000, Output: 10}) {
		t.Fatalf("sol = %+v, before %+v", got, beforeSol)
	}
	if sumModelBuckets(env.store.buckets(t), "gpt-5.6-terra") != beforeTerra {
		t.Fatal("terra changed")
	}
	st := decodeCursorState(t, env.store.cursor(t, path).State)
	if st.Model != "gpt-5.6-sol" || st.Last != next || st.Seen != next.Input+next.Output {
		t.Fatalf("state = %+v, want last %+v", st, next)
	}
}

func TestTrackerIgnoresUnknownKinds(t *testing.T) {
	for _, tt := range []struct {
		path string
		want Kind
	}{
		{"/home/u/.claude/projects/p/a.jsonl", KindClaude},
		{"/home/u/.codex/sessions/2026/r.jsonl", KindCodex},
		{"/home/u/.grok/sessions/a.jsonl", KindUnknown},
		{"/home/u/.claude/projects/p/a.txt", KindUnknown},
		{"/tmp/notes.jsonl", KindUnknown},
	} {
		if got := KindForPath(tt.path); got != tt.want {
			t.Fatalf("KindForPath(%s) = %q, want %q", tt.path, got, tt.want)
		}
	}
	path := filepath.Join(t.TempDir(), "home", "user", ".grok", "sessions", "s.jsonl")
	writeLines(t, path, []byte(`{"type":"assistant","uuid":"00000000-0000-4000-8000-000000000101","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"msg_fixture_A","model":"claude-opus-5-5","usage":{"input_tokens":2,"output_tokens":2}}}`))
	env := newScanEnv(domain.Session{ID: "s1", Name: "g", IssueKey: "PROJ-9", AgentName: "grok", AgentSessionPath: path})
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 || env.store.applyCount() != 0 || len(env.store.cursors(t)) != 0 {
		t.Fatalf("changed %#v applies %d cursors %+v", changed, env.store.applyCount(), env.store.cursors(t))
	}
	if env.store.meta("s1").IssueKey != "PROJ-9" {
		t.Fatalf("meta = %+v", env.store.meta("s1"))
	}
}

func TestTrackerKeepsOldFiles(t *testing.T) {
	lines := fixtureLines(t, claudeFixture)
	aLines := linesContaining(lines, "msg_fixture_A")
	bLines := linesContaining(lines, "msg_fixture_B")
	dir := claudeProjectDir(t)
	oldPath := filepath.Join(dir, "old.jsonl")
	newPath := filepath.Join(dir, "new.jsonl")
	writeLines(t, oldPath, aLines[0])
	writeLines(t, newPath, []byte(`{"type":"user","uuid":"00000000-0000-4000-8000-0000000000bb"}`))
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "claude", AgentSessionPath: oldPath})
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	env.sessions.list[0].AgentSessionPath = newPath
	appendLines(t, oldPath, bLines[0])
	if _, err := env.tracker.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005}
	if got := sumBuckets(env.store.buckets(t)); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
	old := env.store.cursor(t, oldPath)
	info, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if old.Offset != info.Size() {
		t.Fatalf("old offset %d, file %d", old.Offset, info.Size())
	}
	if env.store.cursor(t, newPath).Path != newPath {
		t.Fatal("new path was not recorded")
	}
}

func TestTrackerSkipsFileErrors(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	mainPath := copyClaudeMain(t)
	missing := filepath.Join(filepath.Dir(mainPath), "missing.jsonl")
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "claude", AgentSessionPath: mainPath})
	env.store.seedCursor(domain.UsageCursor{
		SessionID: "s1", Path: missing, Size: 10, ModTime: time.Unix(1, 0).UTC(),
	})
	changed, err := env.tracker.ScanOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(changed, []string{"s1"}) {
		t.Fatalf("changed = %#v", changed)
	}
	if !bytes.Contains(buf.Bytes(), []byte(missing)) {
		t.Fatalf("log = %s", buf.String())
	}
	want := domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite1h: 36005}
	if got := sumBuckets(env.store.buckets(t)); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
}

func TestTrackerRunRescansOnKick(t *testing.T) {
	if ScanInterval != 15*time.Second {
		t.Fatalf("ScanInterval = %s", ScanInterval)
	}
	path := filepath.Join(claudeProjectDir(t), "run.jsonl")
	writeLines(t, path, []byte(strings.TrimSuffix(assistantLine("msg_run_1", "claude-opus-5-5", `"input_tokens":1,"output_tokens":1`), "\n")))
	env := newScanEnv(domain.Session{ID: "s1", AgentName: "claude", AgentSessionPath: path})
	got := make(chan string, 4)
	env.tracker.onChange = func(id string) { got <- id }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		env.tracker.Run(ctx, time.Hour)
		close(done)
	}()
	waitChange(t, got)
	appendLines(t, path, []byte(strings.TrimSuffix(assistantLine("msg_run_2", "claude-opus-5-5", `"input_tokens":2,"output_tokens":2`), "\n")))
	kicked := make(chan struct{})
	go func() {
		env.tracker.Kick()
		env.tracker.Kick()
		close(kicked)
	}()
	select {
	case <-kicked:
	case <-time.After(time.Second):
		t.Fatal("Kick blocked")
	}
	waitChange(t, got)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	want := domain.TokenCounts{Input: 3, Output: 3}
	if got := sumBuckets(env.store.buckets(t)); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
}

type scanEnv struct {
	store    *memStore
	sessions *memSessions
	tracker  *Tracker
	changes  []string
}

func newScanEnv(s domain.Session) *scanEnv {
	env := &scanEnv{
		store:    newMemStore(),
		sessions: &memSessions{list: []domain.Session{s}},
	}
	env.tracker = NewTracker(env.store, env.sessions, func(id string) {
		env.changes = append(env.changes, id)
	})
	return env
}

func waitChange(t *testing.T, got <-chan string) {
	t.Helper()
	select {
	case id := <-got:
		if id != "s1" {
			t.Fatalf("onChange = %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scan")
	}
}

func copyClaudeTree(t *testing.T) string {
	t.Helper()
	mainPath := copyClaudeMain(t)
	copyFile(t, claudeSubagentFixture, claudeSubagentPath(mainPath))
	return mainPath
}

func copyClaudeMain(t *testing.T) string {
	t.Helper()
	mainPath := filepath.Join(claudeProjectDir(t), "00000000-0000-4000-8000-000000000001.jsonl")
	copyFile(t, claudeFixture, mainPath)
	return mainPath
}

func claudeProjectDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "home", "user", ".claude", "projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func claudeSubagentPath(mainPath string) string {
	stem := strings.TrimSuffix(filepath.Base(mainPath), ".jsonl")
	return filepath.Join(filepath.Dir(mainPath), stem, "subagents", "agent-afixture.jsonl")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureLines(t *testing.T, path string) [][]byte {
	t.Helper()
	body := readFixture(t, path)
	raw := bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n"))
	return raw
}

func linesContaining(lines [][]byte, sub string) [][]byte {
	var out [][]byte
	for _, line := range lines {
		if bytes.Contains(line, []byte(sub)) {
			out = append(out, line)
		}
	}
	return out
}

func writeLines(t *testing.T, path string, lines ...[]byte) {
	t.Helper()
	var buf bytes.Buffer
	for _, line := range lines {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLines(t *testing.T, path string, lines ...[]byte) {
	t.Helper()
	var buf bytes.Buffer
	for _, line := range lines {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	appendBytes(t, path, buf.Bytes())
}

func appendBytes(t *testing.T, path string, p []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(p); err != nil {
		t.Fatal(err)
	}
}

func sumBuckets(buckets []domain.UsageBucket) domain.TokenCounts {
	var sum domain.TokenCounts
	for _, b := range buckets {
		sum = sum.Add(b.Tokens)
	}
	return sum
}

func sumModelBuckets(buckets []domain.UsageBucket, model string) domain.TokenCounts {
	var sum domain.TokenCounts
	for _, b := range buckets {
		if b.Model == model {
			sum = sum.Add(b.Tokens)
		}
	}
	return sum
}

func decodeCursorState(t *testing.T, raw string) CodexState {
	t.Helper()
	var st CodexState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("state %q: %v", raw, err)
	}
	return st
}

type memSessions struct {
	mu   sync.Mutex
	list []domain.Session
}

func (m *memSessions) List(context.Context) ([]domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Session, len(m.list))
	copy(out, m.list)
	return out, nil
}

type memStore struct {
	mu         sync.Mutex
	cursorRows map[string][]domain.UsageCursor
	seen       map[string]map[string]struct{}
	bucketRows []domain.UsageBucket
	metas      map[string]domain.UsageSessionMeta
	applies    int
}

func newMemStore() *memStore {
	return &memStore{
		cursorRows: map[string][]domain.UsageCursor{},
		seen:       map[string]map[string]struct{}{},
		metas:      map[string]domain.UsageSessionMeta{},
	}
}

func (m *memStore) seedCursor(cur domain.UsageCursor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursorRows[cur.SessionID] = append(m.cursorRows[cur.SessionID], cur)
}

func (m *memStore) applyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applies
}

func (m *memStore) meta(id string) domain.UsageSessionMeta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.metas[id]
}

func (m *memStore) cursor(t *testing.T, path string) domain.UsageCursor {
	t.Helper()
	for _, cur := range m.cursorsOf("s1") {
		if cur.Path == path {
			return cur
		}
	}
	t.Fatalf("no cursor for %s", path)
	return domain.UsageCursor{}
}

func (m *memStore) cursors(t *testing.T) []domain.UsageCursor {
	t.Helper()
	return m.cursorsOf("s1")
}

func (m *memStore) cursorsOf(sessionID string) []domain.UsageCursor {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.UsageCursor, len(m.cursorRows[sessionID]))
	copy(out, m.cursorRows[sessionID])
	return out
}

func (m *memStore) buckets(t *testing.T) []domain.UsageBucket {
	t.Helper()
	got, err := m.UsageBuckets(context.Background(), nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (m *memStore) UsageCursors(_ context.Context, sessionID string) ([]domain.UsageCursor, error) {
	return m.cursorsOf(sessionID), nil
}

func (m *memStore) ApplyUsage(_ context.Context, cur domain.UsageCursor, recs []domain.UsageRecord) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applies++
	added := false
	for _, rec := range recs {
		if rec.ID != "" {
			if _, ok := m.seen[cur.SessionID][rec.ID]; ok {
				continue
			}
			if m.seen[cur.SessionID] == nil {
				m.seen[cur.SessionID] = map[string]struct{}{}
			}
			m.seen[cur.SessionID][rec.ID] = struct{}{}
		}
		m.addBucket(cur.SessionID, rec)
		added = true
	}
	m.putCursorLocked(cur)
	return added, nil
}

func (m *memStore) addBucket(sessionID string, rec domain.UsageRecord) {
	hour := rec.At.UTC().Truncate(time.Hour)
	for i := range m.bucketRows {
		b := &m.bucketRows[i]
		if b.SessionID == sessionID && b.Hour.Equal(hour) && b.Model == rec.Model {
			b.Tokens = b.Tokens.Add(rec.Tokens)
			return
		}
	}
	m.bucketRows = append(m.bucketRows, domain.UsageBucket{
		SessionID: sessionID,
		Hour:      hour,
		Model:     rec.Model,
		Tokens:    rec.Tokens,
	})
}

func (m *memStore) putCursorLocked(cur domain.UsageCursor) {
	list := m.cursorRows[cur.SessionID]
	for i := range list {
		if list[i].Path == cur.Path {
			list[i] = cur
			m.cursorRows[cur.SessionID] = list
			return
		}
	}
	m.cursorRows[cur.SessionID] = append(list, cur)
}

func (m *memStore) UpsertUsageSession(_ context.Context, meta domain.UsageSessionMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.metas[meta.SessionID]
	if !meta.HasContext {
		meta.LastInputTokens = prev.LastInputTokens
		meta.ContextWindow = prev.ContextWindow
		meta.ContextSource = prev.ContextSource
	}
	if !meta.HasHealth {
		meta.HealthKnown = prev.HealthKnown
		meta.HealthToolCalls = prev.HealthToolCalls
		meta.HealthReadCalls = prev.HealthReadCalls
		meta.HealthEditCalls = prev.HealthEditCalls
		meta.HealthEditsWithoutRead = prev.HealthEditsWithoutRead
		meta.HealthFirstTestAt = prev.HealthFirstTestAt
		meta.HealthRetries = prev.HealthRetries
		meta.HealthReads = prev.HealthReads
	}
	meta.HasContext = false
	meta.HasHealth = false
	m.metas[meta.SessionID] = meta
	return nil
}

func (m *memStore) UsageSessions(context.Context) ([]domain.UsageSessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.UsageSessionMeta, 0, len(m.metas))
	for _, meta := range m.metas {
		out = append(out, meta)
	}
	return out, nil
}

func (m *memStore) UsageBuckets(_ context.Context, sessionIDs []string, since time.Time) ([]domain.UsageBucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sessionIDs != nil && len(sessionIDs) == 0 {
		return nil, nil
	}
	allow := map[string]struct{}{}
	for _, id := range sessionIDs {
		allow[id] = struct{}{}
	}
	var out []domain.UsageBucket
	for _, b := range m.bucketRows {
		if sessionIDs != nil {
			if _, ok := allow[b.SessionID]; !ok {
				continue
			}
		}
		if !since.IsZero() && b.Hour.Before(since) {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}
