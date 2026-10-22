# NL-2: Dashboard A1 (TUI side of W1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the sessions tab into layout A1: quiet rows with glyph and to-do progress, a `needs you` header, a detail pane with meta line, one compact git/PR line, to-do list and waiting question, a prompt line that steers through serve, and a to-do focus mode that edits the list over SSH as `as: "user"`.

**Architecture:** All new UI code lives in small files under `internal/ui` (`todo_*.go`, `a1_*.go`); `dashboard.go` and `session_list.go` only gain wiring. The UI talks to serve through a consumer-defined `todoClient` interface whose production adapter calls nl-1's `usecase.ServeRPC` (one SSH round trip through the allowlisted `aiman rpc`, params base64-encoded so no shell parses user text). Event handling for `todo_changed`/`steer` is a separate path in `applySessionEvent` that never queues a pane refresh.

**Tech Stack:** Go 1.26, `charm.land/bubbletea/v2`, `charm.land/bubbles/v2` (list, textinput, viewport), `charm.land/lipgloss/v2`, `github.com/charmbracelet/x/ansi`.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (sections "Noise budget", "Shared interfaces", "W1", "Dashboard: layout A1"). Mockup A1: https://claude.ai/artifact/UB1zh3xsG688W7YS57iyZq. Backend counterpart: `docs/superpowers/plans/2026-10-22-nl-1-sessions-todos-steering.md`. **Depends on nl-1** (types, `server.SteerStatus`, `SessionEvent` fields, `todo.summary`, `steer.send`, `aiman rpc`/`usecase.ServeRPC`); it lands first.

## Global Constraints

- Bubble Tea v2 only (`charm.land/...`); never import `github.com/charmbracelet/bubbletea|bubbles|lipgloss` v1.
- Keys are matched with `msg.String()`; space is `"space"`. Tests build keys only with `pressKey`/`pressRune` from `internal/ui/keystrokes_test.go`.
- Sub-models render via `viewString()`; only the program model implements `View() tea.View`.
- `make ci` green before every commit; `TestOllamaIntelligenceSummariseSession` is a known failure.
- Laptop reaches remotes only over SSH; every call must finish under 30 s (`ssh.Manager.Execute`).
- Noise budget, verbatim: header `aiman · <host>` and `<n> sessions · <k> needs you`; row = glyph, ticket, name, progress bar, `n/m`; detail = one dim meta line (`agent model · elapsed · branch · ~$cost · cache %` · `reviewing…` · `watching PR #n`), one compact git/PR line (only when there is git or PR state), the to-do list, the waiting question only when present, then the pane preview.
- Glyphs: `●` waiting for you, `◐` working, `✓` done, `✗` failed, `○` idle. To-do marks come from `domain.TodoState.Glyph()` (nl-1): done dim `✓`, active bold `▸`, pending `○`, dropped `–` struck; plus `⚠ not in agent's list` for ids in `TodoList.NotInAgent`.
- Sessions without a list: `no to-do list yet`, no bar.
- Prompt line: `› Reply to <ticket>…` with a `queued`/`delivered` marker after sending.
- Author for every UI write is `as: "user"`.
- `todo_changed` and `steer` events never trigger a pane refresh.
- Existing main-view keys keep their meaning.
- Mockup palette (A1): wait `#e0a83a`, work `#6ea8dc`, done `#86b874`, prompt `#b994dc`, dim `#646e69`, faint `#3a4240`, selected row background `#222930`, bold text `#eef1ee`.

## Chosen keys

`i` (AI classify, `dashboard.go:801`, `4940`) and `t` (tunnels, `dashboard.go:792`, `5131`) are taken, so per the spec's fallback rule:

| Key | Where | Action |
|---|---|---|
| `ctrl+p` | main view, sessions tab | focus prompt line |
| `enter` / `esc` | prompt focus | send / leave |
| `ctrl+t` | main view, sessions tab | focus to-do list |
| `up`/`k`, `down`/`j` | to-do focus | move cursor |
| `a` `e` `space` `s` `d` `x` `J` `K` `esc` | to-do focus | add, edit, toggle done, set active, drop, delete, move down, move up, back |
| `?` | main view | toggle full key list in place of the hint line |

`ctrl+p` and `ctrl+t` have no binding anywhere in `internal/ui` today (verified with grep). The bubbles list keymap does not use them.

## Review Focus

