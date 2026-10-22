# NL-6 CI and Review Reaction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aiman serve` watches each opted-in session's PR, turns failed checks and new review comments into steering to-dos, escalates after 3 failed fixes, and `aiman-trigger` stops typing into panes directly.

**Architecture:** A new `internal/ciwatch` package holds a `Watcher` that serve runs in one goroutine. It reads sessions from serve's repo, talks to GitHub through a small `GitHub` interface (real impl shells out to `gh`; tests use a fake), writes to-dos through nl-1's `Server.TodoAccess()` (which renders, publishes and steers), enqueues extra note text through an injected steer function wired to nl-1's `Server.Steerer()`, and records escalations through nl-5's `notify.Recorder.Emit`. Its state lives in four new tables in serve's SQLite DB. The TUI toggles the per-session `watch_pr` flag with `w` and reads status with `ci.get`, both through nl-1's `aiman rpc` (`usecase.ServeRPC`). `aiman-trigger` gets a `PromptSender` that calls nl-1's `steer.send` (mode `note` for scheduled prompts, `answer` for the unblocker).

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, `gh` CLI, Bubble Tea v2.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (W4; also "Constraints", "Shared interfaces", W1 steering, W2 notifications).

## Global Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only. Key tests build events with `pressKey`/`pressRune` (`internal/ui/keystrokes_test.go`).
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- No socket method may block longer than 30 s. `ci.get`/`ci.watch` read or write local state only; `gh` calls happen only in the watcher goroutine.
- Both terminal backends (tmux, pty) must work. Nothing in this plan types into a terminal itself; delivery is the Steerer's job.
- serve is the only writer of CI state. `aiman-trigger` opens the same DB but writes CI/steering state only through the socket.
- Import graph: `internal/server` imports `internal/ciwatch`; `internal/ciwatch` must not import `internal/server` or `internal/gateway`. `internal/usecase` must not import `internal/server`.
- Poll interval: every 2 minutes per watched session. Log excerpt: last 150 lines of `gh run view --log-failed`. Escalate after 3 failed attempts.
- To-do texts (exact): `Fix CI: <name>` with `Ref = "check:<name>"`; `Address review: <first line> (<file>:<line>)`.
- Notification kind (exact): `ci_escalation`.
- Meta line text (exact): `watching PR #<n>`.
- Source comments are present-tense rationale, no changelog narrative.

## Contracts from sibling plans

- **nl-1** `Server.TodoAccess()` returns the adapter whose `Get(sessionID)` / `Mutate(ctx, sessionID, author, fn) error` go through `Server.MutateTodos`, so a mutation with author `aiman` renders `.aiman_todo.md`, publishes `todo_changed` and enqueues the standard "[aiman] Your to-do list was changed…" note. `ciwatch` adds only the extra note (log excerpt or comment body) through `Server.Steerer().Enqueue`; the 800 ms coalescer merges the two. `Mutate` assigns `ID`, `UpdatedBy`, `UpdatedAt` to items appended with an empty `ID`.
- **nl-1** owns `steer.send {session, text, as, mode}` (Task 6 of this plan is therefore empty) and `aiman rpc` (this plan allowlists `ci.get`, `ci.watch`).
- **nl-1** `Ref` prefixes: this plan owns `check:<name>` and `review:<comment id>`.
- **nl-2** owns the detail meta line and the "needs you" glyph: this plan fills `m.metaExtras[id].WatchingPR` and `item.escalated`, and fetches `ci.get` wherever nl-2 calls `fetchTodosCmd`.
- **nl-5** `(*notify.Recorder).Emit(ctx, sessionID, label string, kind domain.NotifyKind, detail string) error` via `(*Server).NotifyRecorder()` (nil-safe), and `notify` hub events with `kind`. `ciwatch` declares that one-method interface itself.

## Review Focus

