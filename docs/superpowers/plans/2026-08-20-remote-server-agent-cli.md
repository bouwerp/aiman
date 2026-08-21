# Remote Server and Agent CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run `aiman serve` on remotes, give in-pane agents a JSON CLI to list / get / create / prompt / wait / read / rename / move sessions, give every session a unique **name** and a **group**, add dashboard **quick start** (`N`) and **rename** (`R`), and teach that CLI with a bundled skill gated on `AIMAN_ENV=1`.

**Architecture:** One `aiman` binary. `serve` listens on `~/.aiman/aiman.sock` (NDJSON, `0o600`). `session` subcommands are a thin socket client. Handlers reuse `FlowManager.CreateSession`, a new `SendPrompt` extracted from `DeliverInitialPrompt`, `pane.Classify`, and `SessionDiscoverer`. tmux stays the multiplexer. Spec: `docs/superpowers/specs/2026-08-20-remote-server-agent-cli-design.md`.

**Tech Stack:** Go 1.26, stdlib `net` Unix sockets, existing sqlite / local.Executor / bubbletea v1 TUI (TUI only in PR 5).

---

## File Structure

- `internal/server/protocol.go` — request/response types, error codes
- `internal/server/socket.go` — path, chmod 0600, flock, listen
- `internal/server/server.go` — accept loop, dispatch
- `internal/server/handlers_session.go` — list/get/create/rename/move/prompt/wait/read
- `internal/domain/session.go` — `Name`, `Group`
- `internal/infra/sqlite/repository.go` — `name`, `group_name` columns + COALESCE
- `internal/server/*_test.go`
- `cmd/aiman/main.go` — dispatch: serve, --skill, session, TUI guard
- `cmd/aiman/serve.go`
- `cmd/aiman/session_cmd.go`
- `cmd/aiman/skill.go`
- `internal/infra/local/executor.go` — real discovery (today stubs)
- `internal/usecase/flow_manager.go` — `SendPrompt`; AIMAN_* env
- `internal/infra/skills/engine.go` — write skill files into worktree
- `skills/aiman/SKILL.md`
- `internal/ui/dashboard.go` — `ensureRemoteServer` (PR 5)

---

## Phase 1: Socket, ping, list, get

### Task 1.1: Protocol types and codec

**Files:**
- Create: `internal/server/protocol.go`
- Test: `internal/server/protocol_test.go`

- [ ] **Step 1: Write failing tests for encode/decode**

Round-trip a request and a success/error response. Unknown methods must decode (dispatch rejects them). Extra JSON fields must be ignored.

- [ ] **Step 2: Implement `Request`, `Response`, `Error`**

```go
type Request struct {
    ID     string          `json:"id"`
    Method string          `json:"method"`
    Params json.RawMessage `json:"params"`
}

type Response struct {
    ID     string `json:"id"`
    Result any    `json:"result,omitempty"`
    Error  *Error `json:"error,omitempty"`
}

type Error struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
```

Codes: `invalid_params`, `not_found`, `name_taken`, `server_not_running`, `already_running`, `agent_blocked`, `timeout`, `create_failed`, `jira_unavailable`, `protocol_mismatch`.

- [ ] **Step 3: `go test ./internal/server/`**

### Task 1.2: Listen + flock

**Files:**
- Create: `internal/server/socket.go`
- Test: `internal/server/socket_test.go`

- [ ] **Step 1: Failing test: second Listen on the same path returns `already_running`; after Close, a new Listen succeeds.** Use `t.TempDir()` for the socket.

- [ ] **Step 2: Implement `Listen(dir string) (*net.UnixListener, error)`**

Socket path `filepath.Join(dir, "aiman.sock")`, lock `aiman.sock.lock`. `os.Remove` stale sock only if lock is acquired. `chmod 0600`.

- [ ] **Step 3: `go test ./internal/server/`**

### Task 1.3: Accept loop and ping

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

