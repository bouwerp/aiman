package usage

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

const codexFixture = "testdata/codex/rollout-fixture.jsonl"

func TestParseCodexFixture(t *testing.T) {
	body := readFixture(t, codexFixture)
	var st CodexState
	recs, consumed, err := ParseCodex(bytes.NewReader(body), &st)
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(body)) {
		t.Fatalf("consumed = %d, want %d", consumed, len(body))
	}
	if len(recs) != 3 {
		t.Fatalf("len = %d, want 3", len(recs))
	}
	wantTerra := domain.TokenCounts{Input: 12696, Output: 433, CacheRead: 29184}
	wantSol := domain.TokenCounts{Input: 3000, Output: 300, CacheRead: 20000}
	if got := sumModel(recs, "gpt-5.6-terra"); got != wantTerra {
		t.Fatalf("terra = %+v, want %+v", got, wantTerra)
	}
	if got := sumModel(recs, "gpt-5.6-sol"); got != wantSol {
		t.Fatalf("sol = %+v, want %+v", got, wantSol)
	}
	if recs[0].Model != "gpt-5.6-terra" || recs[1].Model != "gpt-5.6-terra" || recs[2].Model != "gpt-5.6-sol" {
		t.Fatalf("models = %s, %s, %s", recs[0].Model, recs[1].Model, recs[2].Model)
	}
	if recs[0].At.UTC().Hour() != 6 || recs[1].At.UTC().Hour() != 6 || recs[2].At.UTC().Hour() != 7 {
		t.Fatalf("hours = %d %d %d", recs[0].At.UTC().Hour(), recs[1].At.UTC().Hour(), recs[2].At.UTC().Hour())
	}
	wantAt := []string{
		"2026-10-22T06:08:08.910Z",
		"2026-10-22T06:08:14.492Z",
		"2026-10-22T07:10:09.000Z",
	}
	for i, value := range wantAt {
		if recs[i].ID != "" {
			t.Fatalf("id %d = %q", i, recs[i].ID)
		}
		if !recs[i].At.Equal(mustParseTime(t, value)) {
			t.Fatalf("At %d = %s, want %s", i, recs[i].At, value)
		}
	}
	if st.Model != "gpt-5.6-sol" {
		t.Fatalf("model = %s", st.Model)
	}
	wantLast := CodexTotals{Input: 64880, Cached: 49184, CacheWrite: 0, Output: 733}
	if st.Last != wantLast {
		t.Fatalf("last = %+v, want %+v", st.Last, wantLast)
	}
}