1. **An agent replying to a review comment through `gh` must not steer itself.** Replies (`in_reply_to_id != 0`) authored by the `gh` viewer login are skipped and marked seen. Test in Task 4.
2. **Turning `watch_pr` on for an old PR must not flood the agent with every historical comment.** Comments created before `WatchSince` are marked seen without steering; a bot comment arriving 30 s after an autonomous session opens its PR still steers. Tests in Task 4.
3. **A `gh` failure (not authenticated, network, rate limit) must not create to-dos or burn attempts.** The error is stored as `LastError` and shown by `ci.get`; nothing else changes. Test in Task 3.
4. **Logs not yet available (run still in progress, non-Actions check) must still produce a useful note.** The note carries `Log unavailable: <detailsUrl>` instead of the excerpt. Test in Task 3.
5. **serve not running when `aiman-trigger` fires a scheduled prompt must not lose the prompt.** `LastRunAt` advances only after every target enqueued; otherwise it retries next tick. Test in Task 7.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/domain/session.go` (modify) | `Session.WatchPR`, `WatchPROn`/`WatchPROff`, `WatchesPR()` |
| `internal/domain/ciwatch.go` (create) | `PRWatchState` persisted shape |
| `internal/infra/sqlite/repository.go`, `repository_scan.go` (modify) | `watch_pr` column, COALESCE |
| `internal/infra/sqlite/ci_state.go` (create) | CI tables + methods |
| `internal/ciwatch/github.go` (create) | `GitHub` interface, value types, `gh` parsing, `GHCLI` impl |
| `internal/ciwatch/watcher.go` (create) | `Watcher`, deps, loop, `Kick`, `SetWatch`, `Status`, `Forget` |
| `internal/ciwatch/checks.go` (create) | failed-check reaction + escalation |
| `internal/ciwatch/comments.go` (create) | review-comment reaction |
| `internal/server/handlers_ci.go` (create) | `ci.get`, `ci.watch` |
| `cmd/aiman/serve.go`, `cmd/aiman/rpc_cmd.go` (modify) | wiring; allowlist `ci.get`, `ci.watch` |
| `internal/usecase/daemon.go`, `cmd/aiman-trigger/main.go` (modify) | throttle, `PromptSender` |
| `internal/usecase/ci_remote.go` (create) | laptop-side wrappers over `ServeRPC` |
| `internal/ui/pr_watch.go` (create), `internal/ui/dashboard.go` (modify) | `w` key, cache, meta part |

---

### Task 1: Persist the `watch_pr` flag and CI state

**Files:**
- Modify: `internal/domain/session.go:55-91` (Session struct), add method below it
- Create: `internal/domain/ciwatch.go`
- Modify: `internal/infra/sqlite/repository.go:103` (ALTER list), `:218-253` (Save), `internal/infra/sqlite/repository_scan.go:11-40`
- Create: `internal/infra/sqlite/ci_state.go`
- Test: `internal/domain/session_watch_test.go`, `internal/infra/sqlite/ci_state_test.go`

**Interfaces:**
- Produces:
  - `domain.Session.WatchPR string` (`""` = default by mode, `"on"`, `"off"`); consts `domain.WatchPROn = "on"`, `domain.WatchPROff = "off"`.
  - `func (s Session) WatchesPR() bool` — `on` → true, `off` → false, empty → `s.Mode == SessionModeAutonomous`.
  - `domain.PRWatchState{SessionID string; PRNumber int; PRState string; HeadSHA string; WatchSince time.Time; LastPolledAt time.Time; LastError string; Escalated []string}` (`Escalated` = check names, sorted, loaded from the escalation table).
  - On `*sqlite.Repository`:
    - `LoadPRWatch(ctx, sessionID string) (domain.PRWatchState, error)` — zero value with `SessionID` set and nil error when no row.
    - `SavePRWatch(ctx, st domain.PRWatchState) error` — upsert; ignores `Escalated`.
    - `RecordCheckFailure(ctx, sessionID, check, sha string) (int, error)` — `INSERT OR IGNORE`, returns distinct failing SHAs for that check.
    - `MarkCheckEscalated(ctx, sessionID, check string, at time.Time) (bool, error)` — true only when newly inserted.
    - `ClearCheck(ctx, sessionID, check string) error` — deletes that check's failures and escalation.
    - `SeenComments(ctx, sessionID string) (map[int64]bool, error)`, `MarkCommentsSeen(ctx, sessionID string, ids []int64) error`.
    - `ResetPRWatch(ctx, sessionID string) error` — deletes every CI row for the session (all four tables).

- [ ] **Step 1: Write failing tests**

`TestWatchesPRDefaults` (table): `{Mode: AUTONOMOUS, WatchPR: ""}`→true; `{INTERACTIVE, ""}`→false; `{AUTONOMOUS, "off"}`→false; `{INTERACTIVE, "on"}`→true.

In `ci_state_test.go` (temp-dir DB via `NewRepository`):
- `TestWatchPRSurvivesBlankSave`: save session with `WatchPR: "off"`, save again with `WatchPR: ""`, `Get` → `"off"`.
- `TestRecordCheckFailureIsIdempotentPerSHA`: `("s1","lint","a")`→1, again `"a"`→1, `"b"`→2; `("s1","test","a")`→1.
- `TestMarkCheckEscalatedOnce`: first call true, second false; `LoadPRWatch` → `Escalated == []string{"lint"}`; after `ClearCheck("s1","lint")` → `Escalated` empty and `RecordCheckFailure("s1","lint","c")` → 1.
- `TestSeenCommentsRoundTrip`: mark `{1,2}`, mark `{2,3}`, `SeenComments` has exactly 1,2,3.
- `TestResetPRWatchClearsAll`: populate all four tables, reset, `LoadPRWatch` zero (except `SessionID`), `SeenComments` empty, `RecordCheckFailure` → 1.
- `TestPRWatchStateRoundTrip`: save `{PRNumber: 7, PRState: "OPEN", HeadSHA: "abc", WatchSince: t, LastError: "x"}`, load equal (times compared with `Equal`).

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/domain/ ./internal/infra/sqlite/ -run 'WatchesPR|WatchPR|CheckFailure|CheckEscalated|SeenComments|ResetPRWatch|PRWatchState'` fails to compile.

- [ ] **Step 3: Implement**

`ALTER TABLE sessions ADD COLUMN watch_pr TEXT` next to `parent_id` (`repository.go:103`); add `watch_pr` to `sessionSelectCols`, the scan (NullString), the INSERT list and `watch_pr = COALESCE(NULLIF(excluded.watch_pr, ''), sessions.watch_pr)`. Add the column to the AGENTS.md COALESCE list.

`ci_state.go` adds `ensureCITables(db *sql.DB) error`, called from `NewRepository` after the `scheduled_prompts` table:

```sql
CREATE TABLE IF NOT EXISTS ci_watch (session_id TEXT PRIMARY KEY, pr_number INTEGER NOT NULL DEFAULT 0, pr_state TEXT NOT NULL DEFAULT '', head_sha TEXT NOT NULL DEFAULT '', watch_since DATETIME, last_polled_at DATETIME, last_error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS ci_check_failures (session_id TEXT NOT NULL, check_name TEXT NOT NULL, head_sha TEXT NOT NULL, failed_at DATETIME NOT NULL, PRIMARY KEY (session_id, check_name, head_sha));
CREATE TABLE IF NOT EXISTS ci_escalations (session_id TEXT NOT NULL, check_name TEXT NOT NULL, escalated_at DATETIME NOT NULL, PRIMARY KEY (session_id, check_name));
CREATE TABLE IF NOT EXISTS ci_seen_comments (session_id TEXT NOT NULL, comment_id INTEGER NOT NULL, PRIMARY KEY (session_id, comment_id));
```