- [ ] **Step 1: Failing test: dial, write `{"id":"1","method":"ping","params":{}}\n`, read `pong` with `version` and `protocol`.**

- [ ] **Step 2: Implement `Server.Serve(ctx)`** one JSON line in, one JSON line out. Unknown method → `invalid_params`. Context cancel closes the listener.

- [ ] **Step 3: `go test ./internal/server/`**

### Task 1.4: Local executor discovery

**Files:**
- Modify: `internal/infra/local/executor.go`
- Test: `internal/infra/local/executor_test.go`

- [ ] **Step 1: Failing tests for `ScanTmuxSessions` / `GetTmuxSessionEnv` against a fake PATH or skipped-if-no-tmux.** Prefer extracting the shell snippets already in `internal/infra/ssh/discovery.go` so SSH and local cannot drift. If extracting is a large refactor, copy the scripts into local for this PR and leave a TODO; do not change SSH behaviour.

- [ ] **Step 2: Implement the stubbed `RemoteExecutor` methods that list needs:** `ScanTmuxSessions`, `GetTmuxSessionCWD`, `GetTmuxSessionEnv`, `CaptureTmuxPane`, `GetGitRoot`. Implement `BatchDiscovery` if the scripts drop in cleanly.

- [ ] **Step 3: `go test ./internal/infra/local/`**

### Task 1.5: Name and group on the session model

**Files:**
- Modify: `internal/domain/session.go`
- Modify: `internal/infra/sqlite/repository.go`
- Test: `internal/infra/sqlite/repository_test.go`
- Create: `internal/domain/session_name.go` (validate + auto-assign) and `_test.go`

- [ ] **Step 1: Failing tests:** charset reject (dots, empty, too long); uniqueness is case-insensitive; auto-assign `impl` then `impl-2`; quick-assign `q1` then `q2` in group `quick`; group falls back issue key → repo short name → `ungrouped`; `Save` COALESCE keeps a non-empty name when discovery sends `""`.

- [ ] **Step 2: Add `Name` / `Group` to `Session` and `SessionConfig`. ALTER TABLE `name`, `group_name`. COALESCE both. Backfill helper used by list.**

- [ ] **Step 3: `go test ./internal/domain/ ./internal/infra/sqlite/`**

### Task 1.6: session.list / session.get handlers

**Files:**
- Create: `internal/server/handlers_session.go`
- Test: `internal/server/handlers_session_test.go` with a fake discoverer/repo

- [ ] **Step 1: Failing tests:** empty list; list merges DB agent_name/name/group onto a live tmux row; get unknown id → `not_found`; get by **name**, `group/name`, uuid, and tmux name; `list --group` filters; JSON sorted by group then name.

- [ ] **Step 2: Implement list/get.** Merge rule: live tmux presence + cwd win; empty live strings do not overwrite DB (same COALESCE idea as `sqlite.Repository.Save`). Backfill name/group when empty and persist.

- [ ] **Step 3: `go test ./internal/server/`**

### Task 1.7: CLI dispatch and TUI guard

**Files:**
- Modify: `cmd/aiman/main.go`
- Create: `cmd/aiman/serve.go`, `cmd/aiman/session_cmd.go`
- Test: `cmd/aiman/main_test.go` or a small `internal/cli` package if `package main` tests get awkward

- [ ] **Step 1: Failing tests:** `AIMAN_ENV=1` + no extra args → exit 2, no TUI; `session list` with no socket → stderr JSON `server_not_running` exit 1.

- [ ] **Step 2: Wire `serve` (load config, db, local executor, `Server.Serve`) and `session list|get` as socket clients (`list --group`). Default socket `~/.aiman/aiman.sock`.**

- [ ] **Step 3: `go test ./cmd/aiman/...` and `go build ./cmd/aiman`**

---

## Phase 2: Read, prompt, wait

### Task 2.1: Extract `SendPrompt`

