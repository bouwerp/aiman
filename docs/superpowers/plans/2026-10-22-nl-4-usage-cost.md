# W3 Usage and Cost Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve parses Claude and Codex transcripts incrementally, keeps per-session token totals in its SQLite DB, prices them from a config table with built-in defaults, and exposes them through `usage.get`, `aiman usage`, a `usage` Hub event, the detail meta line (`~$cost · cache %`) and a `$` usage view.

**Architecture:** A new pure package `internal/usage` holds the two transcript parsers, the price table, the rollup math and a `Tracker` that serve runs in one goroutine. The tracker reads sessions from serve's repo, follows each session's transcript files with a stored byte offset and fingerprint, and writes deduplicated per-session/hour/model token buckets into four new tables in serve's SQLite DB (serve is the only writer). Costs are computed at read time, so a price change applies retroactively. The TUI reads everything over SSH with nl-1's `usecase.ServeRPC(…, "usage.get", …)` (`aiman rpc`); `aiman usage` is the human/agent CLI.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, Bubble Tea v2 / Lip Gloss v2.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (sections "Constraints", "Noise budget", "Shared interfaces", "W3. Usage and cost"). Depends on nl-1 (`docs/superpowers/plans/2026-10-22-nl-1-sessions-todos-steering.md`: Hub, `session.register`, `aiman rpc`/`usecase.ServeRPC`) and nl-2 (`docs/superpowers/plans/2026-10-22-nl-2-dashboard-a1.md`: `detailMetaExtras{…, Tokens}`, `m.metaExtras`, `renderMetaLine`), so it lands after nl-2.

## Global Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only (`charm.land/...`). Never import v1 charmbracelet packages.
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- Laptop reaches remotes only over SSH; laptop SSH calls time out at 30 s, so `usage.get` must answer well under that (it reads the DB only; parsing happens in the tracker goroutine).
- Both terminal backends (tmux, pty) must work: usage depends only on `domain.Session.AgentSessionPath`, never on the backend.
- SQLite is `modernc.org/sqlite`. Serve is the only writer of usage state; other processes read through the socket.
- Import graph: `internal/gateway` → `internal/server` → `internal/usecase`. `internal/usage` imports only `internal/domain` and the standard library. `internal/server` may import `internal/usage`.
- Costs are labelled `~$` everywhere they are shown. A model without a confirmed price (every Codex model until the user sets `usage.prices.<id>`) never shows `~$`: its tokens are shown instead. Placeholders never produce a cost.
- Noise budget: cost and cache % on the detail meta line only (never in the header or rows); `$` opens the usage view. No toasts for usage.
- Tests that need key events use `pressKey`/`pressRune` from `internal/ui/keystrokes_test.go`.
- Source comments describe the current code (no changelog narration).
- Lint: `.golangci.yml` exists but is loose (`gocyclo` 50, no `funlen`). Keep new functions short anyway (aim for cyclomatic ≤ 10, ≤ 60 lines); the parsers are the risk.

## Transcript formats (confirmed on this host, 2026-10-22)

Claude Code 2.1.306 (`~/.claude/projects/<proj>/<session-uuid>.jsonl`, 443 files sampled):
- Usage lives on `type:"assistant"` records at `message.usage` with `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`, and `cache_creation.{ephemeral_5m_input_tokens,ephemeral_1h_input_tokens}`; model at `message.model`; API message id at `message.id`; `message.usage.speed` is `"standard"` or `"fast"`.
- **One API message is written as one line per content block, each repeating the same `message.id` and identical `usage`** (sample file: 211 ids on 1 line, 183 on 2, 103 on 3, 12 on 4). Counting lines double counts.
- `message.model == "<synthetic>"` records carry zero usage.
- Line 1 is usually `type:"last-prompt"` or `queue-operation` with no `uuid`; the first record with a `uuid` is the stable identity.
- Subagents write separate files: `<proj>/<session-uuid>/subagents/agent-<id>.jsonl`, same record shape. The hook-reported `transcript_path` points at the main file only.
- Claude Code itself records `type:"cost-state"` with `modelUsage[*].costUSD`; recomputing those with the default price table below reproduces 142 of 188 sampled (model, totals) rows exactly; the rest mix 5m/1h writes in one aggregate, which `cost-state` does not split.

Codex CLI 0.150–0.163 (`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`, 373 files):
- Line 1 is `type:"session_meta"`, `payload.id` = thread id.
- `type:"turn_context"` has `payload.model` (`gpt-5.6-terra|sol|luna`, `codex-auto-review`); 11 files switch model mid-file.
- `type:"event_msg"`, `payload.type:"token_count"`, `payload.info` is null or has cumulative `total_token_usage` and per-call `last_token_usage`, each `{input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens, total_tokens}`. `input_tokens` **includes** `cached_input_tokens`; `output_tokens` includes reasoning (`total = input + output`). The same totals are often emitted twice in a row. `cache_write_input_tokens` was 0 in every sampled event.
- Newer versions also write `type:"token_usage_record"` with `response_id`; 0.150–0.162 files often lack it, so this plan does not use it.
- Both agents' transcript paths reach serve today through hook reports (`~/.aiman/native-sessions/*` sidecars show `.claude/projects/...jsonl` and `.codex/sessions/...jsonl` paths).

Redacted fixtures (structure only, synthetic ids, content replaced by `[redacted]`, real token numbers) are already committed in the working tree:
- `internal/usage/testdata/claude/00000000-0000-4000-8000-000000000001.jsonl`: `last-prompt`, `queue-operation`, `user` (uuid `…000000000101`), two lines of `msg_fixture_A`, one `msg_fixture_B`, one `<synthetic>` `msg_fixture_SYN`. All `claude-opus-5-5`, all in hour `2026-10-21T07` UTC.
- `internal/usage/testdata/claude/00000000-0000-4000-8000-000000000001/subagents/agent-afixture.jsonl`: one `claude-sonnet-5` message `msg_fixture_SUB` with a 5m cache write.
- `internal/usage/testdata/codex/rollout-fixture.jsonl`: thread `00000000-0000-7000-8000-0000000000c1`; `gpt-5.6-terra` turn, null `info`, two calls, a repeated total, a `token_usage_record`, then a switch to `gpt-5.6-sol` and one more call.

Expected values used by the tests below:

| Fixture | Model | Input (uncached) | Output | CacheRead | CacheWrite5m | CacheWrite1h |
|---|---|---|---|---|---|---|
| Claude main | `claude-opus-5-5` | 4 | 325 | 97159 | 0 | 36005 |
| Claude subagent | `claude-sonnet-5` | 3 | 50 | 2000 | 1000 | 0 |
| Codex | `gpt-5.6-terra` (hour `2026-10-22T06`) | 12696 | 433 | 29184 | 0 | 0 |
| Codex | `gpt-5.6-sol` (hour `2026-10-22T07`) | 3000 | 300 | 20000 | 0 | 0 |