1. Serve on a remote is older than nl-1 (`usecase.ErrRPCUnsupported`): rows show no bar, the detail says `to-dos unavailable on this remote`, nothing retries per event or toasts. Test in Task 4.
2. Prompt or to-do text containing quotes, `$()`, backticks, newlines or non-ASCII must reach serve byte-for-byte and never be shell-parsed. Pinned by nl-1 `TestServeRPCNeverShellParsesText` (the transport this plan uses).
3. A stale `todo.get` result (older rev, or for a session no longer selected) must not overwrite a newer cached list or the visible detail. Test in Task 4.
4. Narrow terminals (80 cols) and long to-do texts: rows and to-do lines truncate with `…` and the view never exceeds the terminal. Tests in Tasks 5 and 6.
5. The selected session disappears (terminated, filtered, or cursor lands on a group header) while prompt or to-do focus is active: focus drops back to the list without a panic and no write is sent to the wrong session. Test in Task 8.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/ui/todo_state.go` | cached per-session to-do snapshot and summary, steer status, progress maths, prompt marker |
| `internal/ui/todo_client.go` | `todoClient` interface, ops, SSH adapter |
| `internal/ui/todo_fetch.go` | fetch commands, messages, event handling, sweep |
| `internal/ui/a1_style.go` | A1 palette and glyph styles |
| `internal/ui/a1_rows.go` | row and header rendering, needs-you count |
| `internal/ui/a1_detail.go` | detail pane: title, meta line, git/PR line, to-dos, question |
| `internal/ui/a1_prompt.go` | focus enum, prompt line, hint line |
| `internal/ui/a1_todo_focus.go` | to-do focus key handling |
| `internal/ui/dashboard.go`, `session_list.go`, `session_events.go` | wiring only |

---

### Task 1: UI to-do state

**Files:**
- Create: `internal/ui/todo_state.go`
- Test: `internal/ui/todo_state_test.go`

**Interfaces:**
- Consumes (nl-1, already landed): `domain.TodoList` (JSON-tagged, with `NotInAgent`), `domain.TodoItem`, `domain.TodoState` consts and `Glyph()`, `domain.Author*`; `server.SteerStatus{Queued, LastDeliveredAt, LastError, Held, Pending}`; `server.SessionEvent{Rev int64, Queued *int, DeliveredAt string, Kind, State}`.
- Produces:
  - `type todoSummary struct { Done, Total int; Active string; Rev int64 }` (decoded from `todo.summary`)
  - `type todoSnapshot struct { List domain.TodoList; Steer server.SteerStatus; HasList bool }` (`HasList` true when `List.Items` is non-empty or `List.Rev > 0`)
  - `func todoProgress(l domain.TodoList) (done, total int)` (dropped items excluded, same rule as `todo.summary`)
  - `func promptMarker(sentAt time.Time, st server.SteerStatus) string` returning `""`, `"queued"`, `"delivered"` or `"not delivered"`.

- [ ] **Step 1: Write the failing tests**

```go
func TestTodoProgressIgnoresDropped(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{
		{ID: "t1", State: domain.TodoDone}, {ID: "t2", State: domain.TodoActive},
		{ID: "t3", State: domain.TodoPending}, {ID: "t4", State: domain.TodoDropped},
	}}
	done, total := todoProgress(l)
	if done != 1 || total != 3 { t.Fatalf("got %d/%d, want 1/3", done, total) }
}

func TestPromptMarker(t *testing.T) {
	sent := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	cases := []struct{ name string; st server.SteerStatus; want string }{
		{"queued", server.SteerStatus{Queued: 1}, "queued"},
		{"delivered after send", server.SteerStatus{LastDeliveredAt: sent.Add(time.Second)}, "delivered"},
		{"delivery older than send", server.SteerStatus{LastDeliveredAt: sent.Add(-time.Minute)}, "queued"},
		{"agent ended", server.SteerStatus{Queued: 1, Held: "ended"}, "not delivered"},
	}
	if got := promptMarker(time.Time{}, server.SteerStatus{Queued: 1}); got != "" { t.Fatalf("unsent: %q", got) }
	for _, c := range cases {
		if got := promptMarker(sent, c.st); got != c.want { t.Errorf("%s: got %q want %q", c.name, got, c.want) }
	}
}
```

- [ ] **Step 2:** `go test ./internal/ui -run 'TestTodoProgress|TestPromptMarker'` → FAIL (undefined).
- [ ] **Step 3: Implement** `todo_state.go`. `promptMarker`: zero `sentAt` → `""`; `Held == "ended"` → `"not delivered"`; `Queued == 0` and `!LastDeliveredAt.Before(sentAt)` → `"delivered"`; else `"queued"`.
- [ ] **Step 4:** same command → PASS; `go build ./...`.
- [ ] **Step 5: Commit** `feat(ui): to-do snapshot state`.

---

### Task 2: (moved to nl-1 Task 3a)

`aiman rpc` and its allowlist are owned by nl-1 Task 3a, together with the laptop helper `usecase.ServeRPC`. The methods this plan calls (`todo.get`, `todo.summary`, `todo.add`, `todo.update`, `todo.move`, `todo.remove`, `steer.send`) are already allowlisted there. Nothing to do; the task number is kept so later references stay valid.

---

### Task 3: `todoClient` and its SSH adapter

**Files:**
- Create: `internal/ui/todo_client.go`
- Test: `internal/ui/todo_client_test.go` (also defines `fakeTodoClient` used by later tasks)

**Interfaces:**
- Consumes: Task 1 types; `config.Remote`; `ssh.NewManager(ssh.Config{Host, User, Root})`; `usecase.ServeRPC`, `usecase.ErrRPCUnsupported`, `usecase.ErrServeNotRunning` (nl-1 Task 3a).
- Produces:

```go
type todoOp struct {
	Method string
	Params map[string]any // never contains "as"; the client adds it
}
func opAdd(session, text, after string) todoOp            // todo.add {session,text,after?}
func opUpdateText(session, id, text string) todoOp        // todo.update {session,id,text}
func opSetState(session, id string, st domain.TodoState) todoOp // todo.update {session,id,state}
func opMove(session, id string, toIndex int) todoOp       // todo.move {session,id,to_index}
func opRemove(session, id string) todoOp                  // todo.remove {session,id}