**Files:**
- Modify: `internal/usecase/flow_manager.go`
- Modify: `internal/usecase/flow_manager_test.go`

- [ ] **Step 1: Failing test: `SendPrompt` writes the prompt file and runs `tmux send-keys -l -- "$(cat …)"` + Enter. Prompt containing `$(reboot)` and quotes must not appear interpolated in the command string (reuse `TestSendKeysScriptReadsPromptFromFile` pattern).**

- [ ] **Step 2: Implement `SendPrompt`. `DeliverInitialPrompt` keeps startup/agy wait, then calls it.**

- [ ] **Step 3: `go test ./internal/usecase/ -count=1`**

### Task 2.2: session.read

**Files:**
- Modify: `internal/server/handlers_session.go`
- Test: handler test with a stub `CaptureTmuxPane`

- [ ] **Step 1: Failing test returns pane text and requested line count.**

- [ ] **Step 2: Implement `session.read`. No gzip.**

### Task 2.3: session.prompt and session.wait

**Files:**
- Modify: `internal/server/handlers_session.go`, `cmd/aiman/session_cmd.go`

- [ ] **Step 1: Failing tests:**
  - prompt on `waiting_input` → `agent_blocked`, no SendPrompt call
  - prompt `--force` on `waiting_input` does call SendPrompt
  - prompt+wait returns when classify becomes `idle`
  - wait timeout → `timeout`
  - per-session mutex: two concurrent prompts against the same id are serialized (second starts after first SendPrompt returns)

- [ ] **Step 2: Implement. `--until blocked` maps to `waiting_input`. Default wait timeout 120s; `0` means none. Poll classify every 500ms.**

- [ ] **Step 3: CLI: `aiman session prompt|wait|read`.**

- [ ] **Step 4: `go test ./internal/server/ ./internal/usecase/ ./cmd/aiman/...`**

---

## Phase 3: session create, rename, move

### Task 3.1: create handler

**Files:**
- Modify: `internal/server/handlers_session.go`, `cmd/aiman/session_cmd.go`, `internal/usecase/flow_manager.go`

- [ ] **Step 1: Failing tests with a fake FlowManager-like interface (do not boot git).** Required: `--repo`, `--branch`, `--agent`, **or** `--quick --agent`. Optional `--name`/`--group`/prompt/dir/issue. Auto-assign `impl` + issue-key group when flags omitted. `--quick` sets ad-hoc, group `quick`, name `q1`/`q2`. Duplicate `--name` → `name_taken` and no CreateSession. `--issue` with no JIRA config → `jira_unavailable`. JSON includes `name` and `group`. Tmux session name uses sanitized `name`, not the branch, with short-id fallback on clash.

- [ ] **Step 2: If `FlowManager` is too concrete to fake, introduce a one-method interface at the handler:**

```go
type sessionCreator interface {
    CreateSession(ctx context.Context, cfg domain.SessionConfig) (*domain.Session, error)
}
```

Defined next to the server, not in `domain`. Only the handler depends on it.

- [ ] **Step 3: Implement CLI flags including `--name` and `--group`. No mutagen. No AWS flags.**

- [ ] **Step 4: `go test ./internal/server/`**

### Task 3.2: rename and move

**Files:**
- Modify: `internal/server/handlers_session.go`, `cmd/aiman/session_cmd.go`

- [ ] **Step 1: Failing tests:** rename to a taken name → `name_taken`; rename does not change `TmuxSession`; invalid charset → `invalid_params`; move changes `group` only; target resolution accepts name and `group/name`.

- [ ] **Step 2: CLI `aiman session rename <target> NEW` and `aiman session move <target> --group GROUP`.**

- [ ] **Step 3: `go test ./internal/server/`**

---

## Phase 4: Skill and env

### Task 4.1: Skill file and `--skill`