Costs with default prices: Claude main `0.3139878`, subagent `0.003406`. Combined Claude cache hit ratio `99159 / 136175 = 0.7281946963744116`.

## Review Focus

1. **Content-block repeats.** One Claude API call on 2–4 lines must count once; a reader expects the same cost Claude Code shows. Pinned by `TestParseClaudeDedupesContentBlocks` (Task 3) and `TestTrackerCountsMessageOnceAcrossScans` (Task 6, the repeat straddles two scans).
2. **History rewritten or copied.** `/compact`, `--resume` or a rewrite that changes the first uuid or shrinks the file must not double count. Pinned by `TestTrackerRewriteDoesNotDoubleCount` (Task 6).
3. **Scan during a write.** A half-written last line must not be consumed or lost. Pinned by `TestParseClaudeStopsAtPartialLine` (Task 3) and `TestTrackerPicksUpCompletedLine` (Task 6).
4. **Forgotten or archived sessions.** Their spend must stay in per-ticket and per-day rollups after the session row is deleted. Pinned by `TestUsageSessionsSurviveSessionDelete` (Task 5) and `TestReportByIssueKeepsForgottenSession` (Task 7).
5. **Unpriced models.** Codex models (placeholders) and unknown Claude ids show tokens, never `~$` and never `$0.00`; a mixed total shows the priced part with `+`. Pinned by `TestCostSkipsUnconfirmedPrice` (Task 1), `TestReportMarksPartial` (Task 7), `TestFormatUsageCost` (Task 9), `TestUsageMetaUnpricedShowsTokens` (Task 10).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/domain/usage.go` (create) | `TokenCounts`, `UsageRecord`, `UsageBucket`, `UsageCursor`, `UsageSessionMeta` |
| `internal/usage/price.go` (create) | `Price`, `PriceTable`, `DefaultPrices`, lookup and cost |
| `internal/usage/claude.go` (create) | Claude JSONL parser and fingerprint |
| `internal/usage/codex.go` (create) | Codex JSONL parser, `CodexState`, fingerprint |
| `internal/usage/lines.go` (create) | complete-line reader shared by both parsers |
| `internal/usage/tracker.go` (create) | `Tracker`: file discovery, offsets, resets, scan loop |
| `internal/usage/report.go` (create) | `Query`, `Summary`, `Row`, `Report`, `Reporter` |
| `internal/infra/sqlite/repository_usage.go` (create) | usage tables and store methods |
| `internal/infra/config/config.go` (modify) | `UsageConfig`, `ModelPrice`; in `Config` and `ServeConfig` |
| `internal/server/handlers_usage.go` (create) | `usage.get`, `SetUsage`, `PublishUsage` |
| `internal/server/server.go` (modify) | dispatch `usage.get` |
| `internal/server/handlers_session.go` (modify) | kick the tracker on agent reports |
| `cmd/aiman/serve.go` (modify) | wire tracker and reporter |
| `cmd/aiman/usage_cmd.go` (create) | `aiman usage` |
| `cmd/aiman/main.go` (modify) | socket-only dispatch of `usage` |
| `internal/ui/usage_fetch.go` (create) | per-session fetch, throttling, meta extras |
| `internal/ui/usage_view.go` (create) | `$` view |
| `internal/ui/dashboard.go`, `internal/ui/session_events.go` (modify) | key, view state, selection and event hooks |

---

### Task 1: Token types and price table

**Files:**
- Create: `internal/domain/usage.go`, `internal/usage/price.go`
- Test: `internal/domain/usage_test.go`, `internal/usage/price_test.go`

**Interfaces:**
- Produces:

```go
// internal/domain/usage.go
type TokenCounts struct {
    Input        int64 `json:"input"`          // uncached input only
    Output       int64 `json:"output"`         // includes thinking/reasoning
    CacheRead    int64 `json:"cache_read"`
    CacheWrite5m int64 `json:"cache_write_5m"`
    CacheWrite1h int64 `json:"cache_write_1h"`
}
func (t TokenCounts) Add(o TokenCounts) TokenCounts
func (t TokenCounts) CacheCreation() int64           // 5m + 1h
func (t TokenCounts) IsZero() bool
func (t TokenCounts) CacheHitRatio() (float64, bool) // CacheRead / (Input + CacheRead + CacheCreation); false when denominator is 0

type UsageRecord struct { ID string; Model string; At time.Time; Tokens TokenCounts } // ID "" = never deduplicated
type UsageBucket struct { SessionID string; Hour time.Time /* UTC, truncated */; Model string; Tokens TokenCounts }
type UsageCursor struct {
    SessionID, Path, Fingerprint string
    Offset, Size int64
    ModTime      time.Time
    State        string // parser state, opaque JSON (Codex); "" for Claude
}
type UsageSessionMeta struct { SessionID, Name, Group, IssueKey, AgentName string }

