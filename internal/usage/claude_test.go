package usage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	claudeFixture         = "testdata/claude/00000000-0000-4000-8000-000000000001.jsonl"
	claudeSubagentFixture = "testdata/claude/00000000-0000-4000-8000-000000000001/subagents/agent-afixture.jsonl"
)

func TestParseClaudeFixture(t *testing.T) {
	body := readFixture(t, claudeFixture)
	recs, consumed, err := ParseClaude(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(body)) {
		t.Fatalf("consumed = %d, want %d", consumed, len(body))
	}
	if len(recs) != 2 {
		t.Fatalf("len = %d, want 2", len(recs))
	}
	if recs[0].ID != "msg_fixture_A" || recs[1].ID != "msg_fixture_B" {
		t.Fatalf("ids = %s, %s", recs[0].ID, recs[1].ID)
	}
	if recs[0].Model != "claude-opus-5-5" || recs[1].Model != "claude-opus-5-5" {
		t.Fatalf("models = %s, %s", recs[0].Model, recs[1].Model)
	}
	wantAt := mustParseTime(t, "2026-10-21T07:25:15.630Z")
	if !recs[0].At.Equal(wantAt) {
		t.Fatalf("At = %s, want %s", recs[0].At, wantAt)
	}
	want := domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite5m: 0, CacheWrite1h: 36005}
	if got := sumTokens(recs); got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
}

func TestParseClaudeDedupesContentBlocks(t *testing.T) {
	body := readFixture(t, claudeFixture)
	var lines [][]byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.Contains(line, []byte("msg_fixture_A")) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("fixture A lines = %d, want 2", len(lines))
	}
	input := append(append(append([]byte{}, lines[0]...), '\n'), lines[1]...)
	input = append(input, '\n')
	recs, _, err := ParseClaude(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1", len(recs))
	}
	if recs[0].ID != "msg_fixture_A" {
		t.Fatalf("id = %s", recs[0].ID)
	}
	wantAt := mustParseTime(t, "2026-10-21T07:25:15.630Z")
	if !recs[0].At.Equal(wantAt) {
		t.Fatalf("At = %s, want first line %s", recs[0].At, wantAt)
	}
	want := domain.TokenCounts{Input: 2, Output: 214, CacheRead: 25372, CacheWrite5m: 0, CacheWrite1h: 35052}
	if recs[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", recs[0].Tokens, want)
	}
}

func TestParseClaudeSkipsSynthetic(t *testing.T) {
	recs, _, err := ParseClaude(bytes.NewReader(readFixture(t, claudeFixture)))
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Model == "<synthetic>" || rec.ID == "msg_fixture_SYN" {
			t.Fatalf("synthetic record %+v", rec)
		}
	}
	nonzero := assistantLine("msg_syn", "<synthetic>", `"input_tokens":9,"output_tokens":9`)
	recs, _, err = ParseClaude(strings.NewReader(nonzero))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("nonzero synthetic len = %d, want 0", len(recs))
	}
	zero := assistantLine("msg_zero", "claude-opus-5-5", `"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0`)
	recs, _, err = ParseClaude(strings.NewReader(zero))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("zero usage len = %d, want 0", len(recs))
	}
}

func TestParseClaudeSplitsCacheTTL(t *testing.T) {
	recs, _, err := ParseClaude(bytes.NewReader(readFixture(t, claudeFixture)))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, rec := range recs {
		if rec.ID != "msg_fixture_A" {
			continue
		}
		found = true
		if rec.Tokens.CacheWrite1h != 35052 || rec.Tokens.CacheWrite5m != 0 {
			t.Fatalf("A cache = 5m %d 1h %d", rec.Tokens.CacheWrite5m, rec.Tokens.CacheWrite1h)
		}
	}
	if !found {
		t.Fatal("missing msg_fixture_A")
	}
	legacy := assistantLine("msg_5m", "claude-opus-5-5", `"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":500`)
	recs, _, err = ParseClaude(strings.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("legacy len = %d, want 1", len(recs))
	}
	if recs[0].Tokens.CacheWrite5m != 500 || recs[0].Tokens.CacheWrite1h != 0 {
		t.Fatalf("legacy cache = %+v", recs[0].Tokens)
	}
	split := assistantLine("msg_split", "claude-opus-5-5", `"cache_creation_input_tokens":999,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":11}`)
	recs, _, err = ParseClaude(strings.NewReader(split))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("split len = %d, want 1", len(recs))
	}
	if recs[0].Tokens.CacheWrite5m != 7 || recs[0].Tokens.CacheWrite1h != 11 {
		t.Fatalf("split cache = %+v, want 5m 7 1h 11", recs[0].Tokens)
	}
}

func TestContextTokensClaudeUsesLastAssistant(t *testing.T) {
	first := assistantLine("msg_1", "claude-opus-5-5", `"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":100,"output_tokens":1`)
	second := assistantLine("msg_2", "claude-opus-5-5", `"input_tokens":20,"cache_read_input_tokens":7,"cache_creation_input_tokens":900,"output_tokens":1`)
	synthetic := assistantLine("msg_syn", "<synthetic>", `"input_tokens":500,"cache_read_input_tokens":500,"output_tokens":1`)
	got, ok, err := ClaudeContextTokens(strings.NewReader(first + second + synthetic))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != 27 {
		t.Fatalf("tokens = %d ok=%v, want 27 from the later assistant (input + cache read, not cache creation)", got, ok)
	}
}

