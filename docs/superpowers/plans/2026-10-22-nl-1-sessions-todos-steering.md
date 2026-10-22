# NL-1: Serve Registry, To-dos and Steering (backend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `aiman serve` the registry of every session on its host and the single writer of a per-session to-do list that the user and the agent share, with user changes delivered to the agent as steering notes when it is safe to type.

**Architecture:** W0 adds `session.register`, persists hook reports for every session, fixes quick-session collisions and lets `session.events` run without a PTY runtime. It also adds `aiman rpc`, the single laptop→serve entry point every later plan registers its methods with. W1 adds a pure `internal/todo` package (file store, diff, mirror merge, rendering, note formatting), a `Steerer` in `internal/server` that coalesces, persists and gates delivery, an event `Hub`, `todo.*`/`steer.send` socket methods, the `aiman todo` CLI and agent hooks that mirror vendor to-do tools.

**Tech Stack:** Go 1.26, `modernc.org/sqlite`, newline JSON over `~/.aiman/aiman.sock`, Claude/Codex hook JSON.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (W0, W1 minus "Dashboard: layout A1", Shared interfaces, Constraints).

## Spec deviations (code reality vs spec)

1. **Line refs.** `handlers_session.go:262-264` is right (in-memory labelling, now `:262-269`). The hook-report drop is `:789-797`, not `:787-793`. The laptop create paths are `dashboard.go:2003-2028` (remote hand-off) and `:2033-2064` (local flow + save), not `:2014-2033`. `handlers_events.go:71-73` is exact.
2. **`BaseBranch` exists only on `SessionConfig`** (`domain/session.go:228`). `domain.Session` has no such field and the auto-detected base (`infra/git/manager.go:368-370`) is never returned. Task 3 adds `Session.BaseBranch`, a `base_branch` column and `Worktree.Base`.
3. **Claude's `idle_prompt` maps to `idle`, not `waiting_input`** (`agenthook/report.go:104-109`, `:125-131`). The gate treats both "idle" and "waiting_input with source idle_prompt" as ready.
4. **Claude and Codex never report a turn ending.** `upsertIdentityHooks` (`agenthook/merge.go:188-193`) registers only `SessionStart`, `SessionEnd`, `Notification(idle_prompt)`. Claude goes "idle" only after its ~60 s idle notification, and hook state expires after `hookTTL` = 2 min (`report.go:17`). Task 15 registers `Stop` (idle) and `UserPromptSubmit` (working) for Claude and Codex, which `inferFromEvent` already maps (`report.go:111-124`).
5. **`AgentEnded` cannot be cleared by `Save`.** `agent_ended = COALESCE(NULLIF(excluded.agent_ended, ''), …)` (`sqlite/repository.go:246`) keeps `"1"` forever, although `ApplyReport` retracts it (`report.go:213-221`). Claude fires `SessionEnd` on `/clear` and `/compact`, so the gate would hold notes forever. Task 4 adds `ClearAgentEnded`.
6. **Claude Code 2.1.x defaults to the Tasks tools (`TaskCreate`/`TaskUpdate`), not `TodoWrite`.** The installed 2.1.308 binary returns Tasks enabled unless `CLAUDE_CODE_ENABLE_TASKS` is false. aiman does not disable them. This host's `~/.claude/projects` transcripts contain zero `TodoWrite` and zero `TaskCreate` calls, so vendor mirroring cannot be the primary path: the `aiman todo` CLI taught by the skill is the primary path for every agent. Mirroring is a bonus: `TodoWrite` (full snapshot) and the Tasks tools (when present) are both mirrored. Task 1 discovers the Tasks payloads and where the full task list can be read.
7. **Hook dedupe is keyed on one marker** (`Marker = "report-agent-session.sh"`, `agenthook/native.go:15`). A second script would be appended on every serve start. Task 15 adds a marker parameter.
8. **Merge rule needs a delivery guard** (spec rules 3–5 and the "survive the agent's next write" anti-goal conflict). Notes are held while the agent works, but `TodoWrite` fires mid-turn with the agent's stale view. The merge therefore ignores snapshot status for non-agent items changed after the last delivery, skips re-adding items the user removed after the last delivery (tombstones), and keeps the current order while such changes are undelivered.
9. **Shared-interface extensions** (names every later plan uses; see `2026-10-22-nl-0-roadmap.md`): JSON tags on `TodoList`/`TodoItem`; `TodoList.NotInAgent []string` (`not_in_agent`) carries the ⚠ marker; free-text steering is `steer.send {session, text, as, mode}` with `mode` `note` (default, gated) or `answer` (answers a dialog at once, never queued); `todo.summary {sessions?}` feeds list progress bars; `SteerStatus.Pending []SteerNote` carries undelivered note texts (W6a); `SessionEvent` gains `state`, `rev`, `queued`, `delivered_at`, `kind`; `todo.OpRef` (Ref-only change, never steers, W8); `Server.MutateTodos`, `Server.TodoAccess()`, `Server.Steerer()` are the entry points for W4/W5/W8 wiring; `fsutil.WriteIfChanged` is the one local compare-before-write helper.
10. **`internal/server` cannot be imported by `usecase`/`sqlite`**, so the persisted note type is `domain.SteerRecord`, and the register wire format is `domain.Session` marshalled with default Go field names.
11. **One laptop→serve mechanism.** Every laptop call to a new socket method goes through `aiman rpc METHOD --params-stdin` (Task 3a), invoked by `usecase.ServeRPC`. Later plans add their methods to the `rpcMethods` allowlist instead of adding per-feature CLI subcommands for the laptop. Human/agent CLIs named in the spec (`aiman todo`, `aiman usage`, `aiman verify`) stay.
12. **`Ref` prefixes** (one namespace across plans): `check:<name>` (W4 failed check), `review:<comment id>` (W4 PR review comment), `finding:<review id>/<finding id>` (W5 verifier finding), `child:<session id>` (W8 fan-out child). Plans own their helper functions; the prefixes must not overlap.
13. **`resolveWorktree` extraction.** `flow_manager_test.go` has no fake git manager (only `TestCreateSessionRequiresAgent` touches `CreateSession`). Task 3 extracts the worktree switch into `resolveWorktree` and tests the base-branch rule as a pure function; nl-7 (`PreparedWorktree`) and nl-8 (`shouldBootstrap`) build on the same extraction.

## Global Constraints

- Go 1.26; Bubble Tea v2 only (no v1 imports). `make ci` green before every commit; `TestOllamaIntelligenceSummariseSession` is a known failure.
- No socket method may block longer than 30 s; long waits are client-side polling.
- Both terminal backends (tmux, pty) must work for every feature here.
- Serve is the only writer of to-dos and the steering queue; other processes go through the socket.
- `.aiman_todo.md` uses the `.aiman_context.md` "DO NOT COMMIT" header (already ignored by the `.aiman_*` glob, `domain/session_files.go:12`) and is written only when its content changes.
- `internal/usecase` must not import `internal/gateway` or `internal/server`.
- Coalescing window: 800 ms per session. Similarity threshold: 0.8 token Jaccard.
- Steering note text is exact (spec W1 "Note text"); free text from `steer.send` is sent without the wrapper.
- An agent's own write never produces a steering note (author `agent` never enqueues).
- Source comments describe current code, not history.
- Before every code-task commit run `make ci` (`TestOllamaIntelligenceSummariseSession` is a known failure) and `graphify update .`.

## Review Focus

1. **Agent writes its list mid-turn, before the user's note is delivered.** The user's edit, state change, removal and order must survive. Tests: Task 8 `TestMergeKeepsUndeliveredUserState`, `TestMergeSkipsUndeliveredTombstone`, `TestMergeKeepsOrderWhileUndelivered`.
2. **Stale "idle" right after a delivery.** Hook state still reads idle until `UserPromptSubmit` lands; a second queued note must not be typed into a now-working agent. Test: Task 12 `TestSteerReadyRequiresFreshIdleAfterDelivery`.
3. **Agent ended, then resumed** (`/clear`, revive). Notes must hold while ended and flow once a live report arrives. Tests: Task 4 `TestReportAfterEndClearsLatch`, Task 12 `TestSteerHoldsWhileEnded`.
4. **Serve restart with undelivered notes.** Reloaded once, delivered once, not duplicated. Test: Task 11 `TestSteererReloadsUndeliveredOnce`.
5. **Garbage or unrelated hook payloads.** Non-JSON, another tool, or a parse failure must never call `todo.mirror` (an empty snapshot would drop every agent item); the hook always exits 0. Tests: Task 14 `TestExtractTodoSnapshotRejectsUnrelated`, `TestTodoReportNoopsOnGarbage`.