type todoClient interface {
	Get(ctx context.Context, remote config.Remote, sessionID string) (todoSnapshot, error)
	Summary(ctx context.Context, remote config.Remote, sessionIDs []string) (map[string]todoSummary, error) // nil ids = all
	Apply(ctx context.Context, remote config.Remote, op todoOp) error
	Prompt(ctx context.Context, remote config.Remote, sessionID, text string) (server.SteerStatus, error)
}

type rpcFunc func(ctx context.Context, remote usecase.TerminalExecutor, method string, params any) (json.RawMessage, error)
type sshTodoClient struct{ dial func(config.Remote) usecase.TerminalExecutor; rpc rpcFunc } // rpc defaults to usecase.ServeRPC
func newSSHTodoClient() *sshTodoClient
```

  - Wire shapes (nl-1 Task 13): `todo.get` → `{"type":"todo_list","list":<domain.TodoList incl. not_in_agent>,"steer":<server.SteerStatus>}`; `todo.summary` → `{"type":"todo_summary","sessions":{"<id>":{"done","total","active","rev"}}}`; `steer.send` → `{"type":"steer_status","steer":…}`.
  - `Prompt` sends `steer.send {session, text, as:"user", mode:"note"}` and decodes `steer`.
  - Every call adds `as: "user"` except `todo.get`/`todo.summary` (reads), and wraps errors as `fmt.Errorf("%s: %w", method, err)` so `errors.Is(err, usecase.ErrRPCUnsupported)` still works.

- [ ] **Step 1: Write the failing tests** using a recording `rpcFunc` fake:
  - `TestSSHTodoClientAddsUserAuthor`: for each of `opAdd`, `opUpdateText`, `opSetState`, `opMove`, `opRemove`, `Prompt`, the recorded params have `as == "user"`; `Prompt` uses method `steer.send` with `mode == "note"`.
  - `TestSSHTodoClientGetDecodes`: rpc returns `json.Marshal` of `{"type":"todo_list","list":<domain.TodoList{SessionID:"s1",Rev:4,Items:[t1 active],NotInAgent:["t1"]}>,"steer":{"queued":2}}` → `List.Rev==4`, `Steer.Queued==2`, `List.NotInAgent == ["t1"]`, `HasList`.
  - `TestSSHTodoClientSummaryDecodes`: `{"sessions":{"a":{"done":3,"total":7,"active":"Run preprod","rev":9}}}` → `map["a"] == todoSummary{3,7,"Run preprod",9}`; `nil` ids sends no `sessions` key.
  - `TestSSHTodoClientSurfacesErrors`: rpc returns `usecase.ErrRPCUnsupported` → `errors.Is(err, usecase.ErrRPCUnsupported)` and the message names the method.
- [ ] **Step 2:** `go test ./internal/ui -run TestSSHTodoClient` → FAIL.
- [ ] **Step 3: Implement** `todo_client.go`; add `fakeTodoClient` in the test file recording `[]todoOp`, prompts, summary calls, and returning a configurable `todoSnapshot`/summary/error per session.
- [ ] **Step 4:** → PASS.
- [ ] **Step 5: Commit** `feat(ui): todo client over aiman rpc with user authorship`.

---

### Task 4: Fetching and events

**Files:**
- Create: `internal/ui/todo_fetch.go`
- Modify: `internal/ui/dashboard.go` Model struct (`:351`) fields; `NewModel` (`:774`) sets `todoClient: newSSHTodoClient()`; global routing block (`:2775-2797`) handles the new messages; selection change in `forwardToFocused` (`:4453-4469`) and the first-tick branch of `applyTmuxTick` (`:4179-4189`) append `m.fetchTodosCmd(s)`.
- Modify: `internal/ui/session_events.go` `sessionStream` (`:56`) gains `todoSwept bool`; `applySessionEvent` (`:143`) calls `applyTodoEvent` before the `eventSeen` logic and returns early when it claims the event.
- Test: `internal/ui/todo_fetch_test.go`

**Interfaces:**
- Consumes: `todoClient` (Task 3), `todoSnapshot`/`todoSummary` (Task 1), `server.SessionEvent.Rev/Queued/DeliveredAt` (nl-1: `Queued *int`, `DeliveredAt` RFC3339Nano), `resolveRemote(cfg, s)`.
- Produces (Model fields): `todoClient todoClient`, `todos map[string]todoSnapshot` (full lists, fetched for the selected session), `todoSummaries map[string]todoSummary` (every session, for rows), `todoInFlight map[string]bool`, `todoUnsupported map[string]bool` (by host), `promptSent map[string]time.Time`.
- Produces (funcs and messages):

```go
type todoLoadedMsg struct { host, sessionID string; snap todoSnapshot; err error }
type todoSweepMsg  struct { host string; summaries map[string]todoSummary; err error }
func (m *Model) fetchTodosCmd(s domain.Session) tea.Cmd       // nil if host unsupported, in flight, or creating/terminating
func (m *Model) applyTodoEvent(host string, ev server.SessionEvent) (tea.Cmd, bool)
func (m *Model) applyTodoLoaded(msg todoLoadedMsg)
func (m *Model) sweepTodosCmd(host string, ids []string) tea.Cmd // one todo.summary call; nil ids = every session on host
func (m *Model) applyTodoSweep(msg todoSweepMsg)
func (m *Model) refreshTodoProgress(id string)               // updates list items in place, like markSessionBusyFromEvent
```

Rules: `todo_changed` with `ev.Rev` newer than the cached rev → for the selected session `fetchTodosCmd` (full list); for any other session `sweepTodosCmd(host, []string{id})` (one-entry summary, so rows stay current without fetching whole lists); `steer` → set `Steer.Queued = *ev.Queued` (when non-nil) and `Steer.LastDeliveredAt` from `ev.DeliveredAt` in place, no fetch; both return claimed `true` and never call `queuePreview`. `applyTodoLoaded` ignores a snapshot whose `List.Rev` is lower than the cached one, and also refreshes `todoSummaries[id]` from the list. `applyTodoSweep` merges summaries, ignoring entries whose `Rev` is lower than cached. An error with `errors.Is(err, usecase.ErrRPCUnsupported)` or `usecase.ErrServeNotRunning` sets `todoUnsupported[host]`; it is cleared when that host's stream restarts (`applyStreamEnded`). The full sweep (`ids == nil`) runs once per stream, on the first event of any type (keepalive included), which proves serve is up.

- [ ] **Step 1: Write the failing tests** (model from `eventModel` in `session_events_test.go` plus `m.todoClient = fake`; run returned cmds directly, never the stream's `next` wait):
  - `TestTodoChangedFetchesWithoutPaneRefresh`: `applyTodoEvent("regent0", {Type:"todo_changed", ID:"p", Rev:3})` → claimed, cmd non-nil; running it yields `todoLoadedMsg{sessionID:"p"}`; `m.eventSeen["p"].lastRefresh.IsZero()` stays true after `applySessionEvent` with the same event.
  - `TestTodoChangedIgnoresOldRev`: cached Rev 5, event Rev 4 → claimed, cmd nil.
  - `TestSteerEventUpdatesStatusWithoutFetch`: `{Type:"steer", ID:"p", Queued:&zero, DeliveredAt:"2026-10-22T10:00:01Z"}` → `m.todos["p"].Steer.LastDeliveredAt` set, fake recorded no `Get`.
  - `TestStaleTodoResultDoesNotOverwrite`: cached Rev 7, `applyTodoLoaded` with Rev 6 → cached Rev still 7.
  - `TestOldServeMarksHostUnsupported`: fake `Get` returns `fmt.Errorf("todo.get: %w", usecase.ErrRPCUnsupported)` → `todoUnsupported["regent0"]`; next `fetchTodosCmd` returns nil; no toast queued.
  - `TestTodoChangedForOtherSessionFetchesSummary`: event for a non-selected id → the fake recorded one `Summary([id])` and no `Get`.
  - `TestFirstEventSweepsOnce`: two keepalives → exactly one `Summary(nil)` call.
  - `TestSelectionChangeFetchesTodos`: `NewModel` with two sessions on one remote, fake client, `SetSize(140, 44)`, select the first row; `m.Update(pressKey("down"))` → `m.todoInFlight[<second id>]` is true (the selection path called `fetchTodosCmd`).
- [ ] **Step 2:** `go test ./internal/ui -run 'TestTodo|TestSteer|TestStale|TestOldServe|TestSelectionChangeFetches|TestFirstEventSweeps'` → FAIL.
- [ ] **Step 3: Implement** `todo_fetch.go` and the wiring listed under Files. Each `Get` runs with `context.WithTimeout(…, 30*time.Second)`.
- [ ] **Step 4:** → PASS; `go test ./internal/ui` (existing event tests still pass).
- [ ] **Step 5: Commit** `feat(ui): fetch to-dos on selection and todo_changed; steer events update status`.

---

### Task 5: A1 rows and header

**Files:**
- Create: `internal/ui/a1_style.go`, `internal/ui/a1_rows.go`
- Modify: `internal/ui/dashboard.go:255-268` (`item` gains `todoDone, todoTotal int; hasTodos bool; escalated bool`), `makeItem` (`:557`) fills the to-do fields from `m.todoSummaries` (`escalated` is set by nl-6 from its CI status cache); `NewModel` (`:780`) sets `l.SetShowTitle(false)`, `l.SetShowStatusBar(false)`, `l.SetShowHelp(false)`, delegate `ShowDescription=false`, `SetSpacing(0)`.
- Modify: `internal/ui/session_list.go:326-346` `Render` calls `renderA1Row` for session rows (headers unchanged). `plainTitle`/`chrome` stay: they are the filter value and are covered by existing tests.
- Test: `internal/ui/a1_rows_test.go`

**Interfaces:**
- Consumes: `item`, `todoSummary`, palette.
- Produces:

```go
func rowGlyph(it item) (string, color.Color)
func progressBar(done, total, width int, fill color.Color) string // "━" filled in fill colour, rest faint
func renderA1Row(it item, selected bool, width int) string
func needsYouCount(items []list.Item) int                        // rows whose glyph is ● or ✗
func renderA1Header(host string, sessions, needsYou int, scanning bool, width int) string
func (m *Model) a1HostLabel() string
```

Rules: glyph `●` when `needsInput || escalated`; `✗` for activity `create-failed`, `exited`, `stale`; `✓` when `AgentEnded`, or idle with `hasTodos && todoDone == todoTotal && todoTotal > 0`; `◐` for `busy`, `creating`, `bgwait`, `terminating`; else `○`. Columns: indent (`" "` at depth 0, `strings.Repeat("  ", depth)+"└ "` below), glyph, space, ticket (`IssueKey`, padded/truncated to 9), two spaces, name (`Name`, else `TmuxSession`; plus `childBadge()` only when `collapsed`), bar right-aligned with width `clamp(width/5, 6, 14)`, two spaces, dim `n/m`. Under 30 columns drop the bar and keep `n/m`. No list: no bar, no `n/m`. Selected row: background `#222930` across the full width, ticket bold. Header: left `aiman` bold + dim ` · <host>`; right dim `<n> sessions · ` + `<k> needs you` in wait colour when k>0 else dim; while `discoveryPending`, right side is dim `scanning…`. Host label: filter set → `remoteNameForHost`; one remote → its name; otherwise `all remotes`.