// internal/usage/price.go
type Price struct {
    Input, Output, CacheRead, CacheWrite5m, CacheWrite1h float64 // USD per million tokens
    Confirmed bool // false = placeholder the user must confirm; never used for cost
}
type PriceTable map[string]Price
func DefaultPrices() PriceTable
func (p PriceTable) Merge(over PriceTable) PriceTable // over wins per key; result is a new map
func (p PriceTable) Lookup(model string) (Price, bool) // false for missing or unconfirmed
func (p Price) Cost(t domain.TokenCounts) float64
```

- [ ] **Step 1: Write the failing tests**

`internal/domain/usage_test.go`:
- `TestCacheHitRatio`: `{Input:4, CacheRead:97159, CacheWrite1h:36005}` → `0.7295972005286555`, ok; zero value → ok false.
- `TestTokenCountsAdd`: field-wise sum of two values.

`internal/usage/price_test.go`:
- `TestDefaultPricesMatchPublished` (table): `claude-opus-5-5` = `{4, 20, 0.20, 5, 8, true}`; `claude-sonnet-5` = `{2, 10, 0.20, 2.5, 4, true}`; `claude-haiku-5-5` = `{0.10, 0.50, 0.01, 0.125, 0.20, true}`; `claude-haiku-4-5` = `{1, 5, 0.10, 1.25, 2, true}`.
- `TestLookupPrefix`: `Lookup("claude-haiku-4-5-20251001")` returns the `claude-haiku-4-5` price (longest key that the id starts with followed by `-`); `Lookup("claude-opus-5")` returns `claude-opus-5`, not `claude-opus-5-5`.
- `TestLookupFast`: `Lookup("claude-opus-5-5@fast")` returns `Input 8, Output 40`; `Lookup("claude-sonnet-5@fast")` returns false (no fast entry, and `@fast` never falls back to the standard price).
- `TestCostSkipsUnconfirmedPrice`: `Lookup("gpt-5.6-terra")` returns false although the key exists.
- `TestCostOpusFixture`: `DefaultPrices()["claude-opus-5-5"].Cost({Input:4, Output:325, CacheRead:97159, CacheWrite1h:36005})` within `1e-9` of `0.3139878`.
- `TestMergeOverrides`: merging `{"claude-opus-5-5": {Input: 1, Confirmed: true}}` changes only that key and leaves `DefaultPrices()` unchanged.

- [ ] **Step 2: Run** `go test ./internal/domain/ ./internal/usage/ -run 'CacheHitRatio|TokenCountsAdd|DefaultPrices|Lookup|Cost|Merge'` → FAIL (undefined).

- [ ] **Step 3: Implement.** `DefaultPrices` returns exactly these entries (USD/MTok: input, output, cache read, 5m write, 1h write). Claude rows are confirmed: source is the bundled `claude-api` skill pricing table (cached 2026-10-06; cache writes 1.25×/2× input) and they reproduce Claude Code's own `cost-state` records on this host. Put that source in a comment above the map.

| Key | In | Out | Read | W5m | W1h | Confirmed |
|---|---|---|---|---|---|---|
| `claude-fable-5-1` | 10 | 50 | 0.25 | 12.5 | 20 | true |
| `claude-fable-5` | 10 | 50 | 1.00 | 12.5 | 20 | true |
| `claude-opus-5-5` | 4 | 20 | 0.20 | 5 | 8 | true |
| `claude-opus-5-5@fast` | 8 | 40 | 0.40 | 10 | 16 | true (in/out published; cache columns assume the same 2×, say so in the comment) |
| `claude-opus-5` | 5 | 25 | 0.50 | 6.25 | 10 | true |
| `claude-opus-4-8`, `claude-opus-4-7`, `claude-opus-4-6` | 5 | 25 | 0.50 | 6.25 | 10 | true |
| `claude-sonnet-5-5`, `claude-sonnet-5` | 2 | 10 | 0.20 | 2.5 | 4 | true |
| `claude-sonnet-4-6` | 3 | 15 | 0.30 | 3.75 | 6 | true |
| `claude-haiku-5-5` | 0.10 | 0.50 | 0.01 | 0.125 | 0.20 | true (≤100K-token prompts; the long-prompt tier is not modelled) |
| `claude-haiku-4-5` | 1 | 5 | 0.10 | 1.25 | 2 | true |
| `gpt-5.6-terra`, `gpt-5.6-sol`, `gpt-5.6-luna`, `codex-auto-review` | 0 | 0 | 0 | 0 | 0 | **false: PLACEHOLDER, user must set `usage.prices.<id>`** |

`Lookup`: exact key; else, for ids without `@`, the longest key `k` with `strings.HasPrefix(model, k+"-")`; ids with `@` match only exactly. Return false when not found or `!Confirmed`. `Cost` = Σ tokens × price / 1e6.

- [ ] **Step 4: Run** the Step 2 command → PASS.
- [ ] **Step 5: Commit** `git add internal/domain/usage*.go internal/usage/price*.go && git commit -m "feat(usage): token counts and default price table"`.

---

### Task 2: Price overrides in config

**Files:**
- Modify: `internal/infra/config/config.go:20-31` (`Config`), `:43-49` (`ServeConfig`), `:52-63` (`MarshalServeConfig`)
- Test: `internal/infra/config/config_test.go`

**Interfaces:**
- Produces:

```go
type UsageConfig struct {
    Prices map[string]ModelPrice `yaml:"prices,omitempty"`
}
type ModelPrice struct {
    Input        float64 `yaml:"input"`
    Output       float64 `yaml:"output"`
    CacheRead    float64 `yaml:"cache_read"`
    CacheWrite5m float64 `yaml:"cache_write_5m"`
    CacheWrite1h float64 `yaml:"cache_write_1h"`
}
// Config.Usage and ServeConfig.Usage: `yaml:"usage,omitempty"`
```

- [ ] **Step 1: Write the failing tests**
- `TestUsagePricesRoundTrip`: YAML `usage: {prices: {gpt-5.6-terra: {input: 1.5, output: 12, cache_read: 0.15}}}` loads into `cfg.Usage.Prices["gpt-5.6-terra"].Output == 12`.
- `TestMarshalServeConfigCarriesUsage`: a `Config` with one price → `MarshalServeConfig()` output contains `usage:` and `gpt-5.6-terra:` (serve on the remote reads it).

- [ ] **Step 2: Run** `go test ./internal/infra/config/ -run 'UsagePrices|ServeConfigCarriesUsage'` → FAIL.
- [ ] **Step 3: Implement** the types and add `Usage: c.Usage` to `MarshalServeConfig`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(config): usage price overrides`.

nl-5 later changes the signature to `MarshalServeConfig(host string)`; when it lands it updates this test's call site.

---

### Task 3: Claude transcript parser

**Files:**
- Create: `internal/usage/lines.go`, `internal/usage/claude.go`
- Test: `internal/usage/claude_test.go` (uses the committed fixtures)

**Interfaces:**
- Consumes: Task 1 `domain.UsageRecord`, `domain.TokenCounts`.
- Produces:

```go
// readLines calls fn for each complete '\n'-terminated line (without the '\n') and
// returns the bytes consumed through the last complete line. A trailing partial
// line is not consumed. Lines of any length are accepted.
func readLines(r io.Reader, fn func(line []byte) error) (consumed int64, err error)

func ParseClaude(r io.Reader) (recs []domain.UsageRecord, consumed int64, err error)
func ClaudeFingerprint(head []byte) string // "uuid:<first uuid>", else "sha256:<hex of first line>", "" for empty
```

- [ ] **Step 1: Write the failing tests**
- `TestParseClaudeFixture`: parse the main fixture → 2 records, IDs `msg_fixture_A`, `msg_fixture_B`, model `claude-opus-5-5`; summed tokens equal the "Claude main" row; `consumed` equals the file size; `At` of A is `2026-10-21T07:25:15.630Z`.
- `TestParseClaudeDedupesContentBlocks`: the two `msg_fixture_A` lines yield one record.
- `TestParseClaudeSkipsSynthetic`: no record has model `<synthetic>`.
- `TestParseClaudeSplitsCacheTTL`: record A has `CacheWrite1h 35052`, `CacheWrite5m 0`; a hand-written line whose usage has `cache_creation_input_tokens: 500` and no `cache_creation` object yields `CacheWrite5m 500`.
- `TestParseClaudeFastSpeed`: a line with `"speed":"fast"` yields model `claude-opus-5-5@fast`.
- `TestParseClaudeStopsAtPartialLine`: fixture bytes plus `{"type":"assistant"` (no newline) → same 2 records, `consumed` = fixture size.
- `TestParseClaudeSkipsBadJSON`: a garbage complete line between records is skipped, not an error.
- `TestParseClaudeSubagentFixture`: subagent file → 1 record matching the "Claude subagent" row.
- `TestClaudeFingerprint`: fixture head → `uuid:00000000-0000-4000-8000-000000000101`; a head with no uuid → `sha256:` prefix.