`MarkCommentsSeen` uses one transaction.

- [ ] **Step 4: Run, expect PASS** — same command, plus `go test ./internal/infra/sqlite/` (existing COALESCE tests still pass).

- [ ] **Step 5: Commit** — `git commit -m "feat(ci): persist watch_pr flag and CI watcher state"`

---

### Task 2: `gh` client and parsers

**Files:**
- Create: `internal/ciwatch/github.go`
- Test: `internal/ciwatch/github_test.go`, fixtures in `internal/ciwatch/testdata/`

**Interfaces:**
- Produces:

```go
var ErrNoPR = errors.New("no pull request for this branch")

type Conclusion string // "failure" | "success" | "pending" | "other"

type Check struct {
    Name       string     // CheckRun.name or StatusContext.context
    Conclusion Conclusion
    DetailsURL string     // detailsUrl or targetUrl
    JobID      int64      // from .../actions/runs/<run>/job/<job>; 0 when not an Actions job
}
type PRView struct {
    Number  int
    HeadSHA string
    State   string // OPEN | CLOSED | MERGED
    Checks  []Check
}
type ReviewComment struct {
    ID        int64
    Body      string
    Path      string
    Line      int // line, else original_line, else 0
    Author    string
    InReplyTo int64
    CreatedAt time.Time
}
type GitHub interface {
    PRView(ctx context.Context, dir string) (PRView, error)        // ErrNoPR when gh reports none
    FailedJobLog(ctx context.Context, dir string, jobID int64) (string, error)
    ReviewComments(ctx context.Context, dir string, pr int) ([]ReviewComment, error)
    ViewerLogin(ctx context.Context) (string, error)
}
type GHCLI struct{ Timeout time.Duration } // implements GitHub; zero Timeout means 45s per call
func ParsePRView(raw []byte) (PRView, error)
func ParseReviewComments(ndjson []byte) ([]ReviewComment, error)
func TailLines(s string, n int) string
const LogTailLines = 150
```