func TestParseCodexSubtractsCached(t *testing.T) {
	var st CodexState
	recs, _, err := ParseCodex(bytes.NewReader(readFixture(t, codexFixture)), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	want := domain.TokenCounts{Input: 8284, Output: 246, CacheRead: 11008}
	if recs[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", recs[0].Tokens, want)
	}
}

func TestParseCodexSkipsNonIncreasing(t *testing.T) {
	body := readFixture(t, codexFixture)
	var st CodexState
	if _, _, err := ParseCodex(bytes.NewReader(body), &st); err != nil {
		t.Fatal(err)
	}
	before := st
	recs, _, err := ParseCodex(bytes.NewReader(body), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("len = %d, want 0", len(recs))
	}
	if st != before {
		t.Fatalf("state = %+v, want %+v", st, before)
	}
}

func TestParseCodexClampsNegativeField(t *testing.T) {
	st := CodexState{Last: CodexTotals{Input: 100, Cached: 90, Output: 10}}
	line := codexCountLine("2026-10-22T06:08:08.910Z", 150, 80, 0, 20, 170)
	recs, _, err := ParseCodex(strings.NewReader(line), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("len = %d, want 1", len(recs))
	}
	want := domain.TokenCounts{Input: 50, Output: 10, CacheRead: 0}
	if recs[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", recs[0].Tokens, want)
	}
	wantLast := CodexTotals{Input: 150, Cached: 80, Output: 20}
	if st.Last != wantLast {
		t.Fatalf("last = %+v, want %+v", st.Last, wantLast)
	}
}

func TestParseCodexNoTurnContext(t *testing.T) {
	if CodexUnknownModel != "codex-unknown" {
		t.Fatalf("CodexUnknownModel = %s", CodexUnknownModel)
	}
	var st CodexState
	line := codexCountLine("2026-10-22T06:08:08.910Z", 10, 4, 0, 3, 13)
	recs, _, err := ParseCodex(strings.NewReader(line), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Model != "codex-unknown" {
		t.Fatalf("records = %+v", recs)
	}
	if st.Model != "" {
		t.Fatalf("model = %q", st.Model)
	}
}

func TestParseCodexNilState(t *testing.T) {
	body := readFixture(t, codexFixture)
	recs, consumed, err := ParseCodex(bytes.NewReader(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(body)) || len(recs) != 3 {
		t.Fatalf("consumed %d records %d", consumed, len(recs))
	}
}

func TestParseCodexUsesFieldSumWhenTotalIsZero(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-10-22T06:08:08.910Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":3,"total_tokens":0}}}}` + "\n",
		`{"timestamp":"2026-10-22T06:08:08.910Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":4,"output_tokens":3}}}}` + "\n",
	}
	want := domain.TokenCounts{Input: 6, Output: 3, CacheRead: 4}
	wantLast := CodexTotals{Input: 10, Cached: 4, Output: 3}
	for _, line := range lines {
		var st CodexState
		recs, _, err := ParseCodex(strings.NewReader(line), &st)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 1 || recs[0].Tokens != want || st.Last != wantLast {
			t.Fatalf("line %s records %+v last %+v", line, recs, st.Last)
		}
	}
}

func TestParseCodexEqualTotalKeepsLast(t *testing.T) {
	st := CodexState{Model: "gpt-5.6-terra", Last: CodexTotals{Input: 100, Cached: 40, CacheWrite: 1, Output: 20}}
	before := st
	for _, total := range []int64{120, 11} {
		line := codexCountLine("2026-10-22T08:00:00Z", 500, 10, 0, 80, total)
		recs, _, err := ParseCodex(strings.NewReader(line), &st)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 0 || st != before {
			t.Fatalf("total %d records %+v state %+v", total, recs, st)
		}
	}
}

func TestParseCodexTotalTokensGatesFields(t *testing.T) {
	st := CodexState{Last: CodexTotals{Input: 100, Output: 20}}
	line := codexCountLine("2026-10-22T08:00:00Z", 90, 0, 0, 10, 121)
	recs, _, err := ParseCodex(strings.NewReader(line), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || !recs[0].Tokens.IsZero() {
		t.Fatalf("records = %+v", recs)
	}
	wantLast := CodexTotals{Input: 90, Output: 10}
	if st.Last != wantLast {
		t.Fatalf("last = %+v, want %+v", st.Last, wantLast)
	}
}

func TestParseCodexFloorsUncachedInput(t *testing.T) {
	var st CodexState
	line := codexCountLine("2026-10-22T06:08:08.910Z", 100, 20, 30, 7, 107)
	recs, _, err := ParseCodex(strings.NewReader(line), &st)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.TokenCounts{Input: 50, Output: 7, CacheRead: 20, CacheWrite5m: 30}
	if len(recs) != 1 || recs[0].Tokens != want {
		t.Fatalf("first = %+v", recs)
	}
	next := codexCountLine("2026-10-22T06:08:09.910Z", 130, 80, 50, 8, 138)
	recs, _, err = ParseCodex(strings.NewReader(next), &st)
	if err != nil {
		t.Fatal(err)
	}
	want = domain.TokenCounts{Input: 0, Output: 1, CacheRead: 60, CacheWrite5m: 20}
	if len(recs) != 1 || recs[0].Tokens != want {
		t.Fatalf("second = %+v, want %+v", recs, want)
	}
}

func TestParseCodexSkipsNullUsage(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-22T06:08:02.100Z","type":"event_msg","payload":{"type":"token_count","info":null}}` + "\n")
	b.WriteString(`{"timestamp":"2026-10-22T06:08:02.200Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":null}}}` + "\n")
	b.WriteString(codexCountLine("2026-10-22T06:08:08.910Z", 5, 1, 0, 2, 7))
	var st CodexState
	recs, _, err := ParseCodex(strings.NewReader(b.String()), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Tokens.Input != 4 || recs[0].Tokens.Output != 2 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestParseCodexSkipsBadJSON(t *testing.T) {
	var b strings.Builder
	b.WriteString(codexCountLine("2026-10-22T06:08:08.910Z", 5, 0, 0, 1, 6))
	b.WriteString("{not-json token_count\n")
	b.WriteString(codexCountLine("2026-10-22T06:08:09.910Z", 8, 0, 0, 2, 10))
	var st CodexState
	recs, _, err := ParseCodex(strings.NewReader(b.String()), &st)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Tokens.Input != 5 || recs[1].Tokens.Input != 3 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestParseCodexStopsAtPartialLine(t *testing.T) {
	body := readFixture(t, codexFixture)
	partial := append(append([]byte{}, body...), []byte(`{"type":"token_count"`)...)
	var st CodexState
	recs, consumed, err := ParseCodex(bytes.NewReader(partial), &st)
	if err != nil {
		t.Fatal(err)
	}
	if consumed != int64(len(body)) || len(recs) != 3 {
		t.Fatalf("consumed %d records %d", consumed, len(recs))
	}
	if st.Last != (CodexTotals{Input: 64880, Cached: 49184, Output: 733}) {
		t.Fatalf("last = %+v", st.Last)
	}
}

func TestParseCodexReturnsReaderError(t *testing.T) {
	line := codexCountLine("2026-10-22T06:08:08.910Z", 5, 0, 0, 1, 6)
	recs, consumed, err := ParseCodex(io.MultiReader(strings.NewReader(line), readerError{}), &CodexState{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v", err)
	}
	if consumed != int64(len(line)) || len(recs) != 1 || recs[0].Tokens.Input != 5 {
		t.Fatalf("consumed %d records %+v", consumed, recs)
	}
}

func TestCodexFingerprint(t *testing.T) {
	head := readFixture(t, codexFixture)
	const wantThread = "thread:00000000-0000-7000-8000-0000000000c1"
	if got := CodexFingerprint(head); got != wantThread {
		t.Fatalf("fixture head = %q, want %q", got, wantThread)
	}
	plain := []byte("no thread here\n")
	wantHash := "sha256:" + hex.EncodeToString(sumLine(plain[:len(plain)-1]))
	if got := CodexFingerprint(plain); got != wantHash {
		t.Fatalf("no meta = %q, want %q", got, wantHash)
	}
	later := []byte("{\"type\":\"event_msg\"}\n{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread-2\"}}\n")
	if got := CodexFingerprint(later); got != "thread:thread-2" {
		t.Fatalf("later meta = %q", got)
	}
	emptyID := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"\"}}\n")
	wantEmpty := "sha256:" + hex.EncodeToString(sumLine(bytes.TrimSuffix(emptyID, []byte("\n"))))
	if got := CodexFingerprint(emptyID); got != wantEmpty {
		t.Fatalf("empty id = %q, want %q", got, wantEmpty)
	}
	if got := CodexFingerprint(nil); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := CodexFingerprint([]byte(`{"type":"session_meta","payload":{"id":"abc"}}`)); got != "" {
		t.Fatalf("partial = %q", got)
	}
}

func sumModel(recs []domain.UsageRecord, model string) domain.TokenCounts {
	var matched []domain.UsageRecord
	for _, rec := range recs {
		if rec.Model == model {
			matched = append(matched, rec)
		}
	}
	return sumTokens(matched)
}

func codexCountLine(ts string, in, cached, write, out, total int64) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}}`+"\n",
		ts, in, cached, write, out, total,
	)
}