---

### Task 1: Spike — capture vendor to-do hook payloads

**Files:**
- Create: `internal/agenthook/testdata/claude_posttooluse_todowrite.json`
- Create: `internal/agenthook/testdata/claude_posttooluse_taskcreate.json`, `claude_posttooluse_taskupdate.json` (only if Tasks fire)
- Create: `internal/agenthook/testdata/codex_posttooluse_update_plan.json` (only if Codex emits it)
- Create: `internal/agenthook/testdata/codex_stop.json`

**Interfaces:**
- Produces: fixtures consumed by Task 14 and Task 15; a decision recorded in the commit message: `codex-update-plan: yes|no`, `claude-tasks-default: yes|no`, `claude-tasks-full-list: <how the full list is read: tool_response field, a file path pattern such as ~/.claude/tasks/<list-id>/, or none>`.

- [ ] **Step 1: Install a capture hook in a scratch HOME for Claude**

In a temp dir, write `.claude/settings.json` with `PostToolUse` matcher `TodoWrite|TaskCreate|TaskUpdate|TaskList` and `Stop`, each running `sh -c 'cat >> "$CAPTURE"'`. Run `HOME=$tmp CAPTURE=$tmp/claude.ndjson claude -p "Make a 3-item todo list for adding a README, mark the first in progress"` once with default env (Tasks) and once with `CLAUDE_CODE_ENABLE_TASKS=false` (to capture a `TodoWrite` fixture only; aiman never sets this variable). For the Tasks run, record whether `tool_response` carries the whole list and list any files Claude wrote under `$tmp/.claude/tasks/`. Copy credentials into the scratch HOME only by symlink to `~/.claude/.credentials.json` (Codex: `~/.codex/auth.json` into `$tmp/.codex/`); never print them.

- [ ] **Step 2: Same for Codex**

`$tmp/.codex/hooks.json` with `PostToolUse` (matcher `update_plan`, then `*` if nothing arrives) and `Stop`. Run `codex exec` with a planning prompt. Expected decision rule: if a payload with `tool_name == "update_plan"` and `tool_input.plan[]` arrives, `codex-update-plan: yes`.

- [ ] **Step 3: Save trimmed fixtures**

Keep only `hook_event_name`, `session_id`, `tool_name`, `tool_input`. Replace ids with `"sess-1"`.

- [ ] **Step 4: Commit**

```bash
git add internal/agenthook/testdata
git commit -m "test: capture Claude and Codex to-do hook payloads

codex-update-plan: <yes|no>
claude-tasks-default: <yes|no>
claude-tasks-full-list: <response|file:<pattern>|none>"
```

If `claude-tasks-full-list: none`, Task 14 mirrors only `TodoWrite`; Tasks-tool sessions rely on the CLI (the primary path anyway). Note it in Open questions; do not stop.

---

### Task 2: W0 — collision-free quick-session labels

**Files:**
- Modify: `internal/domain/branch_name.go` (add function)
- Modify: `internal/usecase/flow_manager.go:222`, `:283`, `:314`
- Test: `internal/domain/branch_name_test.go`

**Interfaces:**
- Produces: `func AdHocLabel(prefix string, now time.Time, rnd io.Reader) string` → `prefix + now.Format("20060102-150405") + "-" + 4 chars from [a-z0-9]`. On a reader error it falls back to the nanosecond field mod 36⁴ in base 36, so it never returns without a suffix.

- [ ] **Step 1: Write the failing test**

```go
func TestAdHocLabelHasSecondsAndSuffix(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 4, 5, 0, time.UTC)
	got := AdHocLabel("adhoc-", now, strings.NewReader("\x00\x01\x02\x03"))
	if got != "adhoc-20261022-150405-0123" { t.Fatalf("got %q", got) }
	if SanitizeTmuxSessionName(got) != got { t.Fatalf("not tmux-safe: %q", got) }
}
func TestAdHocLabelDiffersWithinOneSecond(t *testing.T) {
	now := time.Now()
	a := AdHocLabel("adhoc-", now, rand.Reader)
	b := AdHocLabel("adhoc-", now, rand.Reader)
	if a == b { t.Fatalf("collision %q", a) }
}
```
Byte → char mapping: `alphabet[b % 36]` with `alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"`.

- [ ] **Step 2: Run** `go test ./internal/domain -run TestAdHocLabel` → FAIL (undefined).
- [ ] **Step 3: Implement**, then replace the three `time.Now().Format("20060102-1504")` labels in `CreateSession` with `domain.AdHocLabel("adhoc-"|"session-", time.Now(), rand.Reader)` (`crypto/rand`).
- [ ] **Step 4: Run** `go test ./internal/domain ./internal/usecase` → PASS.
- [ ] **Step 5: Commit** `fix: give quick sessions second-resolution labels with a random suffix` (run `make ci` and `graphify update .` first)

---

### Task 3: W0 — record `BaseBranch` on the session

**Files:**
- Modify: `internal/domain/session.go:54-92` (field), `internal/domain/interfaces.go:152-155` (`Worktree.Base`)
- Modify: `internal/infra/git/manager.go:392-395` (return `Base: baseBranch`)
- Modify: `internal/usecase/flow_manager.go:257-271`, `:274-320` (extract the worktree switch into `resolveWorktree`)
- Modify: `internal/infra/sqlite/repository.go:103` (ALTER), `:218-252` (insert + `base_branch = COALESCE(NULLIF(excluded.base_branch, ''), sessions.base_branch)`), `internal/infra/sqlite/repository_scan.go:11` and `scanSession`
- Modify: `internal/server/handlers_session.go:18-69` (`SessionInfo.BaseBranch` `json:"base_branch,omitempty"`), `internal/usecase/session_create_remote.go` (`remoteCreateResult` carries `base_branch`)
- Test: `internal/infra/sqlite/session_native_test.go`, `internal/usecase/flow_manager_test.go`

**Interfaces:**
- Produces: `domain.Session.BaseBranch string`; `domain.Worktree.Base string`.
- Produces: `func (m *FlowManager) resolveWorktree(ctx context.Context, sshMgr domain.RemoteExecutor, config domain.SessionConfig, branch string) (domain.Worktree, error)`: the switch at `flow_manager.go:274-320` moved unchanged (ad-hoc dir, attach/reuse/existing-branch/new worktree, main-repo safety check, no-repo dir); the ad-hoc and no-repo cases return `Worktree{Path: dir}`. `CreateSession` sets `session.WorktreePath = wt.Path`. nl-7 adds a `PreparedWorktree` case and nl-8 calls `shouldBootstrap` after it.
- Produces: `func sessionBaseBranch(configured, detected string) string` (configured wins, else detected).

- [ ] **Step 1: Write failing tests**

`TestSaveKeepsBaseBranchWhenEmpty` (save with `BaseBranch: "main"`, re-save with `""`, `Get` returns `"main"`). In `flow_manager_test.go` (it has no fake git manager, so test the extracted pieces; add a minimal `fakeRoot(dir string) domain.RemoteExecutor` helper in this file that answers the root-dir lookup with `dir`): `TestSessionBaseBranch` (table: `("", "origin/main")` → `"origin/main"`; `("dev", "origin/main")` → `"dev"`; `("", "")` → `""`) and `TestResolveWorktreeAdHoc` (`(&FlowManager{}).resolveWorktree(ctx, fakeRoot("/r"), SessionConfig{AdHoc: true}, "adhoc-x")` → `Path == "/r/adhoc-x"`, with a nil `gitManager` proving no git call).

- [ ] **Step 2: Run** `go test ./internal/infra/sqlite ./internal/usecase -run 'BaseBranch|ResolveWorktree'` → FAIL.
- [ ] **Step 3: Implement.** Extract `resolveWorktree`; then `session.BaseBranch = sessionBaseBranch(config.BaseBranch, wt.Base)`.
- [ ] **Step 4: Run** `go test ./internal/infra/sqlite ./internal/usecase ./internal/server` → PASS.
- [ ] **Step 5: Commit** `feat: record the base branch on each session` (run `make ci` and `graphify update .` first)

---

### Task 3a: `aiman rpc` and `usecase.ServeRPC` (the laptop→serve entry point)