Commands (args slices, `cmd.Dir = dir`, `exec.CommandContext` with per-call timeout):
- `gh pr view --json number,headRefOid,state,statusCheckRollup`. A non-zero exit whose stderr contains `no pull requests found` maps to `ErrNoPR`.
- `gh run view --job <jobID> --log-failed`, then `TailLines(out, LogTailLines)`.
- `gh api repos/{owner}/{repo}/pulls/<n>/comments --paginate --jq '.[] | {id, body, path, line, original_line, in_reply_to_id, created_at, user: .user.login}'` (`gh` fills `{owner}/{repo}` from the worktree's remote; `--jq` emits one object per line across pages).
- `gh api user --jq .login`.

Conclusion mapping: CheckRun with `status != COMPLETED` → pending; `conclusion` in `FAILURE, TIMED_OUT, STARTUP_FAILURE` → failure; `SUCCESS, NEUTRAL, SKIPPED` → success; else other (CANCELLED, ACTION_REQUIRED, STALE). StatusContext `state`: `FAILURE, ERROR` → failure; `SUCCESS` → success; `PENDING, EXPECTED` → pending.

- [ ] **Step 1: Write failing tests**

Fixture `testdata/pr_view.json` with one PR (`number: 42`, `headRefOid: "abc123"`, `state: "OPEN"`) and rollup entries: CheckRun `lint` FAILURE with `detailsUrl: "https://github.com/o/r/actions/runs/111/job/222"`; CheckRun `test` IN_PROGRESS; CheckRun `build` SUCCESS; StatusContext `ci/ext` ERROR with `targetUrl: "https://ci.example/9"`.

- `TestParsePRView`: `Number==42`, `HeadSHA=="abc123"`, `State=="OPEN"`, checks: `lint` failure JobID 222; `test` pending; `build` success; `ci/ext` failure JobID 0, DetailsURL `https://ci.example/9`.
- `TestParseReviewComments`: two NDJSON lines; one with `line: null, original_line: 17` → `Line==17`; one reply `in_reply_to_id: 5` → `InReplyTo==5`; `Author` from `user`; `CreatedAt` parsed.
- `TestTailLines`: 200-line input → exactly the last 150 lines; 3-line input unchanged; trailing newline does not count as a line.
- `TestGHCLIUsesFakeBinary`: put a shell script named `gh` in `t.TempDir()`, prepend to `PATH` via `t.Setenv`; script echoes the fixture for `pr view` and exits 1 with `no pull requests found for branch "x"` when `$GH_FAKE_NOPR` is set. Assert `PRView` parses and that the second mode returns `errors.Is(err, ErrNoPR)`.

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/ciwatch/ -run 'ParsePRView|ParseReviewComments|TailLines|GHCLI'`.

- [ ] **Step 3: Implement** in `github.go`. Job id via `regexp.MustCompile(`/actions/runs/\d+/job/(\d+)`)`.

- [ ] **Step 4: Run, expect PASS** — same command.

- [ ] **Step 5: Commit** — `git commit -m "feat(ci): gh client for PR checks, failed logs and review comments"`

---

### Task 3: Watcher core and failed-check reaction

**Files:**
- Create: `internal/ciwatch/watcher.go`, `internal/ciwatch/checks.go`
- Test: `internal/ciwatch/checks_test.go`, `internal/ciwatch/fakes_test.go`

**Interfaces:**
- Consumes: Task 1 store methods (via the `Store` interface below); Task 2 `GitHub`, `PRView`, `Check`; nl-1 `domain.TodoItem`, `domain.TodoAuthor`, `Server.TodoAccess()` shape; nl-5 `domain.NotifyCIEscalation`, `notify.Label`.
- Produces:

```go
type Store interface { // satisfied by *sqlite.Repository
    LoadPRWatch(ctx context.Context, sessionID string) (domain.PRWatchState, error)
    SavePRWatch(ctx context.Context, st domain.PRWatchState) error
    RecordCheckFailure(ctx context.Context, sessionID, check, sha string) (int, error)
    MarkCheckEscalated(ctx context.Context, sessionID, check string, at time.Time) (bool, error)
    ClearCheck(ctx context.Context, sessionID, check string) error
    SeenComments(ctx context.Context, sessionID string) (map[int64]bool, error)
    MarkCommentsSeen(ctx context.Context, sessionID string, ids []int64) error
    ResetPRWatch(ctx context.Context, sessionID string) error
}
type Sessions interface{ List(ctx context.Context) ([]domain.Session, error) }
type Todos interface { // nl-1 Server.TodoAccess(); same shape as verify.Todos and fanout.Todos
    Get(sessionID string) (domain.TodoList, error)
    Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error
}
type Notifier interface { // nl-5 *notify.Recorder
    Emit(ctx context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error
}
type SteerFunc func(sessionID string, author domain.TodoAuthor, text string)

type Deps struct {
    Sessions Sessions
    GitHub   GitHub
    Todos    Todos
    Steer    SteerFunc
    Notify   Notifier
    State    Store
    Now      func() time.Time // nil → time.Now
}
const (
    PollInterval   = 2 * time.Minute
    MaxFixAttempts = 3
)
func New(d Deps) *Watcher
func (w *Watcher) PollSession(ctx context.Context, s domain.Session) error
func CheckRef(name string) string // "check:" + name
```

`PollSession` holds `w.mu` for its whole run. Dir is `s.WorktreePath`, else `s.WorkingDirectory`. Flow: `PRView` → on `ErrNoPR` save `PRNumber 0`, clear `LastError`; on other error save `LastError = err.Error()` and return it with nothing else changed; else save number/state/sha/`LastPolledAt`, `LastError=""`. If `State != "OPEN"`, stop. Then `reactToChecks`, then `reactToComments` (Task 4; stub returning nil here).

`reactToChecks(ctx, s, pr)` per check:
- `success` → `ClearCheck`; if an open (`pending`/`active`) to-do has `Ref == CheckRef(name)`, `Mutate` it to `done` as author `aiman`.
- `failure` → `n := RecordCheckFailure(name, pr.HeadSHA)`. If `n > MaxFixAttempts`: `MarkCheckEscalated`; when newly escalated, `Notify.Emit(ctx, s.ID, notify.Label(s.IssueKey, s.Name, s.ID), domain.NotifyCIEscalation, fmt.Sprintf("CI check %s still failing after %d fix attempts on PR #%d", name, MaxFixAttempts, pr.Number))`; no steering. Else if no open to-do with that Ref: `Get` then `Mutate` (re-check inside the callback) appending `TodoItem{Text: "Fix CI: " + name, State: pending, Ref: CheckRef(name)}` as author `aiman`, then `Steer(s.ID, "aiman", checkNote(...))`.
- `pending`/`other` → nothing.

Extra note text (exact):

```
[aiman] CI check "<name>" failed on <sha[:7]> (PR #<n>), to-do <tid>.
Last 150 lines of `gh run view --job <jobID> --log-failed`:
<excerpt>
```

When `JobID == 0` or `FailedJobLog` errors, the last two lines become `Log unavailable: <detailsUrl>`.

- [ ] **Step 1: Write failing tests**

`fakes_test.go`: `fakeGH` (scripted `PRView`/logs/comments/viewer, records calls), `fakeTodos` (in-memory `ciwatch.Todos`, assigns `t1, t2…` to empty IDs, records authors), `fakeNotifier` (records `kind, detail`), `steerRecorder`, and a real `*sqlite.Repository` on a temp DB as `Store`.

- `TestFailedCheckAddsTodoAndSteersLog`: PR 42 sha `aaaaaaa1`, `lint` failure JobID 222, log of 200 lines. Expect one to-do `Fix CI: lint`, `Ref "check:lint"`, author `aiman`; one steer whose text starts `[aiman] CI check "lint" failed on aaaaaaa (PR #42), to-do t1.` and contains line 51 and line 200 but not line 50.
- `TestSamePollRepeatedDoesNotDuplicate`: poll twice, same sha → one to-do, one steer, `RecordCheckFailure` count 1.
- `TestOpenTodoSuppressesNewNotes`: poll sha A, then sha B still failing with `t1` pending → no new to-do, no new steer.
- `TestEscalatesAfterThreeFailedFixes`: four polls, each new sha, agent marks the to-do done between polls. Steers on polls 1–3, none on 4; `fakeNotifier` gets exactly one `ci_escalation` with detail `CI check lint still failing after 3 fix attempts on PR #42`; fifth poll with sha E → no new notification.
- `TestPassClearsAttemptsAndClosesTodo`: fail A (t1 open), then `lint` success on B → t1 `done` by `aiman`; then fail C → new to-do, steer, count 1.
- `TestLogUnavailableNote`: JobID 0, DetailsURL `https://ci.example/9` → note ends `Log unavailable: https://ci.example/9`.
- `TestGHErrorChangesNothing` (Review Focus 3): `PRView` returns `errors.New("gh: auth required")` → `PollSession` returns error, `LoadPRWatch().LastError == "gh: auth required"`, no to-dos, no steers, no failure rows.
- `TestMergedPRIsQuiet`: `State "MERGED"` with a failed check → no to-do, no steer.
- `TestNoPRRecordsZero`: `ErrNoPR` → `PRNumber == 0`, `LastError == ""`.

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/ciwatch/ -run 'FailedCheck|SamePoll|OpenTodo|Escalates|PassClears|LogUnavailable|GHError|MergedPR|NoPR'`.

- [ ] **Step 3: Implement** `watcher.go` (struct, `New`, `PollSession`) and `checks.go` (`reactToChecks`, `checkNote`, `openTodoByRef(list domain.TodoList, ref string) (domain.TodoItem, bool)`).

- [ ] **Step 4: Run, expect PASS**, then `go test -race ./internal/ciwatch/`.

- [ ] **Step 5: Commit** — `git commit -m "feat(ci): react to failed PR checks with steering to-dos and escalation"`

---

### Task 4: Review-comment reaction

**Files:**
- Create: `internal/ciwatch/comments.go`
- Test: `internal/ciwatch/comments_test.go`

**Interfaces:**
- Consumes: Task 3 `Watcher`, `Deps`; Task 2 `ReviewComment`, `ViewerLogin`.
- Produces: `func ReviewRef(id int64) string` (`"review:<id>"`); `reactToComments(ctx, s domain.Session, st domain.PRWatchState, pr PRView) error` called from `PollSession`.

Rules, in order, per comment not in `SeenComments`:
1. `InReplyTo != 0 && Author == viewer` → mark seen, skip (the agent's own replies). Viewer login is fetched once and cached on the `Watcher`; an empty login after an error is retried next poll and, until known, no comments are processed that poll.
2. `CreatedAt.Before(st.WatchSince)` → mark seen, skip. `WatchSince` zero means `s.CreatedAt`.
3. Otherwise add `TodoItem{Text: "Address review: " + firstLine + " (" + path + ":" + line + ")", State: pending, Ref: ReviewRef(id)}` as author `aiman`, then steer. `firstLine` is the first non-empty line of `Body`, cut to 80 runes with `…`. With `Line == 0` the suffix is `(<path>)`.
4. Mark all processed ids seen in one `MarkCommentsSeen` call after the to-dos are written (a crash between the two re-adds at most one batch; the `Ref` check below prevents duplicates).
5. Skip adding if an open to-do already has that `Ref`.

Extra note (exact):

```
[aiman] New review comment on PR #<n> at <path>:<line> by @<author>, to-do <tid>:
<body>
```

`<body>` is cut at 4000 bytes with a trailing `[truncated]` line.

- [ ] **Step 1: Write failing tests**

- `TestNewCommentAddsTodo`: comment `{ID: 9, Body: "Please rename foo\nmore", Path: "a/b.go", Line: 12, Author: "rev", CreatedAt: after}` → to-do `Address review: Please rename foo (a/b.go:12)`, `Ref "review:9"`; steer text starts `[aiman] New review comment on PR #42 at a/b.go:12 by @rev, to-do t1:` and contains `more`.
- `TestCommentDedupeAcrossPolls`: poll twice → one to-do, one steer.
- `TestViewerReplyIgnored` (Review Focus 1): viewer `me`; comment `{Author: "me", InReplyTo: 9}` → no to-do, id seen. A top-level comment by `me` still produces a to-do.
- `TestCommentsBeforeWatchSinceBaselined` (Review Focus 2): `WatchSince = T`; comment at `T-1h` → no to-do, seen; comment at `T+30s` → to-do.
- `TestZeroWatchSinceUsesSessionCreatedAt`: `s.CreatedAt = T`, comment at `T+30s` → to-do.
- `TestLongBodyTruncated`: 10 KB body → note ≤ 4000 bytes of body plus `[truncated]`.

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/ciwatch/ -run 'Comment|Viewer|WatchSince|LongBody'`.

- [ ] **Step 3: Implement** `comments.go`; replace the Task 3 stub.

- [ ] **Step 4: Run, expect PASS** — `go test -race ./internal/ciwatch/`.

- [ ] **Step 5: Commit** — `git commit -m "feat(ci): turn new PR review comments into steering to-dos"`

---

### Task 5: Run the watcher in serve; `ci.get`, `ci.watch` and CLI

**Files:**
- Modify: `internal/ciwatch/watcher.go`
- Create: `internal/server/handlers_ci.go`
- Modify: `internal/server/server.go:25-47` (field + setter), `:118-188` (dispatch), `internal/server/handlers_session.go:708-727` (`handleForget`)
- Modify: `cmd/aiman/serve.go:94-99`, `cmd/aiman/rpc_cmd.go` (allowlist `ci.get`, `ci.watch`)
- Test: `internal/ciwatch/watcher_test.go`, `internal/server/handlers_ci_test.go`, `cmd/aiman/rpc_cmd_test.go`

**Interfaces:**
- Consumes: Tasks 1–4; nl-1 `Server.TodoAccess()`, `Server.Steerer()`, `server.SteerNote`; nl-5 `Server.NotifyRecorder()`.
- Produces:

```go
func (w *Watcher) Run(ctx context.Context)            // polls watched sessions now, then every PollInterval; Kick polls one at once
func (w *Watcher) Kick(sessionID string)              // non-blocking; buffered chan of 16, drops when full
func (w *Watcher) SetWatch(ctx context.Context, sessionID string, on bool) error // ResetPRWatch; when on, saves WatchSince = Now()
func (w *Watcher) Status(ctx context.Context, sessionID string) (domain.PRWatchState, error)
func (w *Watcher) Forget(ctx context.Context, sessionID string) error // ResetPRWatch
func Watched(s domain.Session) bool // WatchesPR() && Status == ACTIVE && !AgentEnded && dir != ""

func (s *Server) SetCIWatcher(w *ciwatch.Watcher)
```

Socket methods (in `handlers_ci.go`):

| Method | Params | Result |
|---|---|---|
| `ci.get` | `session` | `{"type":"ci","ci":CIInfo}` |
| `ci.watch` | `session`, `on` (bool, required) | sets `sess.WatchPR` to `on`/`off`, `repo.Save`, `SetWatch`, `Kick` when on; returns `ci.get` shape |

```go
type CIInfo struct {
    Watch      bool     `json:"watch"`
    PRNumber   int      `json:"pr_number,omitempty"`
    PRState    string   `json:"pr_state,omitempty"`
    HeadSHA    string   `json:"head_sha,omitempty"`
    Escalated  []string `json:"escalated,omitempty"`
    LastPolled string   `json:"last_polled_at,omitempty"` // RFC3339
    LastError  string   `json:"last_error,omitempty"`
}
```

With no watcher set, both answer `CodeInvalidParams`, message `ci watcher not running`. `handleForget` calls `w.Forget` when the watcher is set.

No CLI subcommands: the laptop calls both methods through `aiman rpc` (allowlist entries plus `TestRPCParams` rows).

Wiring in `runServe` (after nl-1's `srv.EnableTodos` and nl-5's `srv.EnableNotifications`): `w := ciwatch.New(ciwatch.Deps{Sessions: db, GitHub: ciwatch.GHCLI{}, Todos: srv.TodoAccess(), Steer: func(id string, a domain.TodoAuthor, t string) { srv.Steerer().Enqueue(server.SteerNote{SessionID: id, Author: a, Text: t}) }, Notify: srv.NotifyRecorder(), State: db})`; `srv.SetCIWatcher(w)`; `go w.Run(ctx)`. Update `printServeUsage` with one sentence: serve watches the PR of each session with `watch_pr` on (default for autonomous sessions) and steers failed checks and review comments.

- [ ] **Step 1: Write failing tests**

- `TestWatchedFilter` (table): autonomous ACTIVE with dir → true; interactive default → false; `WatchPR "on"` interactive → true; ended → false; INACTIVE autonomous trigger rule → false; empty dirs → false.
- `TestRunPollsWatchedAndKick`: fake `Sessions` with one watched and one unwatched; `Run` with a cancelable ctx; `fakeGH` sees `PRView` for the watched dir only; `Kick(id)` causes a second `PRView` well before `PollInterval` (wait on a channel the fake closes, 2 s timeout).
- `TestSetWatchResetsState`: populate failures + seen ids, `SetWatch(on=true)` → `Status` has no escalations, `WatchSince == Now()`.
- `TestCIWatchHandlerPersistsFlag` (server, real temp repo + watcher with fakes): `ci.watch {session, on:false}` → `repo.Get(id).WatchPR == "off"`, result `ci.watch == false`; `on:true` → `"on"`, `true`.
- `TestCIGetWithoutWatcher`: error code `invalid_params`, message `ci watcher not running`.
- `TestForgetClearsCIState`: after `session.forget`, `LoadPRWatch` zero.
- `TestRPCParams` rows: `ci.get {session}` and `ci.watch {session, on}` accepted.

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/ciwatch/ ./internal/server/ ./cmd/aiman/ -run 'WatchedFilter|RunPolls|SetWatch|CIWatch|CIGet|ForgetClearsCI|RPCParams'`.

- [ ] **Step 3: Implement** the methods, handlers, allowlist entries and wiring above.

- [ ] **Step 4: Run, expect PASS** — same command, then `go build ./...`.

- [ ] **Step 5: Commit** — `git commit -m "feat(ci): run the PR watcher in serve with ci.get and ci.watch"`

---

### Task 6: (owned by nl-1) `steer.send`

nl-1 Task 13 ships `steer.send {session, text, as, mode}`: `as` may be `"aiman"` (trigger), a session cannot steer itself, `mode: "note"` queues through the delivery gate and `mode: "answer"` types at once only when the agent shows a dialog. Nothing to build here; the task number is kept so references stay valid.

---

### Task 7: `aiman-trigger` fixes

**Files:**
- Modify: `internal/usecase/daemon.go:19-31` (struct, constructor), `:69-82` (throttle), `:203-259` (`monitorPanes`), `:261-316` (scheduled prompts)
- Modify: `cmd/aiman-trigger/main.go` (constructor call)
- Test: `internal/usecase/daemon_test.go`

**Interfaces:**
- Consumes: nl-1 `steer.send`.
- Produces:

```go
type SendMode string
const (SendNote SendMode = "note"; SendAnswer SendMode = "answer")
type PromptSender interface {
    // SendPrompt returns delivered=false without error when an answer found no dialog to answer.
    SendPrompt(ctx context.Context, sessionID, text string, mode SendMode) (delivered bool, err error)
}
func NewDaemon(cfg *config.Config, db domain.SessionRepository, fm *FlowManager, sender PromptSender) *Daemon
const defaultTriggerPollSecs = 300
func (d *Daemon) dueForPoll(ruleID string, freqSecs int, now time.Time) bool
const unblockPrompt = "Please carry on autonomously as far as you can until it is absolutely impossible."
```

- `dueForPoll`: `freqSecs <= 0` → `defaultTriggerPollSecs`. True when the rule has never polled or `now.Sub(last) >= freq`; records `now` when it returns true. State in `d.lastPolled map[string]time.Time` (the daemon loop is single-goroutine). Replaces the TODO at `daemon.go:82`.
- `monitorPanes`: drop the `s.TmuxSession != ""` condition (capture already supports both backends via `CaptureSessionPane`); replace the `tmux send-keys` at `:253` with `d.sender.SendPrompt(ctx, s.ID, unblockPrompt, SendAnswer)`, logging errors. Answer mode types at once when serve sees a dialog, so the unblocker still unblocks; when serve sees no dialog it does nothing and the next tick re-checks.
- `triggerPrompt` returns `error`: drop the `TmuxSession == ""` skip and the `tmux send-keys` at `:313`; call `SendPrompt(…, SendNote)` per session; join errors with `errors.Join`. `pollScheduledPrompts` sets `LastRunAt` only when `triggerPrompt` returns nil.
- `cmd/aiman-trigger/main.go` defines `socketSender{path string}` whose `SendPrompt` calls `server.Call(path, "steer.send", map[string]any{"session": id, "text": text, "as": "aiman", "mode": string(mode)})`, turns a response `Error` into a Go error, and returns `answered` for answer mode (true for note mode once queued). Path: `server.SocketPath(dir)` from `config.GetDir()`. The trigger calls the socket directly (it runs on the remote), not `aiman rpc`.

- [ ] **Step 1: Write failing tests** (fake `SessionRepository` embedding the interface and overriding `List`, `Get`, `ListScheduledPrompts`, `SaveScheduledPrompt`; `recordingSender`):
  - `TestDueForPollHonoursFrequency`: freq 120: t0 true, t0+60s false, t0+120s true. freq 0: t0 true, t0+299s false, t0+300s true. Two rule ids are independent.
  - `TestScheduledPromptGoesThroughSender`: prompt due for sessions `a`, `b` (one pty with empty `TmuxSession`) → sender gets both with the prompt text and mode `note`; `SaveScheduledPrompt` called with updated `LastRunAt`.
  - `TestUnblockerUsesAnswerMode`: a pane classified `waiting_input` → sender gets `unblockPrompt` with mode `answer`; `delivered=false` is not an error.
  - `TestScheduledPromptRetriesWhenServeDown` (Review Focus 5): sender returns `server.ErrServerNotRunning`-like error → `SaveScheduledPrompt` not called.
  - `TestTriggerHasNoRawSendKeys`: read `daemon.go` source and assert it does not contain `send-keys` (guards the regression cheaply).

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/usecase/ -run 'DueForPoll|ScheduledPrompt|Unblocker|RawSendKeys'`.
- [ ] **Step 3: Implement** the above.
- [ ] **Step 4: Run, expect PASS**, then `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -m "fix(trigger): honour PollFrequencySecs and steer through serve"`

---

### Task 8: TUI toggle `w`, meta line, escalation

Key choice: **`w`** ("watch PR"). Verified free in the main view: the bindings at `internal/ui/dashboard.go:784-808`, the handlers in `handleNavigationKey`/`handleSessionActionKey`/`handleSessionManageKey` (`:4653-5265`) and the Bubbles list keymap do not use it. It already sits in the "still being created"/"being terminated" guard lists at `:4527` and `:4537`, so those guards cover it with no change.

**Files:**
- Create: `internal/usecase/ci_remote.go`, `internal/ui/pr_watch.go`
- Modify: `internal/ui/dashboard.go:784-808` (help binding), `handleSessionActionKey` (`:4776`), nl-2's `fetchTodosCmd` call sites (selection change, `todo_changed`), nl-2's `makeItem`/`refreshTodoProgress` (fill `item.escalated`) and `m.metaExtras` (fill `WatchingPR`)
- Modify: `AGENTS.md` (Remote serve section: one paragraph on the PR watcher, `w`, `ci.get`/`ci.watch`, and the trigger's use of `steer.send`)
- Test: `internal/usecase/ci_remote_test.go`, `internal/ui/pr_watch_test.go`

**Interfaces:**
- Consumes: Task 5 `ci.get`/`ci.watch` and `CIInfo` JSON through nl-1's `usecase.ServeRPC`; nl-2 `detailMetaExtras.WatchingPR`, `item.escalated`; nl-5 `notify` events with `kind`.
- Produces:

```go
// usecase
type CIStatus struct { Watch bool; PRNumber int; PRState string; Escalated []string; LastError string }
func GetCIStatusOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string) (CIStatus, error)
func SetPRWatchOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string, on bool) (CIStatus, error)
func parseCIResult(out string) (CIStatus, error)
var ErrCIUnsupported = errors.New("remote aiman has no PR watch") // wraps usecase.ErrRPCUnsupported

// ui
type ciStatusMsg struct { id string; status usecase.CIStatus; err error; toggled bool }
func watchingPRNumber(st usecase.CIStatus, ok bool) int   // PRNumber when ok && Watch && PRNumber > 0, else 0 → m.metaExtras[id].WatchingPR (nl-2 renders "watching PR #<n>")
func ciNeedsUser(st usecase.CIStatus, ok bool) bool        // ok && len(Escalated) > 0 → item.escalated
func nextPRWatch(s domain.Session, st usecase.CIStatus, ok bool) bool // !st.Watch when ok, else !s.WatchesPR()
// Model gains: ciStatus map[string]usecase.CIStatus
func (m *Model) fetchCIStatusCmd(s domain.Session) tea.Cmd
func (m *Model) togglePRWatchCmd(s domain.Session) tea.Cmd
```

Both usecase wrappers call `ServeRPC(ctx, remote, "ci.get", {session})` / `ServeRPC(ctx, remote, "ci.watch", {session, on})` and parse `{"type":"ci","ci":{…}}`. `usecase.ErrRPCUnsupported` maps to `ErrCIUnsupported` so the TUI can toast `Update aiman on <host> to use PR watch`.

Behaviour:
- `w` on a selected session sends `nextPRWatch(s, m.ciStatus[id])`. On `ciStatusMsg{toggled: true}` store the status and toast `Watching PR for <name>` or `Stopped watching PR for <name>`; on error toast the error.
- Fetch `ci.get` for the selected session wherever nl-2 calls `fetchTodosCmd` (selection change and `todo_changed`), and on a `notify` event with `kind == "ci_escalation"` for any session (so its row turns ●): add that fetch to nl-5's `notify` branch in `applySessionEvent`, batched with its toast, without queuing a preview.
- Meta line: set `m.metaExtras[id].WatchingPR = watchingPRNumber(…)`.
- Needs-you: set `item.escalated = ciNeedsUser(m.ciStatus[id])`; nl-2's `rowGlyph` and `needsYouCount` already honour it.
- Help: `key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "watch PR (CI + reviews)"))` after the `d` binding.

- [ ] **Step 1: Write failing tests**
  - `TestParseCIResult`: sample JSON with `watch: true, pr_number: 12, escalated: ["lint"]` → fields set; malformed → error.
  - `TestCIRemoteUnsupported`: fake rpc returns `usecase.ErrRPCUnsupported` → `errors.Is(err, ErrCIUnsupported)`.
  - `TestSetPRWatchOnRemoteParams`: fake rpc records method `ci.watch` with params `{session: "id1", on: false}`.
  - `TestWatchingPRNumber`: `{Watch: true, PRNumber: 12}` → `12`; `PRNumber 0` → `0`; `Watch false` → `0`; `ok false` → `0`. A main-view render with that extra contains `watching PR #12`.
  - `TestCINeedsUser`: escalated non-empty → true; empty → false.
  - `TestNextPRWatch` (table): autonomous, no cache → false; interactive, no cache → true; cache `Watch: false` on an autonomous session → true; cache `Watch: true` → false.
  - `TestWKeyReturnsToggleCmd`: model with one selected session; `pressRune('w')` is handled and returns a non-nil cmd.
  - `TestWKeyBlockedWhileCreating`: session in `creatingSessions` → toast text contains `still being created`.