**Files:**
- Create: `skills/aiman/SKILL.md`
- Create: `cmd/aiman/skill.go`
- Modify: `cmd/aiman/main.go`

- [ ] **Step 1: Write `SKILL.md` matching the spec (gate, CLI recipe, `--name`/`--group "$AIMAN_GROUP"` helper spawn, safety rules). No pane-split instructions.**

- [ ] **Step 2: `go:embed` and `aiman --skill` prints it verbatim. Help footer on `aiman session` points at `--skill`.**

- [ ] **Step 3: Test: output contains `AIMAN_ENV` and `aiman session list`.**

### Task 4.2: Env injection

**Files:**
- Modify: `internal/usecase/flow_manager.go` (`tmuxEnvFlags` / new-session)
- Modify: `internal/ui/dashboard.go` restart env list (~7055)
- Test: `internal/usecase/flow_manager_test.go`

- [ ] **Step 1: Failing test: create command line contains `-e AIMAN_ENV=1`, `AIMAN_SOCKET_PATH`, `AIMAN_BIN_PATH`, `AIMAN_SESSION_ID`, `AIMAN_SESSION_NAME`, `AIMAN_GROUP`.**

- [ ] **Step 2: Implement. Restart path sets the same keys. Secrets cannot override them.**

### Task 4.3: PrepareSession writes the skill

**Files:**
- Modify: `internal/infra/skills/engine.go`
- Modify: `internal/domain` gitignore helper if needed
- Test: `internal/infra/skills/engine_test.go`

- [ ] **Step 1: Failing test: PrepareSession writes `.agents/skills/aiman/SKILL.md` and `.claude/skills/aiman/SKILL.md` under the worktree (use a temp remote executor).**

- [ ] **Step 2: Extend `EnsureAimanSessionFilesGitignored` to cover those paths.**

- [ ] **Step 3: `go test ./internal/infra/skills/ ./internal/usecase/`**

---

## Phase 5: TUI install and discovery

### Task 5.1: ensureRemoteServer

**Files:**
- Modify: `internal/ui/dashboard.go` (next to `ensureRemoteDaemon` ~5742)
- Test: extract the install command string if the function is untestable in-place; otherwise a small helper in `internal/usecase` that returns the remote commands.

- [ ] **Step 1: Helper builds:**
  1. `curl …/install.sh | sh` (BINARY_NAME=aiman)
  2. `tmux has-session -t aiman-serve || tmux new-session -d -s aiman-serve 'aiman serve'`

- [ ] **Step 2: Call from the same place trigger is ensured. Failure is a warning, not a create-session failure.**

### Task 5.2: Discovery prefers session list

**Files:**
- Modify: `internal/usecase/session_discoverer.go` or dashboard `runDiscovery`
- Test: `internal/usecase/session_discoverer_test.go`

- [ ] **Step 1: Failing test: when `aiman session list` over the executor returns JSON, those rows are used. When it errors, existing tmux scan runs.**

- [ ] **Step 2: Implement. Save still goes through COALESCE.**

- [ ] **Step 3: Doctor warning if serve is down (optional, keep splash ungated).**

### Task 5.3: Grouped sidebar

**Files:**
- Modify: `internal/ui/dashboard.go` (`item.Title`, session list construction)
- Test: `internal/ui/dashboard_test.go` or a focused list-item test

- [ ] **Step 1: Failing test: two sessions in group `WTB-1925` named `impl` and `reviewer` render a header row plus two children; rollup is `waiting_input` if either child is. `FilterValue` includes name and group.**

- [ ] **Step 2: Non-selectable group headers. Child title is `name` plus activity. `[remote]` stays on the header when more than one remote is listed. No TUI rename/move keys in this PR.**

- [ ] **Step 3: `go test ./internal/ui/`**

---

## Phase 6: TUI quick start and rename

### Task 6.1: Quick start (`N`)