- [ ] **Step 1: Write the failing tests** (strip ANSI with `ansi.Strip`):
  - `TestRowGlyphMapping` (table over the rules above, including `escalated` → `●`).
  - `TestA1RowShowsProgress`: `item{session:{IssueKey:"WTB-2445", Name:"Sundae standby"}, needsInput:true, hasTodos:true, todoDone:3, todoTotal:7}` at width 60 → contains `●`, `WTB-2445`, `Sundae standby`, `━`, `3/7`; does not contain `input` or `⚠`.
  - `TestA1RowWithoutListHasNoBar`: no `━`, no `/`.
  - `TestA1RowFitsNarrowWidth`: widths 20, 26, 40 with a 60-rune name → `lipgloss.Width(out) <= width`, contains `…`.
  - `TestA1HeaderNeedsYou`: `renderA1Header("regent0", 4, 1, false, 100)` stripped starts with `aiman · regent0` and ends with `4 sessions · 1 needs you`; width ≤ 100.
- [ ] **Step 2:** `go test ./internal/ui -run 'TestRowGlyph|TestA1Row|TestA1Header'` → FAIL.
- [ ] **Step 3: Implement**; `refreshTodoProgress` (Task 4) now also updates `todoDone/todoTotal/hasTodos` on the matching list item from `m.todoSummaries`.
- [ ] **Step 4:** → PASS. Update `TestSessionListDelegatePaintsState` (`session_list_test.go:83`) only if it asserted on the removed description line; keep its colour intent by asserting the glyph colour.
- [ ] **Step 5: Commit** `feat(ui): A1 session rows with progress bars and needs-you header`.