- [ ] **Step 2: Run, expect FAIL** — `go test ./internal/usecase/ ./internal/ui/ -run 'CIResult|CIRemote|SetPRWatch|WatchingPR|CINeedsUser|NextPRWatch|WKey'`.
- [ ] **Step 3: Implement** the wrappers, messages, key handler (in `handleSessionActionKey`), meta extra, `escalated` hook, help binding and AGENTS.md paragraph.
- [ ] **Step 4: Run, expect PASS**, then `make ci`.
- [ ] **Step 5: Commit** — `git commit -m "feat(ui): w toggles PR watch; meta line shows watching PR #n"`

---

## Spec deviations

1. **`state` added to the `gh pr view` fields** (`number,headRefOid,state,statusCheckRollup`) so merged or closed PRs stop steering.
2. **Attempt counting.** The spec says "after 3 failed attempts across new SHAs". The plan counts distinct failing head SHAs per check: the first failure is the original, not an attempt, so it escalates at the 4th distinct failing SHA (`n > MaxFixAttempts`).
3. **Auto-close on pass.** Not in the spec: when a check passes at the head SHA, its attempts and escalation are cleared and an open `check:` to-do is marked done as `aiman`, which steers. Without this, a to-do the agent forgets to close blocks every later note for that check.
4. **Review comments mean inline PR review comments** (`gh api repos/{owner}/{repo}/pulls/<n>/comments`). Review summary bodies and conversation comments are out of scope. Review to-dos use `Ref = "review:<id>"`. Replies authored by the `gh` viewer are skipped, which also skips the user's own replies when the agent and the user share one GitHub account. Comments older than `WatchSince` (toggle time, else session creation) are baselined silently.
5. **The unblocker uses `steer.send` mode `answer`, scheduled prompts use mode `note`.** The spec routes both through the steering queue; queuing the unblocker would hold it behind the gate while the y/n dialog it exists to answer is showing. `answer` types at once only when serve sees a dialog and is never queued.
6. **New socket methods not named in the spec:** `ci.get`, `ci.watch` (laptop calls through `aiman rpc`). The TUI makes one SSH call for `ci.get` per selection, because the spec defines no `ci` event type.
7. **`ciwatch` takes a `SteerFunc`, not `server.Steerer`.** `internal/server` imports `ciwatch`, so `ciwatch` cannot name `server.SteerNote`; serve adapts.
8. **`watch_pr` is tri-state text** (`""`/`on`/`off`) so the COALESCE upsert keeps an explicit choice and empty means "default by mode".
9. **Escalation "needs the user"** is the `ci_escalation` notification (nl-5) plus `CIStatus.Escalated` setting nl-2's `item.escalated`. Escalation clears when the check passes or the user toggles `w`.
10. **`PollFrequencySecs <= 0` means 300 s**, matching the wizard default (`dashboard.go:5982`). The 30 s daemon tick is the floor.
11. **Trigger covers pty sessions too.** `monitorPanes` and `triggerPrompt` lose their `TmuxSession != ""` filters. Scheduled prompts advance `LastRunAt` only after a successful enqueue.
12. **Notifier:** nl-5's `Emit(ctx, sessionID, label, kind, detail)`, declared consumer-side in `ciwatch`; the label comes from `notify.Label`.