**Files:**
- Modify: `internal/ui/dashboard.go` (key `N`, default-remote helper, skip summary)
- Modify: `internal/ui/creating_session.go` (placeholder `Name` / `Group`)
- Test: `internal/ui/dashboard_test.go` or a focused `quick_session_test.go`

- [ ] **Step 1: Failing tests:** `N` with no remotes stays on main; `N` with `active_remote` / `remotes[0]` / dashboard `f` filter picks that host and goes to agent scan; Enter on an agent starts `startBackgroundCreate` with `AdHoc`, `PromptFree`, `Group=quick`, `Name=q1` (then `q2`); does not enter `viewStateSummary` or `viewStateRunTargetPicker`. Esc from agent picker returns to main.

- [ ] **Step 2: Implement. Help footer: `N` quick session. AWS config copied from `ResolveAWSSessionDefaults` when a syncing delegation exists, with no summary screen. Mutagen still runs (laptop path).**

- [ ] **Step 3: `go test ./internal/ui/`**

### Task 6.2: Dashboard rename (`e`)

**Files:**
- Modify: `internal/ui/dashboard.go` (new `viewStateRenameSession`, `R` binding)
- Reuse: `internal/ui/text_input.go`
- Test: rename charset / `name_taken` / Save

- [ ] **Step 1: Failing tests:** `e` opens input prefilled with `Name`; Enter with a taken name on the same host does not Save; valid rename updates `Session.Name` and list title; tmux name unchanged; `r` still refreshes; `R` still starts AWS refresh.

- [ ] **Step 2: After PR 3, if serve is up, also `ssh host aiman session rename`. Serve-down is a warning, laptop Save still happens.**

- [ ] **Step 3: `go test ./internal/ui/`**

---

## Phase 3 verification (every PR)

- [ ] `gofmt` / `go test ./...` for the packages touched
- [ ] `make ci` before merge (ignore known `TestOllamaIntelligenceSummariseSession`)
- [ ] No bubbletea v2, no new HTTP server, no prompt interpolation
- [ ] `graphify update .` after code lands

---

## Dependencies

- Phase 2 depends on Phase 1 socket + get
- Phase 3 depends on Phase 1 (and uses Phase 2 prompt if `--prompt` is set)
- Phase 4 depends on Phases 1–3 so the skill documents real commands
- Phase 5 depends on Phase 1 at minimum; better after 4 so new sessions have env+skill
- Phase 6 depends on Phase 1 (Name/Group). It does not depend on serve. Rename forwards to the CLI after Phase 3.

## Acceptance Criteria

- [ ] On a remote with `aiman serve` running, an in-pane agent with `AIMAN_ENV=1` can `aiman session list` and see itself plus siblings as JSON with `name` and `group`
- [ ] `aiman session prompt reviewer "…"` resolves the name and delivers via file-backed send-keys; metacharacters are not executed
- [ ] `aiman session prompt reviewer "…" --wait` returns when classify is idle or waiting_input, or times out
- [ ] `aiman session create --repo --branch --agent --name reviewer --group "$AIMAN_GROUP"` starts tmux + worktree on the box and `list --group` shows it next to the caller
- [ ] `aiman session create --quick --agent claude` creates ad-hoc `q1` in group `quick`
- [ ] Duplicate `--name` returns `name_taken` and does not create
- [ ] `aiman session rename` / `move` change DB fields only; tmux session name is unchanged
- [ ] Dashboard `N` picks an agent on the default remote and starts a `quick`/`q{n}` session without the summary screen
- [ ] Dashboard `R` renames the selected session; `r` still refreshes
- [ ] Dashboard sidebar groups by `Group` with rolled-up state; child rows show `Name`
- [ ] `aiman --skill` prints the skill; PrepareSession installs it in the worktree
- [ ] Laptop TUI still creates/restarts/discovers sessions if serve is down
- [ ] Mutagen is not started by remote create
- [ ] `aiman-trigger` still builds and installs unchanged