---

### Task 6: A1 detail pane and layout

**Files:**
- Create: `internal/ui/a1_detail.go`
- Modify: `internal/ui/dashboard.go`: `renderMainView` (`:7008-7088`) puts `renderA1Header` on top and drops the left border (`:7035-7038`); the PR buttons (`:7066-7081`) move into the git/PR line instead of their own block; `mainPanelFrame` (`:6993`) becomes `2` (padding only) with its comment updated; `renderSessionPanel` (`:7091-7184`) renders `renderSessionDetail` then the preview viewport; `renderGitLines` (`:7186-7298`) is replaced by `renderGitPRLine`, which keeps the same information (branch ahead/behind and dirty state, PR number, state, review decision, `CI:<checks summary>`, the "No open PR" case) on one compact line. Git status fetching and `m.gitStatus` stay untouched. `SetSize` (`:2685-2709`): replace `mainChrome = 7` with `a1Chrome = 4` (header, gap, prompt line, hint line) and `compactMainUpperBudget = 12` with `a1DetailBudget = 16` (title, meta, git/PR line, gap, up to 8 to-do rows, up to 3 question rows, gap, preview caption).
- Test: `internal/ui/a1_detail_test.go`; update `render_all_states_test.go:73` and `session_list_test.go:175`.

**Interfaces:**
- Consumes: `todoSnapshot`, `item`, palette, `m.metaExtras`, `m.gitStatus` (existing, incl. `PullRequest`).
- Produces:

```go
type detailMetaExtras struct { CostUSD float64; HasCost bool; Tokens int64; CacheRatio float64; HasCache bool; Reviewing bool; WatchingPR int }
// m.metaExtras map[string]detailMetaExtras: filled by later plans (nl-4 cost/tokens/cache, nl-6 WatchingPR, nl-7 Reviewing); empty here.
func renderMetaLine(s domain.Session, x detailMetaExtras, now time.Time, width int) string
func shortElapsed(d time.Duration) string // "42m", "3h", "2d"; under a minute "now"
type todoView struct { Snap todoSnapshot; Cursor int /* -1 unfocused */; Question []string; Input string /* rendered add/edit line, "" if none */ }
const maxTodoRows = 8
func renderTodoLines(v todoView, width int) []string
func waitingQuestion(it item, width int) []string // nil unless HookState == domain.AgentStateWaitingInput && HookStateMessage != ""; wrapped, max 3 lines
func renderGitPRLine(st domain.GitStatus, width int) string // "" when there is neither git nor PR state
func (m *Model) renderSessionDetail(it item, width int) string
```

