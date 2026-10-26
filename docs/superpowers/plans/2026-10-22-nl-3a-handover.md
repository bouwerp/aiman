# NL-3a Handover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve writes a real handover (`.aiman_handover.md` plus a `handover` context note) on archive, forget, restart, agent switch and Claude/Codex `PreCompact`, and restarts, agent switches and revives use it.

**Architecture:** A pure renderer (`internal/handover`) turns a gathered `handover.Input` into markdown. Serve (`internal/server/handover.go`) gathers the input locally (to-do store, steering queue, `git diff --stat`, hook title/message, local pane capture), writes the file only when it changed, and upserts one context note per session. Laptop-driven triggers (archive, restart, agent switch, revive) reach serve through new socket methods `handover.write` and `handover.point`, called over SSH through nl-1's `usecase.ServeRPC` (`aiman rpc`, allowlist extended here). `PreCompact` arrives through the existing hook reporter.

**Tech Stack:** Go 1.26, `internal/server` newline-JSON socket, `internal/agenthook`, `internal/usecase`, Bubble Tea v2 dashboard.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (sections Constraints, Shared interfaces, W6a). Companion plan: `docs/superpowers/plans/2026-10-22-nl-3b-memory.md` (W6b), which extends Task 3's writer.

**Depends on:** plan `nl-1-sessions-todos-steering` (W0 + W1): server fields `s.todos` (`todoStore`) and `s.steer` (`*steerer`), `Steerer.Enqueue`, `SteerStatus.Pending`, `todo.FormatList`, `domain.AimanScaffoldHeader`, `fsutil.WriteIfChanged`, `upsertClaudeHook(…, marker)`, `aiman rpc`/`usecase.ServeRPC`, `domain.Session.BaseBranch`, `domain.AuthorAiman`.

## Global Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only (`charm.land/...`). Never import v1 charmbracelet packages.
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- Laptop reaches remotes only over SSH. Laptop SSH calls time out at 30 s, so no socket method may block longer than that. `handover.write` runs under a 20 s context.
- Both terminal backends (tmux, pty) must work everywhere.
- Serve is the only writer of the new state. Other processes write through the socket.
- `.aiman_handover.md` uses the `.aiman_context.md` "DO NOT COMMIT" header (`contextstore.WriteSessionPack`, `internal/contextstore/files.go:540-555`) and the existing `.aiman_*` ignore rule (`domain.AimanGeneratedFileGlob`, `internal/infra/git/manager.go:220`). It is written only when its content changes, so it carries no timestamp.
- Import graph: `internal/gateway` → `internal/server` → `internal/usecase`. New usecase code must not import gateway or server.
- Handover is not written on `SessionEnd` (Claude fires it for `/compact` and `/clear`).
- Tests build key events through `pressKey`/`pressRune` (`internal/ui/keystrokes_test.go`).

## Review Focus

- Pane tail or diff containing a ```` ``` ```` run: the rendered file must still be valid markdown (fence longer than any backtick run inside). Test in Task 2.
- Session whose working directory is not a git repo, or `BaseBranch` empty and no `origin/HEAD`: handover is still written; the diff section reads `(unavailable)`. Test in Task 3.
- Remote serve too old to know `handover.write` (`unknown method`) or not running: restart falls back to today's agent-written summary. Test in Task 5.
- Two `PreCompact` reports with nothing changed in between: the file is not rewritten (mtime unchanged) and the note is not re-put. Test in Task 3.
- Session with no working directory or not found: `handover.write` returns `CodeInvalidParams` (no directory) or `CodeNotFound` (unknown) within the budget, never hangs. Test in Task 4.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/agenthook/merge.go` (modify) | `upsertCompactHook` registering `PreCompact` |
| `internal/agenthook/install.go` (modify) | Claude and Codex call it |
| `cmd/aiman/session_cmd.go` (modify) | report forwards `event` |
| `cmd/aiman/rpc_cmd.go` (modify) | allowlist `handover.write`, `handover.point` |
| `internal/handover/render.go` (create) | `Input`, `Render`, `NoteBody`, `Abstract`, fence helper |
| `internal/domain/session_files.go` (modify) | `AimanHandoverFileName` |
| `internal/server/handover.go` (create) | gather + write + note; `handover.write`, `handover.point` handlers |
| `internal/server/server.go` (modify) | dispatch cases |
| `internal/server/handlers_session.go` (modify) | `PreCompact` trigger in report; forget trigger |
| `internal/usecase/handover_remote.go` (create) | `WriteHandoverOnRemote`, `PrepareRestartHandoff`, `HandoverPrompt`, revive pointer |
| `internal/usecase/session_terminal.go` (modify) | `ReviveIfNeeded` sends pointer |
| `internal/ui/dashboard.go` (modify) | restart step 1 and archive use the handover |
| `internal/aimanskill/SKILL.md`, `AGENTS.md` (modify) | document handover |