**Files:**
- Create: `cmd/aiman/rpc_cmd.go`, `internal/usecase/serve_rpc.go`
- Modify: `cmd/aiman/main.go:73-80` (socket-only early switch: `case "rpc": return runRPC(parsed.Rest[1:], os.Stdin, os.Getenv)`)
- Test: `cmd/aiman/rpc_cmd_test.go`, `internal/usecase/serve_rpc_test.go`, `cmd/aiman/socket_only_dispatch_test.go` (add `{"rpc", "todo.get"}`)

**Interfaces:**
- Consumes: `callAndPrintRaw(sock, method string, rawParams []byte) error` (`cmd/aiman/pty_cmd.go:663`), `socketPath()` (`cmd/aiman/session_cmd.go:33`), `remoteAimanPreamble`.
- Produces (CLI): `var rpcMethods = map[string]bool{…}`, the one allowlist of methods the laptop may call. This plan registers `session.register`, `todo.get`, `todo.summary`, `todo.add`, `todo.update`, `todo.move`, `todo.remove`, `steer.send`. Later plans append theirs in their own task (nl-3a `handover.write`, `handover.point`; nl-3b `memory.jobs`, `memory.done`; nl-4 `usage.get`; nl-6 `ci.get`, `ci.watch`; nl-7 `verify.start`, `verify.status`; nl-9 `spec.fanout`) with one `TestRPCParams` row each.
- Produces: `func rpcParams(method string, raw []byte) ([]byte, error)`: unknown method → `unknown method`; params must be a JSON object. It does not inspect `as`: `deriveAuthor` (Task 13) is the single authority on authorship. `runRPC` prints the whole `server.Response` as one compact JSON line (`{"result":…}` or `{"error":{…}}`), not just the result, and exits non-zero on an error answer. `func runRPC(args []string, stdin io.Reader, getenv func(string) string) error`: usage `aiman rpc METHOD --params-stdin`.
- Produces (usecase): `func ServeRPC(ctx context.Context, remote TerminalExecutor, method string, params any) (json.RawMessage, error)`: one SSH round trip, `remoteAimanPreamble + "printf %s '<base64 JSON>' | base64 -d | aiman rpc <method> --params-stdin"` (std base64 is safe inside single quotes, so no shell parses user text); parses the last JSON line of the output as a `server.Response` even when `Execute` returned an error (a non-zero `aiman rpc` exit still carries the `{"error":…}` line), then returns its `result`. `var ErrRPCUnsupported` (output mentions `unknown method`, `unknown command`, `command not found`: serve or binary older than the method) and `var ErrServeNotRunning` (`server_not_running`); other `{"error":…}` answers become `*RPCError{Code, Message}`.

- [ ] **Step 1: Write the failing tests**
  - `TestRPCParams` (table): `todo.add` + `{"session":"s1","text":"x","as":"user"}` → ok, bytes unchanged; `session.forget` → `unknown method`; `todo.get` + `[1]` → error (not an object); `steer.send` + `{"session":"s1","text":"hi"}` → ok.
  - `TestServeRPCNeverShellParsesText`: recording executor; params text `"it's \"$(rm -rf ~)\" `+"`id`"+`\nnext ✓"` round-trips through the decoded base64 segment; the raw command contains no `$(`, backtick or newline.
  - `TestServeRPCErrors`: output `unknown method todo.summary` → `errors.Is(err, ErrRPCUnsupported)`; `{"error":{"code":"server_not_running"}}` → `ErrServeNotRunning`; `{"error":{"code":"not_found","message":"x"}}` → `*RPCError` with code `not_found`; `noise\n{"result":{"type":"todo_list"}}` → raw result.
- [ ] **Step 2: Run** `go test ./cmd/aiman ./internal/usecase -run 'RPCParams|ServeRPC|SocketOnly'` → FAIL.
- [ ] **Step 3: Implement.** Errors from `runRPC` go through `writeCLIError(server.CodeInvalidParams, …)` like `runSession`.
- [ ] **Step 4: Run** the same command → PASS.
- [ ] **Step 5: Commit** `feat: aiman rpc, the allowlisted laptop entry point to serve` (run `make ci` and `graphify update .` first)

---

### Task 4: W0 — `session.register`, hook reports for every session, retractable `AgentEnded`

**Files:**
- Modify: `internal/server/server.go:128-180` (dispatch `session.register`)
- Modify: `internal/server/handlers_session.go:753-807` (report handler), add `handleRegister`, `registrableSession`
- Modify: `internal/infra/sqlite/repository.go` (add `ClearAgentEnded`)
- Modify: `internal/usecase/session_create_remote.go` (add `RegisterSessionsOnRemote`)
- Test: `internal/server/handlers_session_test.go`, `internal/infra/sqlite/session_native_test.go`, `internal/usecase/session_create_remote_test.go`

**Interfaces:**
- Produces (socket): `session.register` params `{"sessions": [<domain.Session JSON>]}` → `{"type":"sessions_registered","ids":[...]}`. Empty or `pending-` ids are skipped.
- Produces: `func registrableSession(in domain.Session, existing *domain.Session) domain.Session`: identity fields come from `in`; `HookState*`, `AgentEnded`, `AgentTitle`, `Status` come from `existing` when present (else `Status` from `in`); `LocalPath`, `MutagenSyncID`, `Tunnels`, `AWSConfig` are cleared (laptop-only).
- Produces: `func (r *Repository) ClearAgentEnded(ctx context.Context, id string) error` (sets `agent_ended = ''`). Serve calls it through a consumer interface `agentEndedClearer` when `ApplyReport` flipped `AgentEnded` from true to false.
- Produces: `func RegisterSessionsOnRemote(ctx context.Context, remote TerminalExecutor, sessions []domain.Session) error`: `ServeRPC(ctx, remote, "session.register", {"sessions": …})`; `ErrRPCUnsupported` and `ErrServeNotRunning` are nil (old or stopped serve).
- Behaviour: when `repo.Get` fails in `handleReportAgentSession`, resolve the id in `loadSessions`; if live, save that session first, then apply the report.

- [ ] **Step 1: Write failing tests**

```go
func TestSessionRegisterPersistsLaptopSession(t *testing.T)      // register → session.get by name returns Branch, WorktreePath, BaseBranch; repo.Get succeeds
func TestSessionRegisterKeepsServeRuntimeState(t *testing.T)    // existing row HookState=working; register with HookState=idle → stays working; LocalPath not stored
func TestReportAgentSessionPersistsLiveUnregistered(t *testing.T) // fakeRemote tmux session with AIMAN_ID=live-1, no row; report state=idle → repo.Get("live-1").HookState == idle
func TestReportAfterEndClearsLatch(t *testing.T)                // report ended=true, then state=working (source lifecycle) → repo.Get().AgentEnded == false
func TestClearAgentEnded(t *testing.T)                          // sqlite: Save AgentEnded:true; ClearAgentEnded; Get → false
func TestRegisterSessionsOnRemoteToleratesOldServe(t *testing.T) // fake executor returns "unknown command \"rpc\"" + error → nil
```
`TestReportAgentSessionUnknownIDStillSucceeds` keeps passing (unknown and not live: success, nothing written).

- [ ] **Step 2: Run** `go test ./internal/server ./internal/infra/sqlite ./internal/usecase -run 'Register|LiveUnregistered|ClearsLatch|ClearAgentEnded'` → FAIL.
- [ ] **Step 3: Implement.** Replace the comment at `handlers_session.go:262-264` so it states the current rule: discovered rows stay in memory until registered or reported.
- [ ] **Step 4: Run** `go test ./internal/server ./internal/infra/sqlite ./internal/usecase ./cmd/aiman` → PASS.
- [ ] **Step 5: Commit** `feat: let serve register laptop sessions and keep hook reports for all` (run `make ci` and `graphify update .` first)

---

### Task 5: W0 — TUI registers sessions with serve

**Files:**
- Modify: `internal/ui/session_events.go:62-85` (`startSessionStream` batches a register command)
- Modify: `internal/ui/dashboard.go:2057-2064` (after the local-flow save, register the new session)
- Create: `internal/ui/session_register.go`
- Test: `internal/ui/session_register_test.go`

**Interfaces:**
- Consumes: `usecase.RegisterSessionsOnRemote` (Task 4).
- Produces: `func sessionsForHost(all []domain.Session, host string) []domain.Session` (copies; skips `domain.IsEphemeralSessionID` and empty ids); `func registerSessionsCmd(remote config.Remote, sessions []domain.Session) tea.Cmd` returning `sessionsRegisteredMsg{host string; n int; err error}`; the handler only logs.