Rules: title line bold `<IssueKey> · <Name>` (either part alone if the other is empty). Meta line, dim, parts joined by ` · `, skipped when empty: `AgentName` + ` ` + `AgentModel`; `shortElapsed(now - CreatedAt)` (skip zero); `Branch`; `~$%.2f` if `HasCost`, else `<n>k tok` / `<n>M tok` when `Tokens > 0` (unpriced models such as Codex show tokens, never `~$`); `cache %d%%` if `HasCache`; `reviewing…`; `watching PR #n` if `WatchingPR > 0`. Git/PR line, dim, directly under the meta line: `↑a↓b · <n> changed · PR #n <state> <review> · CI:<summary>` with the existing colours for review and checks, then the three PR labels (`Review PR`, `Approved`, `Request Changes`) compact at the end when a PR exists; it truncates from the left part first so the PR state stays visible; omitted when `renderGitPRLine` returns `""`. To-do rows: done = dim `✓` + dim text; active = wait-coloured `▸` + bold text; pending = dim `○` + text; dropped = dim `–` + struck dim text; ids in `List.NotInAgent` append dim wait-coloured ` ⚠ not in agent's list`. Question lines are `│ <line>` in wait colour, indented two spaces, placed directly under the active item, or after the list when nothing is active. More than `maxTodoRows` items: show a window that contains the cursor (or the active item when unfocused) and use the last row for dim `+k more`. No list → dim `no to-do list yet`; host in `todoUnsupported` → dim `to-dos unavailable on this remote`. Cursor row (focus mode) gets the selected-row background. All lines truncated to `width` with `…`.

- [ ] **Step 1: Write the failing tests:**
  - `TestMetaLineSkipsMissingFields`: session `{AgentName:"claude", AgentModel:"opus", Branch:"wtb-2445-standby", CreatedAt: now-42m}`, empty extras → stripped equals `claude opus · 42m · wtb-2445-standby`; with `{HasCost:true, CostUSD:1.234, HasCache:true, CacheRatio:0.81, WatchingPR:12}` → ends with `~$1.23 · cache 81% · watching PR #12`; with `{Tokens: 412000, HasCache:true, CacheRatio:0.5}` → ends with `412k tok · cache 50%` and has no `$`.
  - `TestGitPRLineCompact`: git status ahead 2, behind 0, 3 changed, PR #12 OPEN approved, checks `3/3` → one line containing `↑2↓0`, `PR #12`, `CI:3/3`, `Review PR`; no PR and clean tree → `""`; width 40 → fits and still shows `PR #12`.
  - `TestTodoLinesRenderStates`: items done/active/pending/dropped plus `List.NotInAgent = ["t4"]` → stripped lines `✓ Add standby template`, `▸ Run preprod --check`, `○ Open PR`, `– Old idea ⚠ not in agent's list`; the dropped line's raw output contains the strikethrough SGR (`\x1b[9m`) or lipgloss's equivalent (assert `strings.Contains(raw, "9m")`).
  - `TestQuestionSitsUnderActiveItem`: question `["Standby is 5.59M slots behind.", "Proceed with handover? (y/n)"]` → the two `│` lines come right after the `▸` line.
  - `TestQuestionOnlyWhenWaiting`: `HookState: domain.AgentStateIdle` with a message → `waitingQuestion` nil.
  - `TestTodoWindowKeepsCursorVisible`: 12 items, cursor 10 → 8 lines, includes item 10's text and a `+` more line.
  - `TestNoListYet`: empty snapshot → `no to-do list yet`.
  - Update `TestMainViewShowsSessionAndGitDetail` → rename `TestMainViewShowsA1Detail`, expect `AIM-1`, `Claude Code`, `feature/x`, `no to-do list yet`, `agent output line 1`, `aiman ·`, and the PR number from the fixture's git status; drop the `Git` heading. Update `TestRenderSessionPanelSyncIsNA` to assert the detail renders without the sync/N/A strip (the noise budget removes it; git/PR info stays).
- [ ] **Step 2:** `go test ./internal/ui -run 'TestMetaLine|TestGitPRLine|TestTodoLines|TestQuestion|TestTodoWindow|TestNoListYet|TestMainViewShowsA1Detail'` → FAIL.
- [ ] **Step 3: Implement** `a1_detail.go` and the `dashboard.go` changes above.
- [ ] **Step 4:** `go test ./internal/ui` → PASS, including `TestDashboardFitsItsTerminal`, `TestMainPanelFrameMatchesTheStyle`, `TestPreviewLinesDoNotWrapInThePanel`, `TestMainViewRowFitsTheTerminal`, `TestEveryViewStateRendersWithoutPanic`. Add `{80, 24}` to the size table in `TestDashboardFitsItsTerminal`.
- [ ] **Step 5: Commit** `feat(ui): A1 detail pane with meta line, git/PR line, to-dos and waiting question`.

---

### Task 7: Prompt line, hint line and `?`

**Files:**
- Create: `internal/ui/a1_prompt.go`
- Modify: `internal/ui/dashboard.go`: Model gains `focus a1Focus`, `promptInput textinput.Model`, `showKeys bool`; `handleMainKeyMsg` (`:4494`) routes to `handleFocusedKey` first when `m.focus != focusList`, and handles `ctrl+p` and `?` on the sessions tab; add `"ctrl+p", "ctrl+t"` to both blocked-key lists (`:4527`, `:4537`); `renderMainView` replaces `helpText`/`helpBar` (`:7042-7064`) with the prompt line and hint line; `AdditionalFullHelpKeys` (`:782-808`) gains `ctrl+p` "reply to session" and `ctrl+t` "edit to-dos"; non-key messages reach `promptInput.Update` while prompt focus is active (in `handleMainUpdate`, before `forwardToFocused`).
- Test: `internal/ui/a1_prompt_test.go`