---

### Task 1: Register `PreCompact` for Claude and Codex, and forward the hook event

**Files:**
- Modify: `internal/agenthook/merge.go:188-193` (`upsertIdentityHooks`) — add a sibling, do not change `upsertIdentityHooks` (Grok, Gemini, Copilot, Cursor share it).
- Modify: `internal/agenthook/install.go:54-62` (`ensureClaude`), `:81-89` (`ensureCodex`)
- Modify: `cmd/aiman/session_cmd.go:253-345` (`runReportAgentSession`, `reportNativeToServe`)
- Modify: `internal/server/handlers_session.go:753-799` (params struct only; trigger is Task 4)
- Test: `internal/agenthook/install_test.go`, `cmd/aiman/session_cmd_test.go`

**Interfaces:**
- Produces: `func upsertCompactHook(root map[string]any, command string) bool` (unexported); report params carry `"event": string`; server param struct field `Event string \`json:"event"\``; `agenthook.EventPreCompact = "PreCompact"`.

- [ ] **Step 1: Confirm Codex accepts `PreCompact`.** Run `strings "$(readlink -f "$(command -v codex)")" | grep -c '"PreCompact"'`. Expected: ≥ 1 (verified on codex-cli 0.163.0 while planning). If 0, register for Claude only and note it in the commit body.

- [ ] **Step 2: Write failing tests** in `internal/agenthook/install_test.go`:

```go
func TestEnsureOnHostRegistersPreCompactForClaudeAndCodexOnly(t *testing.T) {
    home := t.TempDir()
    for _, d := range []string{".claude", ".codex", ".copilot"} { mustMkdir(t, filepath.Join(home, d)) }
    if _, err := EnsureOnHost(home); err != nil { t.Fatal(err) }
    for _, f := range []string{".claude/settings.json", ".codex/hooks.json"} {
        hooks := readHooks(t, filepath.Join(home, f))
        if !hookHasMarker(hooks["PreCompact"]) { t.Errorf("%s: PreCompact missing", f) }
    }
    if _, ok := readHooks(t, filepath.Join(home, ".copilot/settings.json"))["PreCompact"]; ok {
        t.Error("copilot must not get PreCompact")
    }
}

func TestEnsureOnHostPreCompactIsIdempotent(t *testing.T) {
    // run EnsureOnHost twice; second run reports ActionCurrent for .claude/settings.json
    // and PreCompact has exactly one marked group.
}
```

Add helpers `readHooks` / `hookHasMarker` in the test file if the existing tests have none (check `install_test.go` first and reuse).

In `cmd/aiman/session_cmd_test.go`, assert the params sent to serve include `"event":"PreCompact"` for stdin `{"session_id":"abc","hook_event_name":"PreCompact","trigger":"auto"}`. Extract the param-building into `reportParams(sessionID string, r agenthook.Report) map[string]any` so it is testable without a socket, and test that function.

- [ ] **Step 3: Run** `go test ./internal/agenthook/ ./cmd/aiman/ -run 'PreCompact|ReportParams'`. Expected: FAIL (no PreCompact key; no `event` param).

- [ ] **Step 4: Implement.** `upsertCompactHook` calls nl-1's `upsertClaudeHook(root, "PreCompact", command, "", Marker)` (empty matcher matches `manual` and `auto`; `Marker` is the session reporter's marker, since `PreCompact` runs the same reporter script). `ensureClaude` and `ensureCodex` mutate with `upsertIdentityHooks(root, command) || upsertCompactHook(root, command)` written so both always run (no short-circuit). Copy `got.Event` into `rep.Event` in `runReportAgentSession`; `reportParams` adds `"event": r.Event`. Add `Event string \`json:"event"\`` to the server params and set `rep.Event`. The empty-report guards stay as they are: `PreCompact` payloads carry `session_id`, so they already pass.

- [ ] **Step 5: Run** the same tests. Expected: PASS. Then `make ci`.

- [ ] **Step 6: Commit** `feat(agenthook): register PreCompact for Claude and Codex and forward hook events`.

---