- [ ] **Step 2: Run** `go test ./internal/usage/ -run 'ParseClaude|ClaudeFingerprint'` → FAIL.
- [ ] **Step 3: Implement.** `readLines` uses `bufio.Reader.ReadSlice` and accumulates on `bufio.ErrBufferFull`, so long tool-result lines work. `ParseClaude` only unmarshals lines that contain `"usage"`, into a narrow struct (`type`, `timestamp`, `message.{id,model,usage}`); keeps `type == "assistant"`, skips `<synthetic>` and all-zero usage; dedupes by `message.id` within the call (first wins). `Input = input_tokens`; `Output = output_tokens`; `CacheRead = cache_read_input_tokens`; with a `cache_creation` object use its 5m/1h split, otherwise all of `cache_creation_input_tokens` is 5m. `ClaudeFingerprint` reads complete lines of `head` and returns the first `uuid` field found.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(usage): incremental Claude transcript parser`.

---

### Task 4: Codex transcript parser

**Files:**
- Create: `internal/usage/codex.go`
- Test: `internal/usage/codex_test.go`

**Interfaces:**
- Consumes: `readLines` (Task 3).
- Produces:

```go
type CodexTotals struct { Input, Cached, CacheWrite, Output int64 } // cumulative, as Codex reports them
type CodexState struct {
    Model string      `json:"model"`
    Last  CodexTotals `json:"last"`
}
func ParseCodex(r io.Reader, st *CodexState) (recs []domain.UsageRecord, consumed int64, err error)
func CodexFingerprint(head []byte) string // "thread:<session_meta payload.id>", else "sha256:<hex of first line>"
const CodexUnknownModel = "codex-unknown"
```

- [ ] **Step 1: Write the failing tests**
- `TestParseCodexFixture`: from zero state → 3 records (null info and the repeated total produce none); per-model sums match the two Codex rows; `At` hours are `06` and `07`; final `st.Model == "gpt-5.6-sol"`, `st.Last == CodexTotals{64880, 49184, 0, 733}`.
- `TestParseCodexSubtractsCached`: first record `Input 8284`, `CacheRead 11008`, `Output 246`.
- `TestParseCodexSkipsNonIncreasing`: re-parsing the whole fixture with the final state yields 0 records (a replayed file never double counts).
- `TestParseCodexClampsNegativeField`: state `Last{Input: 100, Cached: 90, Output: 10}` and an event with total `{Input: 150, Cached: 80, Output: 20}` → `CacheRead 0`, `Input 50`, `Output 10`.
- `TestParseCodexNoTurnContext`: a token_count before any turn_context uses `CodexUnknownModel`.
- `TestCodexFingerprint`: fixture head → `thread:00000000-0000-7000-8000-0000000000c1`.

- [ ] **Step 2: Run** `go test ./internal/usage/ -run 'ParseCodex|CodexFingerprint'` → FAIL.
- [ ] **Step 3: Implement.** Only unmarshal lines containing `"turn_context"` or `"token_count"`. `turn_context` sets `st.Model`. A `token_count` with non-null `info.total_token_usage` is skipped unless `total_tokens` exceeds `Last` total (`Last.Input + Last.Output`); otherwise each field delta is `max(0, cur-last)`, the record is `Input = dInput - dCached - dCacheWrite` (floor 0), `CacheRead = dCached`, `CacheWrite5m = dCacheWrite`, `Output = dOutput`, ID `""`, model `st.Model` (or `CodexUnknownModel`), and `st.Last = cur`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(usage): incremental Codex transcript parser`.

---

### Task 5: Usage tables in SQLite

**Files:**
- Create: `internal/infra/sqlite/repository_usage.go`
- Modify: `internal/infra/sqlite/repository.go:183` (call `ensureUsageTables(db)` before the `return &Repository{…}`)
- Test: `internal/infra/sqlite/repository_usage_test.go`

**Interfaces:**
- Consumes: Task 1 domain types.
- Produces (methods on `*sqlite.Repository`, which then satisfies `usage.Store` from Task 6):

```go
func (r *Repository) UsageCursors(ctx context.Context, sessionID string) ([]domain.UsageCursor, error)
// ApplyUsage, in one transaction: for each record with a non-empty ID, INSERT OR IGNORE
// into usage_seen and skip it when the row already existed; add the rest into
// usage_buckets (hour = rec.At UTC truncated); upsert the cursor. Returns whether any
// tokens were added.
func (r *Repository) ApplyUsage(ctx context.Context, cur domain.UsageCursor, recs []domain.UsageRecord) (bool, error)
func (r *Repository) UpsertUsageSession(ctx context.Context, m domain.UsageSessionMeta) error // empty fields keep stored values
func (r *Repository) UsageSessions(ctx context.Context) ([]domain.UsageSessionMeta, error)
func (r *Repository) UsageBuckets(ctx context.Context, sessionIDs []string, since time.Time) ([]domain.UsageBucket, error) // nil ids = all
```

Schema (`byte_offset` because `OFFSET` is reserved):

```sql
CREATE TABLE IF NOT EXISTS usage_cursors (session_id TEXT NOT NULL, path TEXT NOT NULL, byte_offset INTEGER NOT NULL DEFAULT 0, fingerprint TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0, mod_time DATETIME, state_json TEXT NOT NULL DEFAULT '', PRIMARY KEY (session_id, path));
CREATE TABLE IF NOT EXISTS usage_seen (session_id TEXT NOT NULL, record_id TEXT NOT NULL, PRIMARY KEY (session_id, record_id));
CREATE TABLE IF NOT EXISTS usage_buckets (session_id TEXT NOT NULL, hour TEXT NOT NULL, model TEXT NOT NULL, input INTEGER NOT NULL DEFAULT 0, output INTEGER NOT NULL DEFAULT 0, cache_read INTEGER NOT NULL DEFAULT 0, cache_write_5m INTEGER NOT NULL DEFAULT 0, cache_write_1h INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (session_id, hour, model));
CREATE TABLE IF NOT EXISTS usage_sessions (session_id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '', issue_key TEXT NOT NULL DEFAULT '', agent_name TEXT NOT NULL DEFAULT '', updated_at DATETIME);
```

`hour` is stored as `"2006-01-02T15"` UTC.