**Interfaces:**
- Consumes: `todoClient.Prompt`, `promptMarker`, `m.promptSent`, `m.todos`.
- Produces:

```go
type a1Focus int
const ( focusList a1Focus = iota; focusPrompt; focusTodos )
type promptSentMsg struct { host, sessionID string; sentAt time.Time; steer server.SteerStatus; err error }
func (m *Model) enterPromptFocus() tea.Cmd
func (m *Model) handleFocusedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool)
func (m *Model) handlePromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd)
func (m *Model) applyPromptSent(msg promptSentMsg) tea.Cmd   // routed globally next to todoLoadedMsg
func renderPromptLine(ticket, input string, focused bool, marker string, width int) string
func (m *Model) hintLine(width int) string
```

Rules: unfocused prompt line is prompt-coloured `›` + dim `Reply to <ticket or name>…`, plus the marker for the selected session (`queued` dim, `delivered` done colour, `not delivered` wait colour). Focused: `›` + the textinput. `enter` with non-blank text sends via `Prompt` (`steer.send`, mode `note`), records `promptSent[id] = sentAt`, clears the input, stays focused, shows `sending…` until `promptSentMsg`; blank `enter` does nothing. `esc` leaves focus (input kept). On error: toast `⚠️  Reply not sent: <err>` (3 s) and the text is restored. `applyPromptSent` stores the returned `server.SteerStatus` into `m.todos[id].Steer`. Hint line (dim, version right-aligned as today): list focus `ctrl+p reply · ctrl+t to-dos · a attach · ? keys`; prompt focus `enter send · esc back`; to-do focus `a add · e edit · space done · s start · d drop · x delete · J/K move · esc back`. Daemons tab keeps today's agent API help text. `?` toggles `showKeys`, which renders today's long `helpText` (wrapped to two lines max) in place of the hint line.

- [ ] **Step 1: Write the failing tests** (drive keys through `m.Update(pressKey(...))`):
  - `TestCtrlPFocusesPrompt`: `pressKey("ctrl+p")` → `m.focus == focusPrompt`; typing `pressKey("hi there")` then `pressKey("enter")` → fake recorded one prompt `{session: id, text: "hi there"}`; `m.focus` still `focusPrompt`; `pressKey("esc")` → `focusList`.
  - `TestPromptFocusSwallowsMainKeys`: in prompt focus, `pressKey("n")` and `pressKey("q")` type into the input; `m.state` stays `viewStateMain`.
  - `TestBlankPromptNotSent`: `enter` on empty input → fake recorded nothing.
  - `TestPromptMarkerShownAfterSend`: after `applyPromptSent{steer: server.SteerStatus{Queued:1}}` the stripped main view contains `queued`; after a `steer` event with `Queued` 0 and `DeliveredAt` later than `sentAt`, it contains `delivered`.
  - `TestHintLineByFocus`: three focuses → the three hint strings above.
  - `TestCtrlPBlockedWhileCreating`: selected session in `creatingSessions` → `ctrl+p` shows the existing wait toast, focus unchanged.
- [ ] **Step 2:** `go test ./internal/ui -run 'TestCtrlP|TestPromptFocus|TestBlankPrompt|TestPromptMarkerShown|TestHintLine'` → FAIL.
- [ ] **Step 3: Implement.** Apply `applyCursorStyle(&m.promptInput)` (`views.go:22`).
- [ ] **Step 4:** `go test ./internal/ui` → PASS.
- [ ] **Step 5: Commit** `feat(ui): A1 prompt line steers the selected session`.

---

### Task 8: To-do focus mode

**Files:**
- Create: `internal/ui/a1_todo_focus.go`
- Modify: `internal/ui/dashboard.go` (`ctrl+t` in `handleMainKeyMsg`; `focusedSessionID string`, `todoCursor int`, `todoEdit *todoEditState`, `todoInput textinput.Model` on Model); `a1_prompt.go` `handleFocusedKey` dispatches `focusTodos`.
- Modify: `AGENTS.md` "Key TUI Patterns": one bullet for A1 focus modes (`ctrl+p`, `ctrl+t`) and that `todo_changed`/`steer` never refresh the pane.
- Test: `internal/ui/a1_todo_focus_test.go`

**Interfaces:**
- Consumes: `todoClient.Apply`, op constructors (Task 3), `fetchTodosCmd` (Task 4), `renderTodoLines` cursor (Task 6).
- Produces:

```go
type todoEditState struct { id string /* "" means add */; after string }
type todoMutatedMsg struct { host, sessionID string; err error }
func (m *Model) enterTodoFocus() tea.Cmd
func (m *Model) handleTodoKey(msg tea.KeyMsg) (tea.Model, tea.Cmd)
func (m *Model) todoMutateCmd(s domain.Session, op todoOp) tea.Cmd // Apply then Get; returns todoLoadedMsg, or todoMutatedMsg on error
func (m *Model) exitFocusIfSessionGone() // called at the top of handleFocusedKey and after applyRemoteFilter
```