### Task 2: Handover renderer and change-only file writer

**Files:**
- Create: `internal/handover/render.go`
- Modify: `internal/domain/session_files.go` (add `AimanHandoverFileName = ".aiman_handover.md"`)
- Test: `internal/handover/render_test.go`

**Interfaces:**
- Consumes: `domain.TodoList`, `domain.TodoItem`, `domain.TodoState`, `domain.AimanScaffoldHeader`, `todo.FormatList` (nl-1).
- Produces:

```go
package handover

type Input struct {
    Label       string // session Name, else IssueKey, else ID
    Reason      string // "archive" | "forget" | "restart" | "agent_switch" | "pre_compact"
    Agent       string
    Branch      string
    BaseBranch  string
    Todos       *domain.TodoList // nil: no list
    Undelivered []string         // steering note texts, oldest first
    DiffStat    string           // "" renders "(unavailable)"
    HookTitle   string
    HookMessage string
    PaneTail    string           // already cleaned, ≤ 40 lines
}
const PaneLines = 40
func Render(in Input) string    // full file: DO NOT COMMIT header + NoteBody(in)
func NoteBody(in Input) string  // markdown without the header (context note body)
func Abstract(in Input) string  // active to-do text, else HookTitle, else "Handover at <reason>"
```

The file is written with nl-1's `fsutil.WriteIfChanged(path, content, 0o600)`; this plan adds no writer of its own.

- [ ] **Step 1: Write failing tests.**

```go
func TestRenderSections(t *testing.T) {
    in := Input{Label: "WTB-12", Reason: "restart", Agent: "claude", Branch: "wtb-12", BaseBranch: "main",
        Todos: &domain.TodoList{Items: []domain.TodoItem{
            {ID: "t1", Text: "Write test", State: domain.TodoState("done")},
            {ID: "t2", Text: "Fix parser", State: domain.TodoState("active")},
            {ID: "t3", Text: "Docs", State: domain.TodoState("pending")}}},
        Undelivered: []string{"[aiman] Your to-do list was changed by user:\n+ t4 Add rollback test"},
        DiffStat: " a.go | 3 ++-\n 1 file changed", HookTitle: "Fixing parser", PaneTail: "line1\nline2"}
    out := Render(in)
    for _, want := range []string{"DO NOT COMMIT", "# Handover: WTB-12", "Reason: restart",
        "t1 ✓ Write test", "t2 ▸ Fix parser", "t3 ○ Docs", "## Undelivered notes", "+ t4 Add rollback test",
        "## Changed files (git diff --stat main)", "a.go | 3 ++-", "Fixing parser", "## Last screen", "line2"} {
        if !strings.Contains(out, want) { t.Errorf("missing %q", want) }
    }
    if strings.Contains(NoteBody(in), "DO NOT COMMIT") { t.Error("note body must not carry the file header") }
}
func TestRenderOmitsUndeliveredWhenNone(t *testing.T)   // no "## Undelivered notes"
func TestRenderNoTodoList(t *testing.T)                // "No to-do list." when Todos == nil
func TestRenderDiffUnavailable(t *testing.T)           // DiffStat "" → "(unavailable)"
func TestRenderFenceLongerThanContent(t *testing.T)    // PaneTail "x\n```\ny" → fence is "````" and content intact
func TestRenderIsDeterministic(t *testing.T)           // Render(in) == Render(in); no time-dependent text
func TestAbstractPrefersActiveTodo(t *testing.T)       // "Fix parser"; no todos + HookTitle → HookTitle; neither → "Handover at restart"
```

To-do lines come from nl-1's `todo.FormatList` (the steering note's "Current list" format), so both outputs agree; dropped items, which `FormatList` omits, are appended as `tN – text (dropped)`.

- [ ] **Step 2: Run** `go test ./internal/handover/`. Expected: FAIL (package missing).

- [ ] **Step 3: Implement** in the section order the test pins: header, `# Handover: <Label>`, `Reason: … · Agent: … · Branch: … (base …)`, `## To-do list`, `## Undelivered notes` (only when any), `## Changed files (git diff --stat <base>)`, `## Last agent status` (Title / Message lines, only non-empty), `## Last screen (40 lines)`. The header is nl-1's `domain.AimanScaffoldHeader` (already shared with `WriteSessionPack`), so `handover` depends only on `domain` and `todo`.

- [ ] **Step 4: Run** `go test ./internal/handover/`. Expected: PASS.

- [ ] **Step 5: Commit** `feat(handover): render handover markdown`.