- [ ] **Step 1: Write the failing tests** (temp-file DB via `NewRepository(filepath.Join(t.TempDir(), "a.db"))`)
- `TestApplyUsageDedupesIDs`: apply records A and B, then A again with a different cursor offset → second call returns `false`, buckets hold A+B once, cursor shows the second offset.
- `TestApplyUsageAddsUnnamedRecords`: two records with ID `""` and equal tokens are both added.
- `TestApplyUsageBucketsByHourAndModel`: records at `06:59` and `07:01` for one model → two buckets; `UsageBuckets(ctx, nil, time.Time{})` returns both.
- `TestUsageBucketsSince`: `since` excludes older hours.
- `TestUsageSessionsSurviveSessionDelete`: upsert meta for `s1` with issue `PROJ-1`, save and `Delete` session `s1` → `UsageSessions` still returns `PROJ-1`.
- `TestUpsertUsageSessionKeepsFields`: a second upsert with empty `IssueKey` keeps `PROJ-1`.

- [ ] **Step 2: Run** `go test ./internal/infra/sqlite/ -run 'ApplyUsage|UsageBuckets|UsageSessions|UpsertUsageSession'` → FAIL.
- [ ] **Step 3: Implement.** `ensureUsageTables(db *sql.DB) error` creates the four tables; `NewRepository` returns its error wrapped. Bucket upsert: `INSERT … ON CONFLICT(session_id, hour, model) DO UPDATE SET input = input + excluded.input, …`.
- [ ] **Step 4: Run** → PASS, plus `go test ./internal/infra/sqlite/` (existing tests).
- [ ] **Step 5: Commit** `feat(sqlite): usage cursors, buckets and seen ids`.

---

### Task 6: Tracker (incremental scan)

**Files:**
- Create: `internal/usage/tracker.go`
- Test: `internal/usage/tracker_test.go` (in-memory fake `Store`; copies fixtures into `t.TempDir()`)

**Interfaces:**
- Consumes: Tasks 3–5 (`ParseClaude`, `ParseCodex`, fingerprints, store methods).
- Produces:

```go
type Store interface { // satisfied by *sqlite.Repository
    UsageCursors(ctx context.Context, sessionID string) ([]domain.UsageCursor, error)
    ApplyUsage(ctx context.Context, cur domain.UsageCursor, recs []domain.UsageRecord) (bool, error)
    UpsertUsageSession(ctx context.Context, m domain.UsageSessionMeta) error
    UsageSessions(ctx context.Context) ([]domain.UsageSessionMeta, error)
    UsageBuckets(ctx context.Context, sessionIDs []string, since time.Time) ([]domain.UsageBucket, error)
}
type SessionLister interface { List(ctx context.Context) ([]domain.Session, error) }

type Kind string
const (KindUnknown Kind = ""; KindClaude Kind = "claude"; KindCodex Kind = "codex")
func KindForPath(path string) Kind // "/.claude/projects/" + ".jsonl" → claude; "/.codex/sessions/" + ".jsonl" → codex

const ScanInterval = 15 * time.Second
const fingerprintHead = 64 << 10 // bytes read from the start of a file for its fingerprint

func NewTracker(store Store, sessions SessionLister, onChange func(sessionID string)) *Tracker
func (t *Tracker) ScanOnce(ctx context.Context) (changed []string, err error)
func (t *Tracker) Run(ctx context.Context, every time.Duration) // ScanOnce now, then on ticker or Kick
func (t *Tracker) Kick()                                        // non-blocking; coalesces
```

- [ ] **Step 1: Write the failing tests**
- `TestTrackerScansClaudeAndSubagents`: session with `AgentSessionPath` = copied main fixture (subagent dir copied alongside) → `changed == ["s1"]`; fake store buckets equal the "Claude main" plus "Claude subagent" rows; `onChange` called once with `s1`; meta upserted with the session's `IssueKey`.
- `TestTrackerSecondScanIsNoop`: second `ScanOnce` without file changes → no `ApplyUsage` call (stat unchanged) and `changed` empty.
- `TestTrackerCountsMessageOnceAcrossScans`: write the first `msg_fixture_A` line only, scan, append the second A line and the B line, scan → A counted once.
- `TestTrackerPicksUpCompletedLine`: append half of a B line, scan (nothing added, offset unchanged), append the rest, scan → B added.
- `TestTrackerRewriteDoesNotDoubleCount`: after a full scan, (a) truncate and rewrite the same bytes, (b) rewrite with the user record's uuid changed plus one new message `msg_fixture_C` → only C is added; the cursor fingerprint is updated and offset equals the new size.
- `TestTrackerCodexContinuesThreadInNewFile`: scan the Codex fixture; add a second file for the same session (path recorded as an older cursor plus the new `AgentSessionPath`) holding the same `session_meta` and one `token_count` with totals `t3 + {Input:1000, Cached:0, Output:10}` → only `Input 1000, Output 10` added (state seeded from the cursor with the same fingerprint).
- `TestTrackerIgnoresUnknownKinds`: a `.grok/sessions/...` path → no cursor, no change.
- `TestTrackerKeepsOldFiles`: after `AgentSessionPath` changes, the old cursor's file still gets scanned if it grows.

- [ ] **Step 2: Run** `go test ./internal/usage/ -run Tracker` → FAIL.
- [ ] **Step 3: Implement.** Per session from `sessions.List`: upsert meta (`Name`, `Group`, `IssueKey`, `AgentName`); file set = stored cursors' paths ∪ `AgentSessionPath` ∪ (Claude only) `filepath.Glob(<dir>/<stem>/subagents/*.jsonl)` of every Claude path. Per file: `os.Stat`; skip when size and mtime equal the cursor; read the fingerprint from the first `fingerprintHead` bytes; reset to offset 0 when `size < offset` or the fingerprint changed. Codex state: the cursor's `State` if not reset, otherwise the stored state with the highest `Last` total among this session's cursors with the same fingerprint, otherwise zero. Seek to the offset, parse, then `ApplyUsage` with the new offset (`offset + consumed`), size, mtime, fingerprint and marshalled state. Collect sessions where `ApplyUsage` returned true; call `onChange` once per session after the scan. A per-file error is logged with `log.Printf` and skipped, so one unreadable transcript does not stop the others.
- [ ] **Step 4: Run** `go test -race ./internal/usage/` → PASS.
- [ ] **Step 5: Commit** `feat(usage): tracker follows transcripts incrementally`.

---

### Task 7: Reports and rollups

**Files:**
- Create: `internal/usage/report.go`
- Test: `internal/usage/report_test.go`

**Interfaces:**
- Consumes: Task 1 prices, Task 6 `Store`.
- Produces:

```go
type Query struct {
    SessionID string // resolved by the caller
    Group     string
    Issue     string
    Day       string // "2006-01-02" in the query's zone
    By        string // "", "session", "issue", "day", "model"
    Days      int    // 0 = all time; otherwise hours within the last N days of the zone
    TZOffsetMinutes int
}
type Summary struct {
    Tokens        domain.TokenCounts `json:"tokens"`
    CostUSD       float64            `json:"cost_usd"`
    Priced        bool               `json:"priced"`  // some tokens had a confirmed price
    Partial       bool               `json:"partial"` // some tokens had none
    CacheHitRatio *float64           `json:"cache_hit_ratio,omitempty"`
}
type Row struct { Key string `json:"key"`; Summary Summary `json:"summary"` }
type Report struct {
    Total    Summary  `json:"total"`
    Rows     []Row    `json:"rows,omitempty"`
    Unpriced []string `json:"unpriced,omitempty"` // sorted model ids
    Known    bool     `json:"known"`              // false: no transcript we can read
}
type Reporter struct { Store Store; Prices PriceTable; Now func() time.Time }
func (r Reporter) Report(ctx context.Context, q Query) (Report, error)
func Summarize(buckets []domain.UsageBucket, prices PriceTable) (Summary, []string)
const NoTicketKey = "(no ticket)"
```

- [ ] **Step 1: Write the failing tests** (fake store from Task 6 tests, seeded with buckets)
- `TestSummarizeClaudeFixture`: the two Claude rows → `CostUSD` within `1e-9` of `0.3173938`, `Priced`, not `Partial`, ratio `0.7281946963744116`.
- `TestReportMarksPartial`: Claude rows plus the Codex rows → `Partial`, `Unpriced == ["gpt-5.6-sol", "gpt-5.6-terra"]`, cost unchanged from the Claude-only total.
- `TestReportOnlyUnpriced`: Codex rows only → `Priced == false`, `CostUSD == 0`.
- `TestReportByIssueKeepsForgottenSession`: meta for `s1` (`PROJ-1`) exists, no live session → row `PROJ-1`; a session with empty issue lands in `NoTicketKey`. Rows sorted by `CostUSD` descending, then key.
- `TestReportByDayUsesOffset`: a bucket at `2026-10-21T23` UTC with `TZOffsetMinutes: 120` lands in day `2026-10-22`; rows sorted by day descending.
- `TestReportDaysWindow`: `Days: 1` with `Now` = `2026-10-22T12:00Z` excludes a bucket from `2026-10-20`.
- `TestReportUnknownSession`: `SessionID` with no cursors and no buckets → `Known == false`.

- [ ] **Step 2: Run** `go test ./internal/usage/ -run 'Summarize|Report'` → FAIL.
- [ ] **Step 3: Implement.** Filter sessions by `Group`/`Issue` through `UsageSessions`; `Known` is true when the filtered set has any bucket or any cursor. `Summarize` prices each bucket by model and adds unpriced models to the list.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(usage): per-session, ticket and day rollups`.

---

### Task 8: `usage.get`, Hub event and serve wiring

**Files:**
- Create: `internal/server/handlers_usage.go`
- Modify: `internal/server/server.go:163-164` (add `case "usage.get": return s.handleUsageGet(ctx, req)` after `context.stats`), `internal/server/server.go:24-44` (fields), `internal/server/handlers_session.go:789-795` (kick after the hook report is saved), `cmd/aiman/serve.go:97` (wiring), `serve.go` `printServeUsage` (one sentence), `cmd/aiman/rpc_cmd.go` + `rpc_cmd_test.go` (allowlist `usage.get`)
- Test: `internal/server/handlers_usage_test.go`

**Interfaces:**
- Consumes: Task 7 `usage.Reporter`, `usage.Query`, `usage.Report`; Task 6 `usage.Tracker`; nl-1 Task 6 `s.hub *eventHub` (`Publish`, `Subscribe`) on the `Server`, and the `usage` event type from "Shared interfaces"; existing `resolveSession` (`handlers_session.go:809`).
- Produces:

```go
type usageReporter interface { Report(ctx context.Context, q usage.Query) (usage.Report, error) }
type usageKicker interface { Kick() }
func (s *Server) SetUsage(r usageReporter, k usageKicker)
func (s *Server) PublishUsage(sessionID string) // s.hub.Publish(SessionEvent{Type: "usage", ID: sessionID}); no-op without a hub
```

Wire params: `session`, `group`, `issue`, `day`, `by`, `days`, `tz_offset_minutes`. Result: `{"type": "usage", "session": "<resolved id or empty>", "usage": <usage.Report>}`.

- [ ] **Step 1: Write the failing tests** (`New` with a fake repo as other handler tests do; `SetUsage` with a fake reporter that records the `Query`)
- `TestUsageGetResolvesSessionName`: repo holds session `{ID:"s1", Name:"fix-login"}`; params `{"session":"fix-login","by":"day","days":7,"tz_offset_minutes":120}` → reporter got `Query{SessionID:"s1", By:"day", Days:7, TZOffsetMinutes:120}`, result `session == "s1"`.
- `TestUsageGetUnknownSession`: `{"session":"nope"}` → error `CodeInvalidParams`, message `unknown session "nope"`.
- `TestUsageGetRejectsBadBy`: `{"by":"week"}` → `CodeInvalidParams`.
- `TestUsageGetWithoutTracker`: no `SetUsage` → error `usage tracking is not running on this host`.
- `TestReportAgentSessionKicksUsage`: a fake kicker counts one `Kick` after `session.report_agent_session` for a known session.
- `TestPublishUsageEvent`: `ch, stop := srv.hub.Subscribe(); defer stop()`; `PublishUsage("s1")` → `<-ch` is `SessionEvent{Type:"usage", ID:"s1"}`.

- [ ] **Step 2: Run** `go test ./internal/server/ -run 'UsageGet|KicksUsage|PublishUsage'` → FAIL.
- [ ] **Step 3: Implement.** In `runServe` after `srv := server.New(…)`: `tracker := usage.NewTracker(db, db, srv.PublishUsage)`, `rep := usage.Reporter{Store: db, Prices: usage.DefaultPrices().Merge(usagePrices(cfg)), Now: time.Now}`, `srv.SetUsage(rep, tracker)`, `go tracker.Run(ctx, usage.ScanInterval)`. Add `func usagePrices(cfg *config.Config) usage.PriceTable` in `serve.go`: every configured entry becomes a `usage.Price` with `Confirmed: true`. `printServeUsage` gains: "serve tracks token usage from Claude and Codex transcripts; `aiman usage` reports it."
- [ ] **Step 4: Run** `go test -race ./internal/server/ ./cmd/aiman/` → PASS.
- [ ] **Step 5: Commit** `feat(serve): usage.get, usage events and transcript tracking`.

---

### Task 9: `aiman usage` CLI

**Files:**
- Create: `cmd/aiman/usage_cmd.go`
- Modify: `internal/usage/report.go` (`FormatUsageCost`)
- Modify: `cmd/aiman/main.go:73-80` (add `case "usage": return runUsage(parsed.Rest[1:], os.Stdout)` to the socket-only switch), unknown-command help list in `main.go` (add `usage`)
- Test: `cmd/aiman/usage_cmd_test.go`, `cmd/aiman/socket_only_dispatch_test.go:33-37` (add `{"usage"}`)

**Interfaces:**
- Consumes: `socketPath`, `takeFlags`, `writeJSON`, `writeCLIError` (`cmd/aiman/session_cmd.go`); `server.Call`; `usage.Report`.
- Produces:

```go
func runUsage(args []string, out io.Writer) error
func usageParams(flags map[string]string) (map[string]any, error) // --session --group --issue --day --by --days --tz-offset-minutes
func formatUsageReport(w io.Writer, rep usage.Report) error
// internal/usage/report.go, shared with the TUI (Tasks 10, 11):
func FormatUsageCost(s Summary) string // "~$1.23"; "~$<0.01"; ≥100 → "~$123"; Partial → suffix "+"; !Priced → "" (callers show tokens)
func FormatTokens(n int64) string      // "12.7k", "1.3M"; shared with the TUI
```

Flags: `--json` prints the raw result with `writeJSON`; otherwise a table. `--tz-offset-minutes` defaults to the caller's local offset (`time.Now().Zone()`). No filter means every session on the host.

- [ ] **Step 1: Write the failing tests**
- `TestUsageParams`: `{"session":"s1","by":"issue","days":"30"}` → `{"session":"s1","by":"issue","days":30,"tz_offset_minutes":<local>}`; `days: "x"` → error.
- `TestFormatUsageCost` (in `internal/usage/report_test.go`): `{CostUSD:1.234, Priced:true}` → `~$1.23`; `0.004` → `~$<0.01`; `123.4` → `~$123`; `Partial` → `~$1.23+`; `Priced:false` → `""`.
- `TestFormatUsageReportTable`: a report with rows `PROJ-1` (cost `1.234`, ratio `0.81`) and one partial row → header `KEY  INPUT  OUTPUT  CACHE_READ  CACHE_WRITE  ~COST  CACHE`; `PROJ-1` line has `~$1.23` and `81%`; the partial row's cost ends with `+`; an unpriced-only row shows `-` in the cost column and its token columns; a footer line `unpriced: gpt-5.6-terra (tokens only; set usage.prices in ~/.aiman/config.yaml to see ~$)` when `Unpriced` is non-empty.
- `TestSocketOnlyCommandsSkipTheDatabase` gains `{"usage"}` and must stay green.

- [ ] **Step 2: Run** `go test ./internal/usage/ ./cmd/aiman/ -run 'FormatUsageCost|UsageParams|FormatUsageReport|SocketOnly'` → FAIL.
- [ ] **Step 3: Implement.** Token columns use `k`/`M` suffixes (`12.7k`, `1.3M`); the cost column uses `usage.FormatUsageCost`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(cli): aiman usage`.