- [ ] **Step 1: Write the failing test** `TestSessionsForHostFiltersHostAndPlaceholders`: three sessions (host a, host b, `pending-x` on a) → returns only the real host-a session, and mutating the result does not change the input.
- [ ] **Step 2: Run** `go test ./internal/ui -run TestSessionsForHost` → FAIL.
- [ ] **Step 3: Implement.** Snapshot `m.allSessions` before building the command (no model access inside the goroutine). Every stream (re)start registers, which covers serve restarts. In the create goroutine, call `usecase.RegisterSessionsOnRemote` with the `*ssh.Manager` from `sessionCfg.SSHManager` and log failures with `m.logPersistent`.
- [ ] **Step 4: Run** `go test ./internal/ui` → PASS.
- [ ] **Step 5: Commit** `feat: register laptop-created sessions with the remote serve` (run `make ci` and `graphify update .` first)

---

### Task 6: Event Hub; `session.events` without a PTY runtime

**Files:**
- Create: `internal/server/events.go`
- Modify: `internal/server/handlers_events.go:27-46` (fields), `:66-129` (loop)
- Modify: `internal/server/server.go:24-63` (`hub *eventHub` built in `New`)
- Test: `internal/server/events_test.go`, `internal/server/handlers_events_test.go`

**Interfaces:**
- Produces: `type Hub interface { Publish(ev SessionEvent) }`; `type eventHub struct`; `func newEventHub() *eventHub`; `func (h *eventHub) Publish(ev SessionEvent)` (non-blocking; a full subscriber drops the event); `func (h *eventHub) Subscribe() (<-chan SessionEvent, func())` (buffer 64).
- Produces `SessionEvent` fields: `State string json:"state,omitempty"`, `Rev int64 json:"rev,omitempty"`, `Queued *int json:"queued,omitempty"`, `DeliveredAt string json:"delivered_at,omitempty"` (RFC3339Nano), `Kind string json:"kind,omitempty"`. Types: `todo_changed` (id, rev), `steer` (id, queued, delivered_at), reserved `usage` (id; nl-4), `notify` (id, kind; nl-5), `verify` (id = worker, state = review status; nl-7).
- Produces: `func collectHookActivityEvents(sessions []domain.Session, now time.Time, hookSeen map[string]SessionEvent) []SessionEvent` (the loop keeps `hookSeen` separate from the PTY `seen` map so `collectSessionEvents` does not report these sessions as gone/active in turn): one `session_activity` per non-PTY session whose `{Title: AgentTitle, Status, State}` changed, where `State` is `agenthook.ResolveHookState` (empty when not resolved).

- [ ] **Step 1: Write failing tests**

```go
func TestHubFansOutAndDropsWhenFull(t *testing.T)            // two subscribers both get ev; 65th publish to an unread sub does not block
func TestSessionEventsWithoutPTYRuntime(t *testing.T)        // New(ln, repo, nil, nil, nil, nil, "test") → EventsDial succeeds; a saved session yields session_created
func TestSessionEventsCarriesHubEvents(t *testing.T)         // srv.hub.Publish(todo_changed id=x rev=3) → reader sees Type "todo_changed", Rev 3
func TestCollectHookActivityEventsOnlyTmux(t *testing.T)     // pty session ignored; tmux session with fresh idle hook → State "idle"; unchanged second call → none
```

- [ ] **Step 2: Run** `go test ./internal/server -run 'Hub|WithoutPTY|HubEvents|HookActivity'` → FAIL.
- [ ] **Step 3: Implement.** Drop the `s.pty == nil` early return; call `collectSessionEvents` only when `s.pty != nil`; fold `collectHookActivityEvents` (with its own `hookSeen := map[string]SessionEvent{}`) into the existing repo branch; add a `case ev := <-hubCh` to the select that encodes immediately.
- [ ] **Step 4: Run** `go test ./internal/server` → PASS.
- [ ] **Step 5: Commit** `feat: stream session events without a PTY runtime and via a hub` (run `make ci` and `graphify update .` first)

---

### Task 7: To-do domain types and the file store

**Files:**
- Create: `internal/domain/todo.go`
- Create: `internal/todo/store.go`, `internal/todo/change.go`
- Test: `internal/todo/store_test.go`, `internal/todo/change_test.go`