---

### Task 3: Serve gathers and writes the handover

**Files:**
- Create: `internal/server/handover.go`
- Test: `internal/server/handover_test.go`

**Interfaces:**
- Consumes: `handover.Render/NoteBody/Abstract/PaneLines` (Task 2); `fsutil.WriteIfChanged`; nl-1's server fields `s.todos` (`todoStore`) and `s.steer` (`*steerer`); `s.steer.Status(id).Pending []SteerNote` (nl-1 Task 11: undelivered notes, oldest first). `pane.Clean` (`internal/pane/clean.go:36`). `s.remote.CaptureTmuxPane`, `s.remote.Execute` (serve's local executor), `s.pty.CaptureScreen` (`internal/ptyruntime/runtime.go:368`).
- Produces:

```go
type handoverResult struct {
    Path    string `json:"path"`
    Written bool   `json:"written"`  // file content changed
    NoteID  string `json:"note_id"`
}
func (s *Server) writeHandover(ctx context.Context, sess domain.Session, reason string) (handoverResult, error)
func handoverNoteID(sessionID string) string // "handover-" + contextstore.SafeKey(sessionID)
func diffStat(ctx context.Context, ex interface{ Execute(context.Context, string) (string, error) }, dir, base string) string
```

- [ ] **Step 1: Write failing tests** (use `fakeRemote` from `fake_remote_test.go` with `pane` set; a `contextstore.NewFiles(t.TempDir())`; a stub to-do store and steerer implementing nl-1's interfaces):

```go
func TestWriteHandoverWritesFileAndNote(t *testing.T) {
    dir := t.TempDir()
    srv := newHandoverTestServer(t, paneText("$ go test\nFAIL parser"), todosWith("t2", "Fix parser", "active"), pending("[aiman] note A"))
    res, err := srv.writeHandover(ctx, domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: dir, TmuxSession: "wtb", AgentTitle: "Fixing parser"}, "restart")
    // res.Written, res.Path == filepath.Join(dir, domain.AimanHandoverFileName)
    // file contains "t2 ▸ Fix parser", "[aiman] note A", "FAIL parser", "Fixing parser"
    // ctxStore.Get(ctx, "handover-s1") exists; Title "Handover: WTB-12"; Abstract "Fix parser"; Namespace host when Group/Repo empty
}
func TestWriteHandoverUnchangedIsNotRewritten(t *testing.T) // second call: Written false, file mtime unchanged, note Put not called again (wrap store to count Puts)
func TestWriteHandoverNoteNamespace(t *testing.T)           // Group "WTB" → ns group key WTB; else RepoName → repo; else host
func TestWriteHandoverNoWorkingDirectory(t *testing.T)      // error "session has no working directory"
func TestDiffStatNonGitDir(t *testing.T)                    // local.NewExecutor on a temp dir → ""
func TestDiffStatAgainstBase(t *testing.T)                  // temp git repo: commit on main, branch, modify a file uncommitted → output contains the file name
func TestPaneTailKeepsLast40Lines(t *testing.T)             // 100 lines → 40, last one present
```

- [ ] **Step 2: Run** `go test ./internal/server/ -run 'Handover|DiffStat|PaneTail'`. Expected: FAIL.

- [ ] **Step 3: Implement.** Working dir = `WorkingDirectory`, else `WorktreePath`, else error. Pane: pty sessions with `s.pty != nil` use `CaptureScreen(sess.ID)`; others `s.remote.CaptureTmuxPane(ctx, sess.TmuxSession)`; errors give empty tail; then `pane.Clean`, trim trailing blank lines, keep last `handover.PaneLines`. Diff command (quote with `strconv.Quote` like `handlers_session.go` does elsewhere):

```sh
cd <dir> && git rev-parse --git-dir >/dev/null 2>&1 || exit 0
base=<BaseBranch or empty>; [ -n "$base" ] || base=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null) || exit 0
mb=$(git merge-base "$base" HEAD 2>/dev/null) || exit 0
git diff --stat "$mb"
```

The rendered `BaseBranch` label is `sess.BaseBranch`, or `origin/HEAD` when empty. Todos: `s.todos.Get(id)`; nl-1 returns an empty list (Rev 0, no items, nil error) for a session without one, which means `Todos: nil`. Undelivered: texts from `s.steer.Status(id).Pending`. Hook: `AgentTitle`, `HookStateMessage`. Put the note only when `Written` is true (or the note does not exist yet): `ID handoverNoteID`, namespace group/repo/host as in `contextstore.EntryFromSnapshot`, `Title "Handover: "+Label`, `Abstract handover.Abstract(in)`, `Body handover.NoteBody(in)`, `SessionID sess.ID`. Note-put errors are logged, not returned. Nil `todos`/`steer`/`ctxStore`/`pty` are tolerated so existing `New(...)` call sites keep working.

- [ ] **Step 4: Run** the tests. Expected: PASS. `make ci`.

- [ ] **Step 5: Commit** `feat(serve): write .aiman_handover.md and a handover note`.

---

### Task 4: Serve triggers and socket methods

**Files:**
- Modify: `internal/server/handover.go` (handlers), `internal/server/server.go:153-164` (dispatch), `internal/server/handlers_session.go:708-727` (`handleForget`), `:753-799` (`handleReportAgentSession`)
- Modify: `cmd/aiman/rpc_cmd.go`, `cmd/aiman/rpc_cmd_test.go` (allowlist both methods)
- Test: `internal/server/handover_test.go`, `internal/server/handlers_forget_test.go`

**Interfaces:**
- Consumes: `writeHandover` (Task 3); `Steerer.Enqueue(SteerNote)` (nl-1); `domain.TodoAuthor("aiman")` (use nl-1's constant).
- Produces:
  - `handover.write {session, reason}` (`session` resolved like nl-1's `todo.*`: name, group/name or id) → `{"type":"handover","path","written","note_id","exists":true}`; reason must be one of `archive, forget, restart, agent_switch, pre_compact`, else `CodeInvalidParams`.
  - `handover.point {session}` → `{"type":"handover_point","queued":bool}`; queues `HandoverPointerNote` only when the file exists.
  - `const HandoverPointerNote = "[aiman] This session's terminal was restarted. Your last handover is in .aiman_handover.md; read it if you lost track."`
  - `rpcMethods` (nl-1 Task 3a) gains `handover.write` and `handover.point`, with `TestRPCParams` rows. No new CLI subcommands: the laptop is the only caller.

- [ ] **Step 1: Write failing tests.**

```go
func TestHandoverWriteMethod(t *testing.T)            // Call(sock,"handover.write",{"session":"s1","reason":"archive"}) → written true, file exists
func TestHandoverWriteRejectsUnknownReason(t *testing.T) // reason "nap" → CodeInvalidParams
func TestHandoverWriteUnknownSession(t *testing.T)    // → CodeNotFound, returns in < 2 s
func TestPreCompactReportWritesHandover(t *testing.T) // session.report_agent_session {"id":"s1","agent_session_id":"c1","event":"PreCompact"} → file exists
func TestSessionEndReportDoesNotWriteHandover(t *testing.T) // {"id":"s1","ended":true,"event":"SessionEnd"} → no file
func TestForgetWritesHandoverBeforeKill(t *testing.T) // in handlers_forget_test.go: session with WorkingDirectory → file exists after session.forget, row gone
func TestHandoverPointQueuesOnlyWhenFileExists(t *testing.T) // stub steerer records Enqueue; no file → queued false, 0 notes; file → queued true, Text == HandoverPointerNote, Author aiman
```

- [ ] **Step 2: Run** `go test ./internal/server/ -run 'Handover|PreCompact|SessionEndReport|Forget'`. Expected: FAIL.

- [ ] **Step 3: Implement.** Handlers resolve the `session` param with `resolveSession(loadSessions)` (same as nl-1's `todo.*`) and wrap work in `context.WithTimeout(ctx, 20*time.Second)`. In `handleReportAgentSession`, after the session is saved, `if strings.EqualFold(rep.Event, agenthook.EventPreCompact) { _, err := s.writeHandover(ctx, *sess, "pre_compact"); log on err }` — synchronous: capture is local and the agent's compaction waits for the hook, so the handover reflects the pre-compact state. In `handleForget`, call `writeHandover(…, "forget")` best-effort (10 s budget, log errors) before `s.pty.Kill`. Add dispatch cases next to the `context.*` ones.

- [ ] **Step 4: Run** the tests plus `go test ./cmd/aiman/ -run RPCParams`. Expected: PASS. `make ci`.

- [ ] **Step 5: Commit** `feat(serve): handover.write, handover.point, PreCompact and forget triggers`.

---

### Task 5: Laptop usecases: restart handoff and revive pointer

**Files:**
- Create: `internal/usecase/handover_remote.go`
- Modify: `internal/usecase/session_terminal.go:632-645` (`ReviveIfNeeded`)
- Test: `internal/usecase/handover_remote_test.go` (reuse `restartRemote` from `restart_handoff_test.go`, extending its `Execute` switch for `aiman rpc handover.write` / `handover.point`)

**Interfaces:**
- Consumes: `usecase.ServeRPC` (nl-1 Task 3a) with methods from Task 4; `CaptureSessionHandoffBestEffort` (`restart_handoff.go:84`) as the fallback.
- Produces:

```go
const HandoverPrompt = `Read ` + domain.AimanHandoverFileName + ` — it is the handover from the previous agent: the to-do list, notes you have not seen, changed files and the last screen. Continue from it.`

type HandoverResult struct { Path string `json:"path"`; Written bool `json:"written"`; Exists bool `json:"exists"` }
func WriteHandoverOnRemote(ctx context.Context, remote TerminalExecutor, sessionID, reason string) (HandoverResult, error)

type HandoffMode int
const ( HandoffNone HandoffMode = iota; HandoffHandover; HandoffSummary )
// PrepareRestartHandoff tries serve's handover first; on any error it falls back
// to asking the agent for .aiman_session_summary.md (today's flow).
func PrepareRestartHandoff(ctx context.Context, remote domain.RemoteExecutor, s domain.Session, workingDir, reason string, summaryTimeout time.Duration) (HandoffMode, string /*note*/)
func pointAtHandover(ctx context.Context, remote TerminalExecutor, sessionID string)
```

- [ ] **Step 1: Write failing tests.**

```go
func TestPrepareRestartHandoffUsesServeHandover(t *testing.T) {
    // remote answers `aiman rpc handover.write` with {"result":{"type":"handover","path":"/w/.aiman_handover.md","written":true,"exists":true}}
    // → HandoffHandover; commands include `rm -f` of /w/.aiman_session_summary.md; no SESSION_SUMMARY_SAVED prompt sent
}
func TestPrepareRestartHandoffFallsBackOnUnknownMethod(t *testing.T) // ServeRPC returns ErrRPCUnsupported → summary flow runs (prompt contains SESSION_SUMMARY_SAVED); mode HandoffSummary when the file appears, HandoffNone otherwise
func TestPrepareRestartHandoffPassesReason(t *testing.T)            // decoded rpc params have reason "agent_switch"
func TestReviveIfNeededPointsAtHandover(t *testing.T)              // pty session revived → an `aiman rpc handover.point` command ran after `aiman pty create`
func TestReviveIfNeededNoRevivalNoPointer(t *testing.T)            // PTY exists → no handover.point command
```

- [ ] **Step 2: Run** `go test ./internal/usecase/ -run 'RestartHandoff|ReviveIfNeeded'`. Expected: FAIL.

- [ ] **Step 3: Implement.** `WriteHandoverOnRemote` calls `ServeRPC(ctx, remote, "handover.write", {"session": id, "reason": r})` and decodes the result. On success remove a stale `.aiman_session_summary.md` (and `.tmp`) in `workingDir` so `PrepareSession` (`internal/infra/skills/engine.go:207`) does not point the new agent at an old summary. `pointAtHandover` is best-effort (errors ignored) and runs at the end of a successful `ReviveIfNeeded`.

- [ ] **Step 4: Run** the tests. Expected: PASS. `make ci`.

- [ ] **Step 5: Commit** `feat(usecase): restart from serve handover; revive points at it`.

---

### Task 6: Dashboard wiring, skill and docs

**Files:**
- Modify: `internal/ui/dashboard.go:7543-7577` (restart step 1 and prompt), `:1290-1305` (`persistSnapshotCmd`)
- Modify: `internal/aimanskill/SKILL.md`, `AGENTS.md`
- Test: `internal/ui/restart_agent_switch_test.go` or a new `internal/ui/restart_handover_test.go`

**Interfaces:**
- Consumes: `usecase.PrepareRestartHandoff`, `usecase.HandoverPrompt`, `usecase.WriteHandoverOnRemote` (Task 5).
- Produces: `func restartHandoverReason(cfg domain.SessionConfig) string` (`"agent_switch"` when `cfg.FreshAgent`, else `"restart"`); `func restartPrompt(prepared string, mode usecase.HandoffMode) string`.

- [ ] **Step 1: Write failing tests** for the two pure helpers:

```go
func TestRestartHandoverReason(t *testing.T) // FreshAgent true → "agent_switch"; false → "restart"
func TestRestartPromptAddsHandover(t *testing.T) {
    // restartPrompt("", usecase.HandoffHandover) == usecase.HandoverPrompt
    // restartPrompt("Read .aiman_task.md …", usecase.HandoffHandover) has the task prompt first, then HandoverPrompt
    // restartPrompt("x", usecase.HandoffSummary) == "x"
}
```

- [ ] **Step 2: Run** `go test ./internal/ui/ -run 'RestartHandoverReason|RestartPrompt'`. Expected: FAIL.

- [ ] **Step 3: Implement.** Replace the `CaptureSessionHandoffBestEffort` call with `mode, note := usecase.PrepareRestartHandoff(summaryCtx, mgr, *s, workingDir, restartHandoverReason(sessionCfg), 90*time.Second)`; status text "Writing handover..."; `resumeSnapshot = nil` when `mode != usecase.HandoffNone`; `sendKeysPrompt = restartPrompt(prepared.InitialPrompt, mode)` before `InjectSharedContext`. In `persistSnapshotCmd`, before `putRemoteSnapshotContext`, call `usecase.WriteHandoverOnRemote(putCtx, mgr, sess.ID, "archive")` best-effort (resolve the remote once and share `mgr`). Skill: one paragraph under "Shared context" saying `.aiman_handover.md` is written on restart/compaction, read it when resuming, never commit it. AGENTS.md: update the "Session Restart" steps (step 2 becomes "serve writes `.aiman_handover.md`; agent-written summary only as fallback") and add `handover.write`/`handover.point` to the serve section.

- [ ] **Step 4: Run** `make ci`. Expected: PASS (only the known Ollama failure).

- [ ] **Step 5: Manual check.** On a dev remote with updated serve: restart a session with a to-do list; the new agent's first prompt names `.aiman_handover.md`, the file holds the to-dos and screen, and `aiman context get handover-<id>` returns the note. Run `/compact` in a Claude session and confirm the file updates.

- [ ] **Step 6: Commit** `feat(ui): restart, agent switch and archive use serve's handover`.

---

## Spec deviations

1. **Laptop triggers need a socket method.** Archive, restart and agent switch run on the laptop, so serve learns of them through new `handover.write`, called through nl-1's `aiman rpc`. The spec names no method.
2. **Steering notes:** the spec's `SteerStatus` is "queued count, last delivered, last error". The handover needs the texts; nl-1 owns `SteerStatus.Pending []SteerNote`.
3. **Forget-time handover usually has no screen.** The dashboard tears the terminal down (`terminateKillTmux`) before `session.forget` (`terminateDeleteRecord`, `dashboard.go:2544-2563`), so the forget handover often renders an empty last-screen section. Archive writes its own handover first, so archived sessions keep the screen.
4. **The handover note has no `Type` yet.** W6a ships before W6b, so the note uses the stable id `handover-<session-id>` (overwritten, never piling up). Plan 3b sets `Type: handover` and adds the memory-candidate enqueue.
5. **The restart prompt points at the file** instead of inlining 40 screen lines plus a diff stat into a prompt that is typed with send-keys.
6. **Revive pointer goes through the Steerer** (`handover.point`), so it obeys the delivery gate instead of typing into an agent that is booting.
7. `restart_handoff.go:26` is verified (`CaptureRestartSessionSummary`). The flow is kept as the fallback, not deleted.

---

## Research amendments — 2026-10-26

Source: `docs/superpowers/research/2026-10-26-harness-research.md` §What the
research adds (4) and the course's ADR-0006. These tasks are additive follow-ups
to the shipped handover. They make the file the compaction protocol, not only a
restart artifact.

### Task 7: Fixed handover skeleton and verbatim evidence

**Files:**
- Modify: `internal/handover/render.go`, `internal/handover/render_test.go`
- Modify: `internal/server/handover.go` (Input construction), `internal/server/handover_test.go`

**Interfaces:**
```go
type Evidence struct {
    TestCommand string
    TestOutput  string
    ErrorTrace  string
    Commands    []string
    Files       []string
}
type Input struct {
    // existing fields …
    Evidence     Evidence
    Verification string // "passed", "incomplete", "not recorded"
}
func MissingSections(md string) []string // required headings absent, in order
```
Required headings, in this order:
`## Goal`, `## Constraints & Preferences`, `## Progress`, `## Key Decisions`,
`## Next Steps`, `## Critical Context`, `## Evidence`, `## Verification`.

The content is derived, not summarised — serve has no model:
- **Goal**: the active to-do text, else `HookTitle`, else `"Continue the session."`
- **Constraints & Preferences**: `"See .aiman_task.md and the to-do list."` plus the hook message when present.
- **Progress**: `Done: n · In progress: n · Blocked: n`, then the to-do lines (the existing `formatTodos` output).
- **Key Decisions**: `"None recorded in this handover."` — do not invent decisions.
- **Next Steps**: pending and active items, in list order.
- **Critical Context**: undelivered steering notes, oldest first.
- **Evidence**: `git diff --stat` in a fence, then the last `PaneLines` cleaned lines. Keep error, test-failure and stack-trace lines verbatim; do not strip or summarise them. A line is "evidence" when it matches `(?i)(error|fail|panic|traceback|assert|expected|actual|exit status|FAIL)` or it is one of the last 10 lines.
- **Verification**: the latest review status for the worker, else `"not recorded"`. Read it lazily; if nl-7 is not in the tree, the string is `"not recorded"`.

Remove the old `## To-do list`, `## Changed files`, `## Last agent status` and
`## Last screen` headings; their content moves under Progress/Critical
Context/Evidence. Update `TestNoteBody` and the server tests that assert the old
headings. `Abstract` keeps its current fallback chain.

- [ ] **Step 1: Write the failing tests**
  - `TestHandoverHasRequiredSections`: every heading appears exactly once, in order.
  - `TestHandoverEvidenceKeepsErrorLines`: a pane tail with `go test` failure lines keeps them verbatim; a progress-bar line is dropped by `pane.Clean` before it reaches the renderer.
  - `TestHandoverGoalFallsBack`: no active item and no hook title → `Continue the session.`
  - `TestMissingSections`: a body missing `## Evidence` and `## Verification` returns exactly those two.
  - `TestHandoverVerificationNotRecorded`: no review → `not recorded`.
- [ ] **Step 2: Run** `go test ./internal/handover/ ./internal/server/ -run 'Handover|MissingSections'` → FAIL.
- [ ] **Step 3: Implement.** Keep `Render` a pure function of `Input` (no git, clock or network). `writeHandover` fills `Evidence.TestCommand` from the repo's `verify_command` when configured (nl-7 T13), `Files` from `git diff --name-only`, and `Commands` from the to-do `Ref`s when they name checks. Everything else is derived.
- [ ] **Step 4: Run** the tests and `make ci` → PASS.
- [ ] **Step 5: Commit** `feat(handover): fixed skeleton and verbatim evidence`

### Task 8: Reason-aware pointer after compaction

**Files:**
- Modify: `internal/server/handover.go`, `internal/server/handover_test.go`
- Modify: `cmd/aiman/rpc_cmd.go` (allowlist unchanged; `handover.point` params gain `reason`)

**Interfaces:**
```go
const (
    HandoverRestartNote    = "[aiman] This session's terminal was restarted. Your last handover is in .aiman_handover.md; read it if you lost track."
    HandoverCompactionNote = "[aiman] Your context was compacted. Your handover is in .aiman_handover.md; read it if you lost track."
)
func (s *Server) queueHandoverPointer(ctx context.Context, sess domain.Session, reason string) bool
```
`handover.point` accepts `{session, reason?}`; `reason` defaults to `restart` and
accepts the same five values as `handover.write`. `notePreCompactHandover`
writes the file and then queues `HandoverCompactionNote` best-effort inside the
same 20 s budget. Do not queue on `archive` or `forget` (the session is ending).

- [ ] **Step 1: Write the failing tests**
  - `TestPreCompactQueuesCompactionPointer`: a `PreCompact` report writes the file and enqueues exactly one note whose text is `HandoverCompactionNote`.
  - `TestPointUsesReasonText`: `handover.point` with `reason=pre_compact` uses the compaction text; `restart` (or empty) uses the restart text.
  - `TestPointDoesNotQueueForArchive`: reason `archive` queues nothing.
- [ ] **Step 2: Run** `go test ./internal/server/ -run 'PreCompact|PointUsesReason'` → FAIL.
- [ ] **Step 3: Implement.** `queueHandoverPointer` checks `ctx.Err`, `s.steer` and `handoverFileExists` before enqueuing, as today. The pointer is a normal steer note, so the delivery gate holds it until the agent is idle after compaction.
- [ ] **Step 4: Run** the tests and `make ci` → PASS.
- [ ] **Step 5: Commit** `fix(handover): point at the file after compaction`