---

### Task 10: Detail meta line fields

**Files:**
- Create: `internal/ui/usage_fetch.go`
- Modify: `internal/ui/dashboard.go` (`Model` fields near `:402-403`; selection change at `:4453-4469`, next to `fetchGitStatus`), `internal/ui/session_events.go:141-150` (`applySessionEvent`: handle `usage` before the activity path)
- Test: `internal/ui/usage_fetch_test.go`

**Interfaces:**
- Consumes: nl-2 `detailMetaExtras{CostUSD, HasCost, Tokens, CacheRatio, HasCache}` and `m.metaExtras map[string]detailMetaExtras`; `remoteForHost` (`internal/ui/remote_services.go:87`); `ssh.NewManager(...)`; `usecase.ServeRPC` (nl-1 Task 3a).
- Produces:

```go
type usageMsg struct { sessionID string; rep usage.Report; err error }
const usageFetchMinInterval = 10 * time.Second
func fetchSessionUsageCmd(cfg *config.Config, s domain.Session) tea.Cmd // usecase.ServeRPC(ctx, mgr, "usage.get", {session: id, tz_offset_minutes: <laptop offset>})
func (m *Model) maybeFetchUsage(s domain.Session, force bool) tea.Cmd   // nil while in flight or fetched < usageFetchMinInterval ago, unless force
func applyUsageToExtras(x detailMetaExtras, rep usage.Report) detailMetaExtras
```

Model fields: `usageFetchedAt map[string]time.Time`, `usageInFlight map[string]bool`.

- [ ] **Step 1: Write the failing tests**
- `TestApplyUsageToExtras`: report total cost `1.234`, priced, ratio `0.81` → `HasCost`, `CostUSD 1.234`, `HasCache`, `CacheRatio 0.81`.
- `TestUsageMetaUnpricedShowsTokens`: `Priced:false` with tokens → `HasCost == false`, `Tokens` = input + output + cache read + cache writes (meta line shows `412k tok`, no `$`); ratio still set.
- `TestUsageMetaUnknownShowsNothing`: `Known:false` → both flags false.
- `TestUsageEventFetchesSelectedOnly`: `applySessionEvent` with `{Type:"usage", ID:<selected>}` returns a batch containing a fetch and does not call `queuePreview` (assert `m.eventSeen` untouched for that id); for a non-selected id it clears `usageFetchedAt[id]` and fetches nothing.
- `TestUsageFetchThrottled`: two `maybeFetchUsage(s, false)` calls within 10 s → second returns nil; `force` bypasses the interval but not `usageInFlight`.
- `TestUsageMsgUpdatesExtras`: `Update(usageMsg{…})` fills `m.metaExtras[id]`, clears in-flight; an error leaves existing extras and is logged with `m.log`.