**Interfaces:**
- Produces (`domain`): `TodoState` consts `TodoPending="pending"`, `TodoActive="active"`, `TodoDone="done"`, `TodoDropped="dropped"`, `func (s TodoState) Valid() bool`, `func (s TodoState) Glyph() string` (✓ ▸ ○ –); `TodoAuthor` consts `AuthorUser`, `AuthorAgent`, `AuthorAiman`, `AuthorVerifier`; `TodoItem`/`TodoList` exactly as the spec, with tags `id,text,state,updated_by,updated_at,ref,omitempty` and `session_id,items,rev,updated_at`, plus `TodoList.NotInAgent []string json:"not_in_agent,omitempty"`; `TodoSnapshotItem{Text string json:"text"; Status TodoState json:"status"}`; `func NormalizeVendorTodoStatus(s string) TodoState` (`in_progress`→active, `completed`→done, ours pass through, unknown→pending).
- Produces (`todo`): spec `Store` interface; `type Files struct`; `func NewFiles(root string) *Files`; `Get`; `Mutate`. File `<root>/<SafeSessionID>.json` holds `{"list":…, "next_id":n, "tombstones":[…]}` (last 50 tombstones); `type Tombstone struct{ ID string json:"id"; Text string json:"text"; RemovedAt time.Time json:"removed_at" }` (used by Task 8's `Merge`). Writes go to a temp file in the same dir, then rename; one `sync.Mutex` per session id.
- Produces: `OpKind` consts `OpAdd`, `OpEdit`, `OpState`, `OpRemove`, `OpMove`, `OpRef`; `type Op struct{ Kind OpKind; ID, Text string; State domain.TodoState; ToIndex int; Ref string }`; `type Change struct{ Author domain.TodoAuthor; Ops []Op; Rev int64 }`; `func (c Change) Empty() bool`; `func (c Change) SteersAgent() bool` (false when `Author == agent` or every op is `OpRef`); `func Diff(before, after domain.TodoList) []Op`.
- Mutate rules, after `fn`: reject empty text or invalid state (`ErrInvalid`, wrapped); assign `t<next_id>` to items with empty `ID` in order; if more than one item is `active`, keep the one whose state changed in this call (last in order) and demote the rest to `pending`; stamp per `stamp(prev, next, author, now)`: new or text changed → `UpdatedBy=author, UpdatedAt=now`; only state/position changed → same, except `author==agent` on a non-agent item leaves both untouched; add tombstones for removed ids; if `Diff` is empty return the unchanged list and an empty `Change` without writing; else `Rev++`, `UpdatedAt=now`, write.
- Diff: adds, removes, edits, state changes, `OpRef` when only an item's `Ref` changed (W8 links a fan-out child this way; without it `Mutate` would treat a Ref-only change as a no-op and drop it); moves are surviving items outside the longest increasing subsequence of their before-positions, `ToIndex` = new 0-based index. `OpRef` is excluded from steering notes (see Task 9 and Task 11).

- [ ] **Step 1: Write failing tests**

```go
func TestMutateAssignsNeverReusedIDs(t *testing.T)     // add t1,t2; remove t2; add → "t3"
func TestMutateKeepsOneActive(t *testing.T)            // t1 active; set t2 active → t1 pending, t2 active
func TestMutateNoopDoesNotBumpRev(t *testing.T)        // fn changes nothing → Rev unchanged, file mtime unchanged
func TestAgentStateChangeKeepsUserAuthor(t *testing.T) // user adds t1; agent sets done → UpdatedBy user, UpdatedAt unchanged
func TestMutateIsAtomicAndConcurrent(t *testing.T)     // 20 goroutines add once each → 20 items, 20 distinct ids (run with -race)
func TestGetMissingListIsEmpty(t *testing.T)           // Get("nope") → SessionID "nope", Rev 0, no items, nil error
func TestDiffReportsMove(t *testing.T)                 // [t1,t2,t3] → [t3,t1,t2] → exactly one OpMove{ID:"t3", ToIndex:0}
func TestDiffRefOnly(t *testing.T)                     // only t3.Ref changes → one OpRef; Mutate writes and bumps Rev; Change.SteersAgent() == false
```

- [ ] **Step 2: Run** `go test -race ./internal/todo` → FAIL.
- [ ] **Step 3: Implement** in the files above (inject `now func() time.Time` on `Files`, default `time.Now`).
- [ ] **Step 4: Run** `go test -race ./internal/todo ./internal/domain` → PASS.
- [ ] **Step 5: Commit** `feat: add the serve-owned to-do store` (run `make ci` and `graphify update .` first)

---

### Task 8: `todo.mirror` merge rule

**Files:**
- Create: `internal/todo/mirror.go`
- Modify: `internal/todo/store.go` (add `Mirror`)
- Test: `internal/todo/mirror_test.go`

**Interfaces:**
- Produces: `func Normalize(text string) string` (lowercase, strip `unicode.IsPunct`, collapse whitespace); `func Similarity(a, b string) float64` (token Jaccard of normalised texts); `func Merge(list domain.TodoList, tombs []Tombstone, snap []domain.TodoSnapshotItem, deliveredAt, now time.Time) domain.TodoList` (pure; returns the list with `NotInAgent` set; new items have empty `ID`); `func (f *Files) Mirror(sessionID string, snap []domain.TodoSnapshotItem, deliveredAt time.Time) (domain.TodoList, Change, error)` (runs `Merge` under the session lock, then the same id/active/stamp/write path as `Mutate` with author `agent`).
- Algorithm: spec rules 1–8 in order, with these guards. "Undelivered" means a non-agent item with `UpdatedAt.After(deliveredAt)`. Exact matches first, then best similarity ≥ 0.8 greedily by score; each existing item matches once. Rule 4 skips the status copy for undelivered items. Unmatched snapshot items whose normalised text matches a tombstone with `RemovedAt.After(deliveredAt)` are skipped. Rule 7 adds the id to `NotInAgent` only when the item is not undelivered. When any item is undelivered, the result keeps the current order and appends new items; otherwise rule 8 order.

- [ ] **Step 1: Write failing tests** (table-driven where natural)

```go
func TestMergeExactAndFuzzyMatch(t *testing.T)            // "Run preprod --check" ~ "run preprod check" (exact after Normalize); "Add rollback test to deploy script" vs "Add a rollback test to deploy script" ≥0.8 (threshold stays 0.8)
func TestMergeAgentItemTakesSnapshot(t *testing.T)        // rule 3: text, status, position
func TestMergeUserItemTakesOnlyStatus(t *testing.T)       // rule 4, delivered: text and UpdatedBy unchanged, State from snapshot
func TestMergeKeepsUndeliveredUserState(t *testing.T)     // user set t2 done after deliveredAt; snapshot says in_progress → stays done
func TestMergeDropsUnmatchedAgentItems(t *testing.T)      // rule 6 → State dropped, item kept
func TestMergeMarksDeliveredOmission(t *testing.T)        // rule 7 → NotInAgent == ["t4"]; undelivered → NotInAgent empty
func TestMergeSkipsUndeliveredTombstone(t *testing.T)     // user removed "Write docs" after deliveredAt; snapshot still has it → not re-added
func TestMergeKeepsOrderWhileUndelivered(t *testing.T)    // user moved t3 first, undelivered; snapshot order t1,t2,t3 → t3,t1,t2
func TestMergeEachExistingMatchesOnce(t *testing.T)       // two identical snapshot texts, one existing → one matched, one added
func TestMirrorNeverProducesAgentNoteAuthor(t *testing.T) // Change.Author == AuthorAgent
```

- [ ] **Step 2: Run** `go test ./internal/todo -run 'Merge|Mirror'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/todo` → PASS.
- [ ] **Step 5: Commit** `feat: merge agent to-do snapshots without losing user edits` (run `make ci` and `graphify update .` first)

---

### Task 9: `.aiman_todo.md` rendering and steering-note formatting

**Files:**
- Create: `internal/todo/render.go`, `internal/fsutil/write.go`, `internal/fsutil/write_test.go`
- Modify: `internal/domain/session_files.go` (add `AimanTodoFileName = ".aiman_todo.md"`, `AimanScaffoldHeader`)
- Modify: `internal/contextstore/files.go:539-555` (`WriteSessionPack` uses `domain.AimanScaffoldHeader`; output unchanged)
- Test: `internal/todo/render_test.go`, existing `internal/contextstore` tests

**Interfaces:**
- Produces: `domain.AimanScaffoldHeader` = the exact three-line comment plus the "> **Do not commit this file.** …" blockquote and blank line from `WriteSessionPack`.
- Produces: `func fsutil.WriteIfChanged(path string, content []byte, mode os.FileMode) (changed bool, err error)` (reads first; writes temp + rename only when bytes differ). It is the one local compare-before-write helper: nl-3a (`.aiman_handover.md`) and nl-8 (cache hygiene) use it instead of their own.
- Produces: `func FormatLine(it domain.TodoItem) string` (`"t3 ▸ Run preprod --check"`, the one line format; the CLI reuses it); `func FormatList(list domain.TodoList) string` (`FormatLine` per non-dropped item); `func FormatSteerNote(authors []domain.TodoAuthor, ops []Op, list domain.TodoList) string` (renders no line for `OpRef`); `func Render(list domain.TodoList) string`; `func WriteRendered(dir string, list domain.TodoList) (changed bool, err error)` (`fsutil.WriteIfChanged` on `dir/.aiman_todo.md`).
- Change lines: add `"+ t7 Add rollback test"`, edit `"~ t3 now: <text>"`, state done `"✓ t2 done"`, active `"▸ t3 active"`, pending `"○ t3 pending"`, dropped `"- t5 dropped"`, remove `"- t5 removed"`, move `"↕ t4 moved to <ToIndex+1>"`. Authors join as `"user"`, `"user and aiman"`, `"user, aiman and verifier"`.
- Render body: header, `# To-do list`, `- [x] t1 …` / `- [ ] t3 ▸ … (active)` / `- [ ] t4 …` / `- [~] t5 … (dropped)`, then ``Update it with `aiman todo`; ids are stable.``. No rev and no timestamps, so unchanged items give identical bytes.

- [ ] **Step 1: Write failing tests**

```go
func TestFormatSteerNoteExactText(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "Write spec", State: domain.TodoDone}, {ID: "t3", Text: "Run preprod --check --dry", State: domain.TodoActive}, {ID: "t4", Text: "Add rollback test", State: domain.TodoPending}}}
	ops := []Op{{Kind: OpAdd, ID: "t4", Text: "Add rollback test"}, {Kind: OpEdit, ID: "t3", Text: "Run preprod --check --dry"}}
	want := "[aiman] Your to-do list was changed by user:\n+ t4 Add rollback test\n~ t3 now: Run preprod --check --dry\nCurrent list:\nt1 ✓ Write spec\nt3 ▸ Run preprod --check --dry\nt4 ○ Add rollback test\nAdjust your work to match. Update the list with `aiman todo` using these ids."
	if got := FormatSteerNote([]domain.TodoAuthor{domain.AuthorUser}, ops, list); got != want { t.Fatalf("got\n%s", got) }
}
func TestWriteRenderedSkipsUnchanged(t *testing.T)   // second call with same items but Rev+1 → changed=false, mtime unchanged
func TestFormatSteerNoteSkipsRef(t *testing.T)       // ops [OpRef t3, OpAdd t4] → no line mentions t3's ref
func TestWriteIfChanged(t *testing.T)                // in internal/fsutil: identical bytes keep mtime; new bytes replace with the given mode
func TestRenderStartsWithScaffoldHeader(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/todo ./internal/fsutil ./internal/contextstore -run 'Format|Render|Pack|WriteIfChanged'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/todo ./internal/fsutil ./internal/contextstore` → PASS.
- [ ] **Step 5: Commit** `feat: render .aiman_todo.md and steering notes` (run `make ci` and `graphify update .` first)

---

### Task 10: Persist steering notes in serve's DB

**Files:**
- Create: `internal/domain/steer.go`
- Create: `internal/infra/sqlite/repository_steer.go`
- Modify: `internal/infra/sqlite/repository.go:171-182` (create table)
- Test: `internal/infra/sqlite/repository_steer_test.go`

**Interfaces:**
- Produces: `domain.SteerRecord{ID int64; SessionID string; Author TodoAuthor; Text string; CreatedAt, DeliveredAt time.Time}`.
- Produces: table `steer_notes(id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, author TEXT NOT NULL, text TEXT NOT NULL, created_at DATETIME NOT NULL, delivered_at DATETIME)` plus index on `(session_id, delivered_at)`. Methods: `AddSteerNote(ctx, *domain.SteerRecord) error` (sets `ID`), `ListUndeliveredSteerNotes(ctx) ([]domain.SteerRecord, error)` (ordered by id), `MarkSteerNotesDelivered(ctx, ids []int64, at time.Time) error`, `DeleteSteerNotes(ctx, sessionID string) error`, `LastSteerDelivery(ctx, sessionID string) (time.Time, error)` (max `delivered_at` for the session, zero when none; keeps the delivery guard of Task 8 correct after a restart).

- [ ] **Step 1: Write failing test** `TestSteerNotesRoundTrip`: add two for s1, one for s2; mark s1's first delivered; list → the other two in id order; delete s2 → one left; `LastSteerDelivery(s1)` equals the marked time, `LastSteerDelivery(s2)` is zero.
- [ ] **Step 2: Run** `go test ./internal/infra/sqlite -run SteerNotes` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/infra/sqlite` → PASS.
- [ ] **Step 5: Commit** `feat: persist undelivered steering notes` (run `make ci` and `graphify update .` first)

---

### Task 11: Steerer — queue, coalescing, reload, delivery loop

**Files:**
- Create: `internal/server/steer.go`
- Test: `internal/server/steer_test.go`

**Interfaces:**
- Consumes: `domain.SteerRecord` (Task 10), `todo.Op`, `todo.FormatSteerNote` (Task 9), `SessionEvent` (Task 6).
- Produces (spec names): `SteerNote`, `Steerer` interface with `Enqueue(n SteerNote)` and `Status(sessionID string) SteerStatus`.
- Produces: `type SteerNote struct{ SessionID string json:"session_id"; Author domain.TodoAuthor json:"author"; Text string json:"text" }` (spec shape plus tags).
- Produces: `type SteerStatus struct{ Queued int json:"queued"; LastDeliveredAt time.Time json:"last_delivered_at,omitzero"; LastError string json:"last_error,omitempty"; Held string json:"held,omitempty"; Pending []SteerNote json:"pending,omitempty" }` (`Held` ∈ `working|dialog|ended|unknown`; `Pending` = undelivered notes oldest first, read from the persisted queue; nl-3a puts them in the handover and the TUI does not need them).
- Produces: `type SteerStore interface { AddSteerNote; ListUndeliveredSteerNotes; MarkSteerNotesDelivered; DeleteSteerNotes }` (signatures from Task 10); `type steerTarget interface { steerReady(ctx context.Context, id string, lastDelivered time.Time) (bool, string); deliverSteer(ctx context.Context, id, text string) error; todoList(id string) (domain.TodoList, error) }`.
- Produces: `func newSteerer(store SteerStore, target steerTarget, publish func(SessionEvent), window time.Duration) *steerer`; methods `Enqueue`, `EnqueueTodo(sessionID string, author domain.TodoAuthor, ops []todo.Op)`, `Status`, `Kick(sessionID string)`, `Forget(sessionID string)`, `Load(ctx) error`, `Run(ctx)`.
- Behaviour: `EnqueueTodo` returns at once when every op is `OpRef`. The first enqueue for a session opens an 800 ms window (`window` param). At flush, accumulated todo ops (from all authors in the window) become one note via `FormatSteerNote` against `todoList(id)`, and free texts join with a blank line into a second note; each is persisted (`AddSteerNote`) and a `steer` event published. `Run` wakes on `Kick` or a 1 s ticker. For each session with undelivered notes it asks `steerReady`; when ready it joins all undelivered texts with a blank line, calls `deliverSteer`, then `MarkSteerNotesDelivered`, updates `LastDeliveredAt`, and publishes `steer`. On error it sets `LastError` and retries on the next tick. `Load` reads undelivered rows once at start and seeds each session's `LastDeliveredAt` from `LastSteerDelivery` (add it to `SteerStore`), so the merge guard survives a restart; `Forget` drops memory state and calls `DeleteSteerNotes`.

- [ ] **Step 1: Write failing tests** (fake store, fake target, `window = 20*time.Millisecond`)

```go
func TestSteererCoalescesWithinWindow(t *testing.T)        // two EnqueueTodo + one Enqueue inside the window → 2 records (todo note, free text), one todo note lists both ops
func TestSteererHoldsUntilReady(t *testing.T)             // target not ready("working") → no deliver; Status.Held=="working", Queued==1; flip ready + Kick → delivered once, Queued==0
func TestSteererDeliversAllQueuedTogether(t *testing.T)   // 2 notes queued while held → one deliverSteer call with both texts joined by "\n\n"
func TestSteererReloadsUndeliveredOnce(t *testing.T)      // store has 1 undelivered; Load twice; Run with ready target → exactly one deliver
func TestSteererRecordsDeliveryError(t *testing.T)        // deliver returns err → Status.LastError set, note still queued
func TestSteererPublishesSteerEvents(t *testing.T)        // publish sees Type "steer" with *Queued 1, then *Queued 0 and DeliveredAt set
func TestEnqueueTodoIgnoresRefOnly(t *testing.T)          // EnqueueTodo with only OpRef ops → nothing queued, no event
func TestSteerStatusListsPending(t *testing.T)            // 2 undelivered notes → Status().Pending has both texts, oldest first
func TestSteererLoadSeedsLastDelivered(t *testing.T)      // store has a delivered note at T; Load → Status().LastDeliveredAt == T (restart keeps the delivery guard)
```

- [ ] **Step 2: Run** `go test -race ./internal/server -run 'Steerer|EnqueueTodo|SteerStatus'` → FAIL.
- [ ] **Step 3: Implement** (one goroutine in `Run`; a mutex guards per-session state; flush timers via `time.AfterFunc`).
- [ ] **Step 4: Run** `go test -race ./internal/server -run 'Steerer|EnqueueTodo|SteerStatus'` → PASS.
- [ ] **Step 5: Commit** `feat: add the steering queue with coalescing and persistence` (run `make ci` and `graphify update .` first)

---

### Task 12: Delivery gate and direct delivery

**Files:**
- Create: `internal/server/steer_delivery.go`
- Modify: `internal/usecase/session_terminal.go:355-390` (export `PromptStillInComposer(pane, prompt string) bool` wrapping `submitMarker` + `composerStillHolds`)
- Modify: `internal/server/server.go` (declare fields `todos todoStore` and `steer *steerer`, plus `target steerTarget` defaulting to `s`; declare the `todoStore` interface here, Task 13 consumes it; `(*steerer).Kick` and `Forget` are nil-safe so handlers call them before `EnableTodos`)
- Modify: `internal/server/handlers_session.go:753-807` (`s.steer.Kick(id)` after a saved report), `:708-727` (forget calls `s.steer.Forget`)
- Test: `internal/server/steer_delivery_test.go`

**Interfaces:**
- Produces: `func (s *Server) steerReady(ctx context.Context, id string, lastDelivered time.Time) (bool, string)`: resolve the session (`loadSessions`); `AgentEnded` → `(false, "ended")`. If `agenthook.ResolveHookState` resolves: `idle` → ready only if `HookStateAt.After(lastDelivered)`; `waiting_input` with source `idle_prompt` → ready under the same freshness rule; `waiting_input` → `"dialog"`; else `"working"`. Otherwise `s.classify` (guard `s.pty == nil` for PTY sessions): confidence `High` and `idle` and `time.Since(lastDelivered) > 3*time.Second` → ready; `waiting_input` → `"dialog"`; else `"unknown"`.
- Produces: `func (s *Server) deliverSteer(ctx context.Context, id, text string) error`. PTY: `s.pty.Write(id, text)`, wait 500 ms, write `"\r"`, then up to 3 checks 1 s apart with `CaptureScreen` + `usecase.PromptStillInComposer`, resending `"\r"` when it still holds. Tmux: `usecase.SendPrompt(ctx, s.remote, sess.TmuxSession, sess.ID, text)`. Never calls `usecase.SendSessionPrompt`.
- Produces: `func (s *Server) todoList(id string) (domain.TodoList, error)` (delegates to the store). Tests replace the unexported `s.target steerTarget` (default `s`) to inject readiness and delivery.

- [ ] **Step 1: Write failing tests**

```go
func TestSteerHoldsWhileEnded(t *testing.T)                      // repo session AgentEnded → (false,"ended")
func TestSteerReadyOnFreshIdleHook(t *testing.T)                 // HookState idle, source lifecycle, HookStateAt=now → ready
func TestSteerReadyRequiresFreshIdleAfterDelivery(t *testing.T)  // HookStateAt before lastDelivered → (false, …)
func TestSteerHoldsOnDialog(t *testing.T)                        // waiting_input, source lifecycle → (false,"dialog")
func TestSteerReadyOnIdlePromptWaiting(t *testing.T)             // waiting_input, source idle_prompt, fresh → ready
func TestDeliverSteerToPTY(t *testing.T)                         // startPTYServer-style server with `cat` session id "st1"; deliverSteer("hello steer") → pty capture contains "hello steer"
func TestDeliverSteerToTmuxUsesLocalSendKeys(t *testing.T)       // fakeRemote: execs contain `tmux send-keys -t "work"` and no `aiman pty input`
func TestReportKicksSteerer(t *testing.T)                        // real newSteerer with fake store and a recording target; held note + report state idle → delivered (Kick reached the steerer)
```

- [ ] **Step 2: Run** `go test ./internal/server ./internal/usecase -run 'Steer|DeliverSteer|Kicks|PromptStill'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/server ./internal/usecase` → PASS.
- [ ] **Step 5: Commit** `feat: gate steering on agent readiness and deliver from serve` (run `make ci` and `graphify update .` first)

---

### Task 13: Socket API — `todo.*`, `todo.summary`, `steer.send`, author derivation, serve wiring

**Files:**
- Create: `internal/server/handlers_todo.go`
- Modify: `internal/server/server.go` (fields `todos`, `steer`, `target` already declared in Task 12; `EnableTodos` builds the steerer with `s.target`; dispatch; `Serve` starts `steer.Run` when enabled)
- Modify: `cmd/aiman/serve.go:91-98` (`todos := todo.NewFiles(filepath.Join(dir, "todos"))`, `srv.EnableTodos(todos, db)`)
- Test: `internal/server/handlers_todo_test.go`

**Interfaces:**
- Consumes: Tasks 6–12.
- Consumes: `type todoStore interface { todo.Store; Mirror(sessionID string, snap []domain.TodoSnapshotItem, deliveredAt time.Time) (domain.TodoList, todo.Change, error) }`; `func (s *Server) EnableTodos(store todoStore, notes SteerStore)` (builds the steerer with package var `steerCoalesceWindow = 800 * time.Millisecond`, which tests shorten, and calls `steer.Load`).
- Produces: `func deriveAuthor(method, callerID, targetID, as string) (domain.TodoAuthor, error)`: `todo.mirror` → agent; `callerID == targetID` → agent (for `steer.send` this is refused instead: `invalid_params` "a session cannot steer itself"); `as` empty or `"user"` → user; `"aiman"` → aiman only for `steer.send` (`aiman-trigger` calls the socket directly); this function is the only authorship check (`aiman rpc` forwards `as` untouched); other `as` → `invalid_params`.
- Produces: `func (s *Server) MutateTodos(ctx context.Context, sess domain.Session, author domain.TodoAuthor, fn func(*domain.TodoList) error) (domain.TodoList, error)` (exported; the entry point W4/W5/W8 use). After a non-empty change: `todo.WriteRendered(sess.WorkingDirectory, list)` (logged, not fatal; skipped when the dir is empty), publish `todo_changed{id, rev}`, and when `change.SteersAgent()` `s.steer.EnqueueTodo(id, author, change.Ops)`.
- Produces: `type TodoAccess struct{ s *Server }` with `Get(sessionID string) (domain.TodoList, error)` and `Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error` (resolves the session via `loadSessions`, then `MutateTodos`); `func (s *Server) TodoAccess() TodoAccess`. This is the one adapter serve hands to `ciwatch` (nl-6), `verify` (nl-7) and `fanout` (nl-9), whose consumer-side `Todos` interfaces all have exactly these two methods.
- Produces: `func (s *Server) Steerer() Steerer` (for `cmd/aiman/serve.go` wiring of W4); when to-dos are not enabled it returns a no-op `Steerer` value (never a typed-nil `*steerer` inside the interface).
- Methods (params; all take `session` resolved via `resolveSession(loadSessions)` and optional `as`): `todo.get` → `{"type":"todo_list","list":…,"steer":SteerStatus}`; `todo.add {text, after?, ref?}` → `{"type":"todo_list","list":…,"id":"t7"}` (`after` unknown → `not_found`); `todo.update {id, text?, state?}`; `todo.move {id, to_index}` (clamped); `todo.remove {id}`; `todo.mirror {items:[{text,status}]}` (status through `NormalizeVendorTodoStatus`; `deliveredAt` from `s.steer.Status(id).LastDeliveredAt`); `todo.summary {sessions?: [id…]}` (no `session` param; omitted `sessions` means every session serve knows) → `{"type":"todo_summary","sessions":{"<id>":{"done":n,"total":m,"active":"<active item text or empty>","rev":r}}}` (`total` excludes dropped items; sessions without a list are omitted; reads files only, one call per host for the dashboard's progress bars); `steer.send {session, text, as?, mode?}`:
  - `mode` `"note"` (default): `Enqueue(SteerNote{Author: derived})`, delivered through the W1 gate → `{"type":"steer_status","steer":…}`.
  - `mode` `"answer"`: never queued or persisted. Types `text` at once with `deliverSteer` only when `steerReady` reports `"dialog"` (the agent is in `waiting_input` on a permission/choice dialog); otherwise answers without typing. Result `{"type":"steer_status","steer":…,"answered":bool}`. `aiman-trigger`'s autonomous unblocker uses it (nl-6), so the unblocker keeps working while notes stay gated.
  - Unknown `mode` → `invalid_params`.
  Unknown item id → `not_found`. Without `EnableTodos` every method returns `invalid_params` "to-dos not enabled".

- [ ] **Step 1: Write failing tests** (server with sqlite `testRepo`, `todo.NewFiles(t.TempDir())`, a fake `steerTarget` assigned to `s.target`, never ready)

```go
func TestTodoAddFromUserSteers(t *testing.T)            // todo.add as user → list has t1, steer status Queued becomes 1 after the window
func TestTodoAddFromOwnAgentDoesNotSteer(t *testing.T)  // Request.Caller == session id → UpdatedBy agent; Queued stays 0
func TestTodoMirrorIsAlwaysAgent(t *testing.T)          // mirror with as:"user" → items UpdatedBy agent, no note
func TestTodoMutationRendersFile(t *testing.T)          // session WorkingDirectory=t.TempDir() → .aiman_todo.md exists and contains "t1"
func TestTodoMutationPublishesTodoChanged(t *testing.T) // subscribe to hub → todo_changed with Rev 1
func TestTodoUnknownItemIsNotFound(t *testing.T)        // todo.update id t9 → error code not_found
func TestDeriveAuthor(t *testing.T)                     // table: mirror/any → agent; caller==target → agent; ""/"user" → user; "aiman" → error
func TestSteerSendQueuesWithoutWrapper(t *testing.T)    // fake store sees text exactly "please rebase" (no "[aiman]")
func TestSteerSendRejectsSelf(t *testing.T)             // Request.Caller == target → invalid_params, nothing queued
func TestSteerSendAnswerTypesOnDialog(t *testing.T)     // target held "dialog" → deliverSteer called once, answered true, store untouched
func TestSteerSendAnswerSkipsWhenNotDialog(t *testing.T) // target held "working" → no deliver, answered false, nothing queued
func TestSteerSendAsAiman(t *testing.T)                 // as "aiman" → note Author aiman; as "aiman" on todo.add → invalid_params
func TestTodoSummary(t *testing.T)                      // two lists (one with a dropped item) → done/total/active per id; sessions:["a"] filters
func TestTodoAccessMutateUsesServePath(t *testing.T)    // TodoAccess().Mutate as aiman → todo_changed published and a note queued
```

- [ ] **Step 2: Run** `go test ./internal/server -run 'Todo|DeriveAuthor|SteerSend'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/server ./cmd/aiman` → PASS.
- [ ] **Step 5: Commit** `feat: expose to-dos and steering on the agent API` (run `make ci` and `graphify update .` first)

---

### Task 14: `aiman todo` CLI and `todo report --from-stdin`

**Files:**
- Create: `cmd/aiman/todo_cmd.go`
- Create: `internal/agenthook/todo.go`
- Modify: `cmd/aiman/main.go:73-80` (early `case "todo": return runTodo(parsed.Rest[1:], os.Stdin, os.Getenv)`)
- Modify: `cmd/aiman/session_cmd.go:205-208` (`"json": true` in `boolSessionFlags`)
- Test: `cmd/aiman/todo_cmd_test.go`, `internal/agenthook/todo_test.go`, `cmd/aiman/socket_only_dispatch_test.go` (add `{"todo", "list"}`)

**Interfaces:**
- Consumes: Task 13 methods; Task 1 fixtures.
- Produces: `func ExtractTodoSnapshot(raw []byte, readTasks func(listRef string) ([]domain.TodoSnapshotItem, error)) ([]domain.TodoSnapshotItem, bool)`: `ok` only when `tool_name` is `TodoWrite` with an array `tool_input.todos[]{content,status}`; or `TaskCreate`/`TaskUpdate` and Task 1 found a full-list source (`claude-tasks-full-list`), in which case the full list comes from the response or from `readTasks` (subject → text, `pending|in_progress|completed` through `NormalizeVendorTodoStatus`, deleted tasks omitted); or (if Task 1 said yes) `update_plan` with `tool_input.plan[]{step,status}`. An empty array is `ok` (the agent cleared its list). A Tasks payload without a readable full list is not `ok` (never mirror a partial list).
- Produces: `func runTodo(args []string, stdin io.Reader, getenv func(string) string) error`; `func formatTodoLines(list domain.TodoList) string` (`todo.FormatLine` per item, dropped items included as `t5 – text`; no second copy of the line format).
- CLI: `list | add TEXT… [--after ID] | start ID | done ID | drop ID | edit ID TEXT… | move ID POS | rm ID | report --from-stdin`, flags `--session NAME` (default `$AIMAN_ID`; neither → usage error), `--as user`, `--json`. `move` POS is 1-based (`to_index = POS-1`). Non-JSON output prints the list after every command. `report` always returns nil, prints nothing, needs `AIMAN_ID`, and calls `todo.mirror` only when `ExtractTodoSnapshot` is `ok`.

- [ ] **Step 1: Write failing tests**

```go
func TestExtractTodoSnapshotClaude(t *testing.T)          // fixture → 3 items, statuses pending/active/done
func TestExtractTodoSnapshotClaudeTasks(t *testing.T)     // TaskUpdate fixture + fake readTasks returning 3 tasks → 3 items; readTasks error → ok=false (skipped when Task 1 found no full-list source)
func TestExtractTodoSnapshotRejectsUnrelated(t *testing.T) // Bash PostToolUse, "not json", {"tool_name":"TodoWrite"} without todos → ok=false
func TestTodoReportNoopsOnGarbage(t *testing.T)           // runTodo(["report","--from-stdin"], "garbage", getenv AIMAN_ID=x) against a fake socket listener (AIMAN_SOCK) → returns nil, no output, the listener saw zero requests
func TestTodoMovePositionIsOneBased(t *testing.T)         // todoParams("move", ["t4","2"]) → to_index 1
func TestTodoRequiresSession(t *testing.T)                // no AIMAN_ID, no --session → errUsage
```
Factor `todoParams(cmd string, rest []string, flags map[string]string) (method string, params map[string]any, err error)` so the tests need no socket.

- [ ] **Step 2: Run** `go test ./cmd/aiman ./internal/agenthook -run 'Todo|ExtractTodo|SocketOnly'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./cmd/aiman ./internal/agenthook` → PASS.
- [ ] **Step 5: Commit** `feat: add the aiman todo CLI and hook reporter entry point` (run `make ci` and `graphify update .` first)

---

### Task 15: Hook registration — to-do reporter and turn hooks

**Files:**
- Modify: `internal/agenthook/script.go` (`hookScript(subcommand string) string` generates both scripts: the existing reporter and the todo reporter; `todoReporterPath(home)` = `~/.aiman/hooks/todo-report`)
- Modify: `internal/agenthook/native.go:15` (add `TodoMarker = "todo-report"`)
- Modify: `internal/agenthook/merge.go:108-169` (`hasMarker`/`replaceMarkedCommand`/`upsertClaudeHook` take a marker; a found group with a different `matcher` gets it rewritten), add `upsertTurnHooks(root, command)` (`Stop`, `UserPromptSubmit`) and `upsertTodoHook(root, command, matcher)` (`PostToolUse`)
- Modify: `internal/agenthook/install.go:24-89` (write the todo script; `ensureClaude` adds turn hooks + `PostToolUse` matcher `TodoWrite|TaskCreate|TaskUpdate` (the Tasks names only if Task 1 found a full-list source); `ensureCodex` adds turn hooks + `update_plan` only if Task 1 said yes)
- Test: `internal/agenthook/install_test.go`

aiman does not set `CLAUDE_CODE_ENABLE_TASKS`; Claude keeps its Tasks tools.

**Interfaces:**
- Produces: the todo script is `hookScript("todo report --from-stdin")`: identical to the reporter script except the last command is `"$bin" todo report --from-stdin >/dev/null 2>&1 || true`. Writing it adds a result to `EnsureOnHost`: update the existing install test to expect 3 results.
- Produces: `upsertClaudeHook(root, event, command, matcher, marker string) bool` (marker parameter added). nl-3a's `upsertCompactHook` calls it with `Marker`.

- [ ] **Step 1: Write failing tests**

```go
func TestEnsureClaudeRegistersTodoAndTurnHooks(t *testing.T) // settings.json has PostToolUse matcher "TodoWrite|TaskCreate|TaskUpdate" (or "TodoWrite") → todo-report; Stop and UserPromptSubmit → report-agent-session.sh
func TestEnsureOnHostIsIdempotent(t *testing.T)              // run twice → second run all ActionCurrent; PostToolUse has exactly one aiman group
func TestUpsertTodoHookUpdatesMatcher(t *testing.T)          // existing group matcher "TodoWrite" → re-run with "TodoWrite|update_plan" rewrites it, no duplicate
func TestEnsureCodexTurnHooks(t *testing.T)                  // ~/.codex/hooks.json has Stop and UserPromptSubmit
```
Existing tests that count hook groups for Copilot/Grok/Cursor must keep passing unchanged (turn hooks are Claude and Codex only).

- [ ] **Step 2: Run** `go test ./internal/agenthook -run 'Ensure|UpsertTodo'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/agenthook` → PASS.
- [ ] **Step 5: Commit** `feat: mirror Claude and Codex to-do tools and report turn ends` (run `make ci` and `graphify update .` first)

---

### Task 16: Skill text, docs, full CI

**Files:**
- Modify: `internal/aimanskill/SKILL.md` (new `## Your to-do list` section after `## Prompt and wait`, before `## Run a command in another terminal`)
- Modify: `internal/aimanskill/install_test.go` (extend `TestSkillDocumentsContextCLI` or add `TestSkillDocumentsTodoCLI`)
- Modify: `AGENTS.md` (repo layout: `internal/todo`; session model: `BaseBranch` (also add `base_branch` to the COALESCE-protected field list); serve section: register, to-dos, steering, hub; gotcha on the `AgentEnded` clear path)

**Interfaces:**
- Section content: every session has a to-do list owned by serve and shown to the user; at the start of a task write your plan into it with `aiman todo add`, and keep it current; `aiman todo list` shows ids (`t3 ▸ …`); use `aiman todo add|start|done|drop|edit|move|rm` with those ids; keep exactly one item active; this is the list the user sees and edits, so use it even if your own to-do tool also exists (Claude's `TodoWrite` and Tasks tools are mirrored when used, but do not rely on that); messages starting `[aiman]` come from the operator or aiman: follow them and update the list with the ids they name; `.aiman_todo.md` is generated, do not edit or commit it.

- [ ] **Step 1: Write the failing test** `TestSkillDocumentsTodoCLI`: `Text` contains `aiman todo list`, `aiman todo done`, `[aiman]` and `.aiman_todo.md`.
- [ ] **Step 2: Run** `go test ./internal/aimanskill -run Todo` → FAIL.
- [ ] **Step 3: Write the section and AGENTS.md updates.**
- [ ] **Step 4: Run** `make ci` → green apart from `TestOllamaIntelligenceSummariseSession`; then `graphify update .`.
- [ ] **Step 5: Commit** `docs: teach agents the to-do list and document serve's new state`

---

## Open questions

1. **Codex mirroring** depends on Task 1. If Codex hooks do not expose `update_plan`, Codex agents rely on the CLI and the skill text.
2. **Tasks-tool mirroring** depends on Task 1 finding a full-list source. If there is none, Tasks-tool sessions are covered by the CLI only, which the skill already makes the primary path.
3. **Cross-plan contract:** the names in Spec deviation 9 are used verbatim by nl-2…nl-9; see `2026-10-22-nl-0-roadmap.md`.
4. **Orphaned files:** `session.forget` clears the steering queue but leaves `~/.aiman/todos/<id>.json` for W6a's handover; nothing prunes them yet.