func TestParseClaudeFastSpeed(t *testing.T) {
	fast := assistantLine("msg_fast", "claude-opus-5-5", `"input_tokens":1,"output_tokens":2,"speed":"fast"`)
	recs, _, err := ParseClaude(strings.NewReader(fast))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Model != "claude-opus-5-5@fast" {
		t.Fatalf("fast = %+v", recs)
	}
	for _, speed := range []string{"", "standard"} {
		usage := `"input_tokens":1,"output_tokens":2`
		if speed != "" {
			usage += `,"speed":"` + speed + `"`
		}
		line := assistantLine("msg_plain", "claude-opus-5-5", usage)
		recs, _, err = ParseClaude(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].Model != "claude-opus-5-5" {
			t.Fatalf("speed %q model = %+v", speed, recs)
		}
	}
}

func TestParseClaudeStopsAtPartialLine(t *testing.T) {
	body := readFixture(t, claudeFixture)
	partial := append(append([]byte{}, body...), []byte(`{"type":"assistant"`)...)
	recs, consumed, err := ParseClaude(bytes.NewReader(partial))
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(body)) {
		t.Fatalf("consumed = %d, want %d", consumed, len(body))
	}
	if len(recs) != 2 || recs[0].ID != "msg_fixture_A" || recs[1].ID != "msg_fixture_B" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestParseClaudeSkipsBadJSON(t *testing.T) {
	var b strings.Builder
	b.WriteString(assistantLine("msg_ok_1", "claude-opus-5-5", `"input_tokens":1,"output_tokens":1`))
	b.WriteString("{\"type\":\"assistant\",\"usage\":\n")
	b.WriteString(assistantLine("msg_ok_2", "claude-opus-5-5", `"input_tokens":2,"output_tokens":2`))
	recs, _, err := ParseClaude(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].ID != "msg_ok_1" || recs[1].ID != "msg_ok_2" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestParseClaudeSubagentFixture(t *testing.T) {
	body := readFixture(t, claudeSubagentFixture)
	recs, consumed, err := ParseClaude(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(body)) {
		t.Fatalf("consumed = %d, want %d", consumed, len(body))
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1", len(recs))
	}
	if recs[0].ID != "msg_fixture_SUB" || recs[0].Model != "claude-sonnet-5" {
		t.Fatalf("record = %+v", recs[0])
	}
	want := domain.TokenCounts{Input: 3, Output: 50, CacheRead: 2000, CacheWrite5m: 1000, CacheWrite1h: 0}
	if recs[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", recs[0].Tokens, want)
	}
}

func TestClaudeFingerprint(t *testing.T) {
	head := readFixture(t, claudeFixture)
	const wantUUID = "uuid:00000000-0000-4000-8000-000000000101"
	if got := ClaudeFingerprint(head); got != wantUUID {
		t.Fatalf("fixture head = %q, want %q", got, wantUUID)
	}
	plain := []byte("no uuid here\n")
	wantHash := "sha256:" + hex.EncodeToString(sumLine(plain[:len(plain)-1]))
	if got := ClaudeFingerprint(plain); got != wantHash {
		t.Fatalf("no uuid = %q, want %q", got, wantHash)
	}
	first, _, _ := bytes.Cut(head, []byte("\n"))
	first = append(first, '\n')
	wantLeaf := "sha256:" + hex.EncodeToString(sumLine(bytes.TrimSuffix(first, []byte("\n"))))
	if got := ClaudeFingerprint(first); got != wantLeaf {
		t.Fatalf("leafUuid line = %q, want %q", got, wantLeaf)
	}
	if got := ClaudeFingerprint(nil); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := ClaudeFingerprint([]byte(`{"uuid":"00000000-0000-4000-8000-000000000101"}`)); got != "" {
		t.Fatalf("partial = %q", got)
	}
}

func TestReadLinesAcceptsLineLongerThanBuffer(t *testing.T) {
	long := bytes.Repeat([]byte("y"), 64*1024)
	var input []byte
	input = append(input, long...)
	input = append(input, '\n')
	input = append(input, []byte("second\n")...)
	input = append(input, []byte("partial")...)
	var got [][]byte
	consumed, err := readLines(bytes.NewReader(input), func(line []byte) error {
		got = append(got, append([]byte(nil), line...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantConsumed := int64(len(long) + 1 + len("second\n"))
	if consumed != wantConsumed {
		t.Fatalf("consumed = %d, want %d", consumed, wantConsumed)
	}
	if len(got) != 2 || !bytes.Equal(got[0], long) || string(got[1]) != "second" {
		t.Fatalf("lines = %d", len(got))
	}
}

func TestParseClaudeReturnsReaderError(t *testing.T) {
	line := assistantLine("msg_ok", "claude-opus-5-5", `"input_tokens":1,"output_tokens":1`)
	recs, consumed, err := ParseClaude(io.MultiReader(strings.NewReader(line), readerError{}))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
	if consumed != int64(len(line)) || len(recs) != 1 || recs[0].ID != "msg_ok" {
		t.Fatalf("consumed %d records %+v", consumed, recs)
	}
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func sumTokens(recs []domain.UsageRecord) domain.TokenCounts {
	var sum domain.TokenCounts
	for _, rec := range recs {
		sum = sum.Add(rec.Tokens)
	}
	return sum
}

func assistantLine(id, model, usage string) string {
	return `{"type":"assistant","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"` + id + `","model":"` + model + `","usage":{` + usage + `}}}` + "\n"
}

func sumLine(line []byte) []byte {
	sum := sha256.Sum256(line)
	return sum[:]
}

type readerError struct{}

func (readerError) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