- [ ] **Step 2: Run** `go test ./internal/ui/ -run 'ApplyUsageToExtras|UsageMeta|UsageEvent|UsageFetch|UsageMsg'` → FAIL.
- [ ] **Step 3: Implement.** On selection change call `m.maybeFetchUsage(s, false)` beside `fetchGitStatus`. Route `usageMsg` in `Update` before the `switch m.state` (like the AWS credential messages: a fetch can land after the user leaves the main view). If nl-2 has not landed, stop and finish nl-2 first: this task writes only into `m.metaExtras`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(ui): cost and cache % on the detail meta line`.

---

### Task 11: `$` usage view

**Files:**
- Create: `internal/ui/usage_view.go`
- Modify: `internal/ui/dashboard.go`: add `viewStateUsage` after `viewStateContextStats` (`:175`); `usageView UsageViewModel` field near `contextStats` (`:370`); `$` key after the `m` admin-menu branch in `handleMainKeyMsg` (`:4754-4757`), sessions tab only; help binding after `:808` (`key.WithKeys("$"), key.WithHelp("$", "usage and cost")`); `dispatchSettingsUpdate` (`:2908`) and `renderSettingsView` (`:3346`) cases; `AGENTS.md` (one paragraph under "Remote serve / trigger as a service" describing the usage tracker, `usage.get`, `aiman usage` and `$`)
- Test: `internal/ui/usage_view_test.go`, `internal/ui/render_all_states_test.go` (add the new state)

**Interfaces:**
- Consumes: Task 7 `usage.Row`, `usage.Report`; Task 9 `usage.FormatUsageCost`; `remoteForHost`.
- Produces:

```go
type usageViewMode int // usageByIssue (default), usageByDay
type usageHost struct { host string; rows map[usageViewMode][]usage.Row; unpriced []string; err string; busy bool; extra string /* one dim line under the host; empty here, filled by nl-8 with rtk gain */ }
type UsageViewModel struct { mode usageViewMode; hosts []usageHost; cursor int }
type usageRollupMsg struct { host string; mode usageViewMode; rep usage.Report; err error }
func fetchUsageRollupCmd(cfg *config.Config, host string, mode usageViewMode) tea.Cmd // ServeRPC usage.get {by: issue, days: 30} | {by: day, days: 14}
func mergeUsageRows(hosts []usageHost, mode usageViewMode) []usage.Row             // same key across hosts summed; ratio recomputed from tokens; Partial/Priced OR-ed
func (m *Model) enterUsageView() tea.Cmd
func (m *Model) handleUsageViewUpdate(msg tea.Msg) (tea.Model, tea.Cmd)
func (m UsageViewModel) viewString() string
```

Layout: title `Usage · last 30 days by ticket` / `Usage · last 14 days by day`; columns `TICKET|DAY  ~COST  CACHE  INPUT  OUTPUT` (cost cell `-` for unpriced-only rows, whose tokens still show); hosts still loading show a dim `<host> loading…` line, failed hosts a red one-line error; dim footer `tab by ticket/day  r refresh  esc back` and, when anything is unpriced, dim `~ estimates · unpriced: <ids>`. Keys: `tab` toggles mode (fetching the other mode on first use), `r` refetches, `j`/`k`/arrows move the cursor, `esc`/`q` return to `viewStateMain`.

- [ ] **Step 1: Write the failing tests**
- `TestDollarOpensUsageView`: main view, sessions tab, `Update(pressRune('$'))` → `m.state == viewStateUsage` and the returned cmd is non-nil (one fetch per remote).
- `TestDollarIgnoredOnDaemonsTab`: `m.currentTab = tabDaemons` → state unchanged.
- `TestMergeUsageRowsAcrossHosts`: `PROJ-1` on two hosts (costs `1.0` and `0.5`, tokens with ratios) → one row, cost `1.5`, ratio from summed tokens; one host partial → merged `Partial`.
- `TestUsageViewRendersRows`: by-issue rows `PROJ-1 ~$1.50 73%` and `(no ticket)`; a Codex-only row shows `-` and its tokens, no `~$`; unpriced footer shows ids; a host with `extra: "rtk: 12k tokens saved"` shows that line, a host without one shows none.
- `TestUsageViewTabSwitchesMode`: `tab` → mode `usageByDay`, cmd non-nil the first time, nil the second time (cached) until `r`.
- `TestUsageViewEscReturnsToMain`.

- [ ] **Step 2: Run** `go test ./internal/ui/ -run 'Dollar|MergeUsageRows|UsageView|RenderAllStates'` → FAIL.
- [ ] **Step 3: Implement** following `internal/ui/context_stats.go` (per-host fetch over SSH, `docStyle`, `failStyle`, `activeStyle`). Route `usageRollupMsg` globally like `usageMsg`.
- [ ] **Step 4: Run** `make ci` → green (apart from the known Ollama test).
- [ ] **Step 5: Manual check** with the `run` skill against a remote running this serve: select a Claude session and see `~$… · cache …%` on the meta line within one scan interval; press `$`, toggle `tab`, `esc`. Run `aiman usage` on the remote and compare one session's cost with Claude Code's `/cost`.
- [ ] **Step 6: Commit** `feat(ui): $ usage view with per-ticket and per-day rollups`, then `graphify update .`.

---

## Spec deviations

1. **Dedupe by API message id, not only offset + first id.** Claude repeats `message.usage` on every content-block line of one API call, so the spec's offset + first-record-id scheme alone double or triple counts. The plan keeps the offset and fingerprint (for speed and resets) and adds a per-session `usage_seen` table of message ids. That also makes resets safe without trusting the spec's assumption that "Claude rewrites on compact" (not observed here; `--resume` copying history is covered either way).
2. **First-record id is the first record carrying a `uuid`** (Claude) or `session_meta.payload.id` (Codex). Claude's literal first line is usually `last-prompt` or `queue-operation`, which has no id.
3. **Claude subagent transcripts are included.** They live in `<session>/subagents/*.jsonl`, outside `AgentSessionPath`; leaving them out under-reports any session that uses subagents.
4. **Cache creation is split by TTL.** Claude Code writes mostly 1-hour cache entries (2× input) and the API reports 5m/1h separately; pricing them as one rate would be wrong by up to 60%.
5. **Codex `input_tokens` includes cached tokens**, so the plan subtracts them before computing the spec's ratio `cache_read / (input + cache_read + cache_creation)`. Codex uses cumulative totals, not offsets alone: non-increasing totals are skipped, and per-thread state carries over to a new file of the same thread.
6. **Storage is per session × hour × model buckets**, not one total row per session. Per-day rollups and per-model pricing need it; costs are computed at read time.
7. **`usage.get` gains `by`, `days`, `tz_offset_minutes`** for the `$` view's rollups; the spec lists only the filters.
8. **Usage survives forget/archive** through a denormalised `usage_sessions` table, so per-ticket totals do not lose deleted sessions.
9. **`rtk gain` totals in the `$` view are left to nl-8** (W7 ships after W3). This plan only reserves `usageHost.extra`, rendered as one dim line per host when non-empty; nl-8 Task 7 fills it from `rtk.FetchGain`.
10. **Unpriced means tokens, not `~$`.** Codex prices ship as unconfirmed placeholders; until the user confirms them in `usage.prices`, Codex usage shows token counts only (meta line `<n>k tok`, `-` in cost columns). The user decides later whether Codex should get a `~$` at all.

## Open questions

1. **GPT/Codex prices are placeholders.** Published prices for `gpt-5.6-terra|sol|luna` and `codex-auto-review` could not be verified, so they ship unconfirmed: tokens are shown and no `~$` appears until the user confirms prices in `usage.prices.<id>` (decided). Open: Codex under a ChatGPT plan may not be billed per token at all, so the user may prefer never to price it.
2. **Haiku 5.5 long-prompt tier** ($0.50/$2.50 above 100K-token prompts) is not modelled; long Haiku calls are under-priced.
3. **Fast-mode cache prices** for `claude-opus-5-5@fast` assume 2× like input/output; only input/output are published in the reference used.
4. **Day boundaries** use the laptop's UTC offset at query time, so a DST change shifts old days by an hour.
5. **Dependencies.** Task 8 needs nl-1 Task 6 (`s.hub *eventHub`); Task 10 needs nl-2's `m.metaExtras` (including `Tokens`).