Rules: entering focus records `focusedSessionID` and puts the cursor on the active item (else 0). Keys act on `items[cursor]` of the cached list for `focusedSessionID`: `space` toggles `done` ↔ `pending` (`opSetState`), `s` → `active`, `d` → `dropped`, `x` → `opRemove`, `J` → `opMove(id, cursor+1)`, `K` → `opMove(id, cursor-1)` (no-op at the ends; cursor follows the item), `e` opens `todoInput` prefilled with the text (enter → `opUpdateText` when changed; esc cancels), `a` opens an empty input inserted after the cursor (enter → `opAdd(text, after=id at cursor or "")`), `up`/`k`/`down`/`j` move the cursor, `esc` leaves focus. Keys on an empty list: only `a` and `esc` act. Mutation errors toast `⚠️  To-do not saved: <err>` (3 s). If the selected row is no longer a session or no longer `focusedSessionID`, focus returns to `focusList` and the key is not applied.

- [ ] **Step 1: Write the failing tests** (fake client with a 3-item list `t1 done`, `t2 active`, `t3 pending`; `pressKey`/`pressRune` only):
  - `TestCtrlTEntersTodoFocusOnActiveItem`: cursor == 1.
  - `TestTodoKeysSendOps` (table): `space` on t3 → `todo.update {state: done}`; `space` on t1 → `{state: pending}`; `s` on t3 → `{state: active}`; `d` → `{state: dropped}`; `x` → `todo.remove {id}`; `J` on t2 → `todo.move {id: t2, to_index: 2}`; `K` on t2 → `{to_index: 0}`; `K` on t1 → no op recorded.
  - `TestTodoAddAndEdit`: `pressRune('a')`, `pressKey("Write runbook")`, `pressKey("enter")` → `todo.add {text:"Write runbook", after:"t2"}`; `pressRune('e')` on t3, select-all is not needed: clear via `pressKey("ctrl+u")`, type `Open PR now`, `enter` → `todo.update {id:t3, text:"Open PR now"}`; `esc` during edit sends nothing.
  - `TestTodoFocusEscReturnsToList` and `TestTodoFocusDoesNotAttach`: `pressRune('a')` in focus never yields `attachMsg`.
  - `TestTodoFocusDropsWhenSessionGone`: remove the session from `allSessions`, `applyRemoteFilter()`, then `pressKey("space")` → `m.focus == focusList`, fake recorded no op.
- [ ] **Step 2:** `go test ./internal/ui -run 'TestCtrlTEnters|TestTodoKeys|TestTodoAdd|TestTodoFocus'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Full gate:** `make ci` → green apart from `TestOllamaIntelligenceSummariseSession`. Then `graphify update .`.
- [ ] **Step 5: Manual check** with the `run` skill against a remote running an nl-1 serve: select a session, `ctrl+t`, add an item, confirm `.aiman_todo.md` changes on the remote and the row's `n/m` updates without the pane flickering; `ctrl+p`, send a line, see `queued` then `delivered`.
- [ ] **Step 6: Commit** `feat(ui): to-do focus mode edits the session's list as the user`.

---

## Spec deviations and cross-plan contract

1. **Keys.** `i` and `t` are taken (`dashboard.go:801`, `:792`), so `ctrl+p` and `ctrl+t` per the spec's fallback. The mockup's hint (`tab edit to-dos`) is also not used: `tab` switches to the Agent API tab (`dashboard.go:4654`).
2. **Prompt method.** The prompt line uses nl-1's `steer.send {session, text, as:"user", mode:"note"}`; `session.prompt` (`handlers_session.go:447`) types immediately and refuses `waiting_input`, so it is not the queue.
3. **`todo.get` result shape** is nl-1's: `{"type":"todo_list","list":<TodoList incl. not_in_agent>,"steer":<server.SteerStatus>}`. The UI decodes into nl-1's types directly (`internal/ui` already imports `internal/server`).
4. **`⚠ not in agent's list`** comes from `TodoList.NotInAgent` (nl-1).
5. **Laptop entry point** is nl-1's `aiman rpc` + `usecase.ServeRPC` (nl-1 Task 3a); this plan adds no CLI.
6. **Row progress** uses nl-1's batch `todo.summary`: one call per remote when its event stream first answers, and a one-session summary on `todo_changed` for non-selected sessions. Full lists are fetched only for the selected session.
7. **Noise budget versus existing UI.** The sync/N/A meta, activity text, agent title and the left border leave the main view. Kept: git/PR information and the PR labels, folded into one compact line under the meta line (the spec's noise budget allows "one compact git/PR line"); group headers; the tree indent for child sessions; the child count, which shows only when the children are collapsed and hidden.
8. **Header while scanning.** The right side shows `scanning…` until the first discovery lands. This replaces the list-title suffix the hidden title used to carry.
9. **Hooks for later plans:** `m.metaExtras` (nl-4 cost/tokens/cache, nl-6 `WatchingPR`, nl-7 `Reviewing`), `item.escalated` (nl-6), `todoView.RefGlyph` (nl-9). Not in this plan: hiding reviewer sessions (nl-7), toasts for needs-you transitions (nl-5).
