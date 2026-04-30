# AGENTS.md — Technical Context for AI Agents

This file documents the architecture, key files, known gotchas, and current state of **Aiman** so that any AI coding agent can continue development without needing prior context.

---

## What Is Aiman?

Aiman is a **terminal UI (TUI) orchestrator** written in Go. It manages the full lifecycle of remote AI coding sessions:

- Turns a JIRA ticket into a git worktree + tmux session + mutagen sync + AI agent in one flow
- Tracks active sessions, provides live pane previews, git status, and AI summaries
- Supports Claude Code, Gemini CLI, GitHub Copilot, OpenCode, and Cursor as agents

Binary: `aiman` — built from `./cmd/aiman/main.go`  
Module: `github.com/bouwerp/aiman`  
Go: 1.26  
Current release: **v0.6.57**

---

## Build & Test

```bash
make ci          # fmt + vet + test + lint (full CI — run before every commit)
make build       # go build -o aiman ./cmd/aiman
go build ./...   # quick build check
go test ./...    # tests (skip -race if Go stdlib version mismatch)
```

**Known pre-existing test failure:** `TestOllamaIntelligenceSummariseSession` in `internal/infra/ai` — requires a local Ollama instance, always fails in CI. Ignore it.

**Release process:**
```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```
GitHub Actions builds cross-platform binaries and creates the release automatically.

---

## Repository Layout

```
cmd/aiman/          — main.go entry point
internal/
  domain/           — pure Go domain types and interfaces (no external deps)
    session.go      — Session, SessionConfig, SessionStatus, GitStatus, PullRequest, Secret
    interfaces.go   — all domain interfaces (RemoteExecutor, SyncEngine, etc.)
    snapshot.go     — SessionSnapshot type
    agent.go        — Agent type
    skills.go       — Skill type
    aws.go          — AWSConfig type
  usecase/
    flow_manager.go — CreateSession (the main session-creation orchestrator)
    session_discoverer.go — discovers sessions from live remote (tmux/git/mutagen)
    snapshot.go     — SnapshotManager (archive, compress, AI summarise)
    doctor.go       — startup health checks
  infra/
    ssh/manager.go  — SSH ControlMaster multiplexer (Execute, WriteFile, ResetControlSocket)
    sqlite/repository.go — SQLite persistence (sessions + snapshots + secrets)
    mutagen/        — mutagen sync engine
    git/            — git worktree management, PR metadata via gh CLI
    jira/           — JIRA Cloud API v3
    ai/             — AI summarisation (Ollama)
    skills/         — SkillEngine (reads skills repo, PrepareSession)
    config/         — ~/.aiman/config.yaml read/write
    awsdelegation/  — STS token push to remote profiles
    agent/          — agent scanning on remote
  pane/
    clean.go        — strips ANSI/noise from tmux pane captures
  ui/
    dashboard.go    — entire TUI (5000+ lines); all views, key handlers, message types
```

---

## The Session Data Model

A `domain.Session` holds:

| Field | Description |
|---|---|
| `ID` | UUID — primary key everywhere |
| `IssueKey` | JIRA key, e.g. `PROJ-123` |
| `Branch` | git branch name |
| `RepoName` | `owner/repo` GitHub slug |
| `RemoteHost` | SSH host (from config) |
| `WorktreePath` | absolute path to git worktree on the remote |
| `WorkingDirectory` | active sub-directory inside worktree (what the agent cwd is) |
| `TmuxSession` | tmux session name on the remote |
| `MutagenSyncID` | mutagen sync session name (usually `aiman-sync-<ID>`) |
| `LocalPath` | `~/.aiman/work/<ID>` — local mutagen sync root |
| `AgentName` | agent binary name, e.g. `claude` |
| `Status` | `PROVISIONING → ACTIVE → CLEANUP` (or `ERROR`/`INACTIVE`) |
| `Tunnels` | local↔remote port forwards |
| `AWSProfileName` | `aiman-<id[:8]>` scoped profile on the remote |

---

## SQLite Persistence — Critical Design Decision

**File:** `internal/infra/sqlite/repository.go`

The `Save` method uses `INSERT … ON CONFLICT(id) DO UPDATE SET`. As of **v0.6.57**, all critical session fields use:

```sql
field = COALESCE(NULLIF(excluded.field, ''), sessions.field)
```

**Why this matters:** The session discoverer (`Discover`) builds sessions from live remote state — it can only know what it can read from a running tmux process. Fields like `Branch`, `AgentName`, `WorktreePath`, and `WorkingDirectory` will often be empty in discovered sessions. Before this fix, discovery was silently overwriting the DB with empty strings on every scan cycle, causing restart to fail with "session has no working directory".

**Fields protected by COALESCE:** `issue_key`, `branch`, `repo_name`, `remote_host`, `worktree_path`, `working_directory`, `tmux_session`, `mutagen_sync_id`, `local_path`, `agent_name`.

**Fields always overwritten:** `status`, `updated_at`.

**Fields with null-safe COALESCE (not empty-string):** `tunnels_json`, `aws_profile`, `aws_config_json`.

---

## Session Discovery

**File:** `internal/usecase/session_discoverer.go`

`Discover(ctx, host)` scans the remote for running tmux sessions, correlates them with mutagen syncs, and returns `[]domain.Session`. It can only populate fields it can read live:

- `TmuxSession`, `WorkingDirectory` (from `AIMAN_ID` env var in tmux pane)
- `MutagenSyncID` (from `mutagen sync list`)
- Basic git branch info if the CWD is a git repo

It does **not** read `Branch`, `AgentName`, `IssueKey`, `WorktreePath`, or `LocalPath` — those come from the DB.

The `discoveryResultMsg` handler in `dashboard.go` (~line 4269) saves all discovered sessions to DB after each discovery cycle. This is where the stale-reference bug lived; it's now safe because of the COALESCE fix.

---

## SSH Manager

**File:** `internal/infra/ssh/manager.go`

Key behaviours:
- Uses `ControlMaster=auto` + `ControlPersist=10m` — all calls to the same host share one socket
- Socket path: `~/.aiman/sockets/ssh-<sha1(user@host)[:16]>.sock`
- Per-call timeout: **30 seconds** (via `context.WithTimeout`)
- On transport errors: clears socket, retries up to 2×, then falls back to `ControlMaster=no`
- `ResetControlSocket()`: sends `ssh -O exit` to gracefully stop the master, then removes the socket file. Call this before disruptive remote operations (e.g. tmux kill-session) to ensure a clean connection for the next call.

**Do not** add "permission denied" removal from `isRetriableSSHTransportError` without testing — it causes 4× retries on auth failures which wastes 120s.

---

## Session Restart

**File:** `internal/ui/dashboard.go` — `restartSession()` (~line 4885)

The restart flow (triggered by `s` key):
1. Local: terminate mutagen syncs (`aiman-sync-<ID>`, `aiman-sync-<ID>-pull`, `s.TmuxSession`, `s.MutagenSyncID`)
2. Optionally: call `flowManager.SkillEngine.PrepareSession` to generate agent command
3. `mgr.ResetControlSocket()` — clear stale SSH master
4. **1 SSH call**: `tmux kill-session … && tmux new-session … "bash -l -c '<agent>; exec bash'"`
5. Re-establish mutagen sync

**The worktree is never touched.** Only the tmux process + agent is replaced.

State captured at goroutine dispatch time (before the goroutine starts) to avoid data races:
- `s` (session copy)
- `workingDir` (from `s.WorkingDirectory`)  
- `remote` (from config)
- `sessionCfg` (copy of `m.sessionCfg`)
- `db` (`m.db`)
- `flowManager` (`m.flowManager`)

---

## Session Creation Flow

**File:** `internal/usecase/flow_manager.go` — `CreateSession`

The TUI wizard (`n` key) drives the user through:

1. JIRA issue picker → `m.sessionCfg.IssueKey` / `m.sessionCfg.Issue`
2. Branch name editor
3. Repo picker (via `gh repo list`)
4. Directory picker (remote subdirectory of repo)
5. Agent picker (scans remote for installed agents)
6. Summary + AWS override screen
7. → `flowManager.CreateSession(ctx, sessionCfg)` in a goroutine

`CreateSession` does:
- git clone/fetch on remote
- `git worktree add`
- trust the working directory for the agent
- write `.aiman_task.md` (JIRA description + prior snapshot context)
- `tmux new-session` with agent command
- `mutagen sync create`
- push AWS STS credentials

Status messages flow back to the TUI via `m.sendStatus()` → `statusMsg` → rendered in `viewStateLoading`.

---

## Archive / Snapshot System

**Key files:**
- `internal/usecase/snapshot.go` — `SnapshotManager`: capture pane, clean, compress (gzip), call AI
- `internal/pane/clean.go` — `pane.Clean`: strips ANSI, collapses noise (progress bars, timestamps, package manager spam), preserves conversation
- `internal/domain/snapshot.go` — `SessionSnapshot` type; `PaneContent` is `[]byte` (gzip-compressed cleaned pane)
- `internal/infra/sqlite/repository.go` — `SaveSnapshot`, `ListAllSnapshots`, etc.

Decompress pane content with `usecase.DecompressPaneContent(s.PaneContent)`.

---

## Key TUI Patterns

**File:** `internal/ui/dashboard.go`

The entire UI is one large `Model` struct with a `viewState` enum. Key patterns:

- **Progress/steps checklist**: `archiveStep` struct + `archiveSteps []archiveStep` — rendered in `viewStateArchiveProgress` (~line 2512). Use this pattern if adding step-by-step progress UI elsewhere.
- **Status messages**: `m.sendStatus(msg string)` → queues `statusMsg` → displayed in `viewStateLoading`
- **Session create/restart completion**: `sessionCreateMsg{err, session}` returned from goroutine
- **Discovery**: `discoveryResultMsg` fired every N seconds with all discovered sessions
- **Debug console**: toggled with backtick; shows `m.debugLines []string`
- **Mutagen sync progress**: `mutagenProgressMsg` used during `Ctrl+Y` recreate

---

## Configuration

**File:** `internal/infra/config/` — reads `~/.aiman/config.yaml`

Key config fields:
- `integrations.jira` — URL, email, api_token
- `remotes[]` — name, host, user, root, aws_delegation
- `active_remote` — which remote is currently selected
- `git` — include_personal, include_orgs, include_patterns, exclude_patterns
- `agents[]` — manually listed agents (supplemented by remote scan)
- `skills.repo` — git URL for skills repository

`config.DirName = ".aiman"` — used to construct all local paths.

---

## Known Gotchas

1. **Bubble Tea versioning**: The project uses `bubbletea` v1. Do NOT introduce any library that pulls in v2 — the interfaces are incompatible and will cause compile errors. `bubbles` (v1) is fine. `bubbleterm` requires v2 — do not use it.

2. **SSH backgrounding**: Never use `ssh -f` from Go's `os/exec` — the child gets `SIGHUP`/`SIGKILL` when the parent handles signals. Use `ControlMaster=auto` + `ControlPersist` exclusively.

3. **Unix socket path length**: macOS limits Unix socket paths to ~104 chars. Keep paths in `~/.aiman/sockets/` short; `manager.go` hashes `user@host` to 16 hex chars to stay under the limit.

4. **`COALESCE` in Save**: Do not remove the `NULLIF(excluded.field, '')` guards from `repository.go` — the discovery cycle runs every few seconds and will silently blank out session fields if those guards are removed.

5. **`isRetriableSSHTransportError` includes "permission denied"**: This is intentional (handles intermittent SSH auth glitches) but means auth failures retry 4× at 30s each. Do not change without testing.

6. **mutagen sync naming**: The canonical sync name is `aiman-sync-<session-ID>`. A pull-only transient sync uses `aiman-sync-<session-ID>-pull`. Older sessions may have `MutagenSyncID` set to something else — always terminate `s.MutagenSyncID` in addition to the computed name.

7. **Pane content compression**: `SessionSnapshot.PaneContent` is gzip bytes, not raw text. Always decompress before displaying or passing to AI.

---

## Remaining TODOs (from PLAN.md)

- **Remote VM Bootstrapper**: Connect to a new VM and install baseline tooling (git, tmux, go, node, claude, cursor, gemini, opencode, acli), configure SSH keys, and authenticate agents.
- **AI Compute Monitoring**: Provider subscription/usage monitoring (Anthropic, Google, OpenAI) — credit balances and usage tracking.
- **EC2 Provisioning**: Spin up/terminate EC2 instances and wire to Aiman's remote registry.
- **MOSH Support**: Hand off to MOSH for high-latency interactive connections.
- **Agentic Patterns**: Robust orchestrator-worker-validator patterns; translate for each supported coding tool.

---

## Recent History (Bug Fixes — v0.6.43 → v0.6.57)

The session restart feature went through extensive debugging. Summary of root causes found and fixed:

| Version | Fix |
|---|---|
| v0.6.43 | Added 5-min outer timeout; removed dead code; fixed `%%` escaping |
| v0.6.44 | Added GetSyncStatus, SetProgramMsg, SSH timeouts, CI build fixes |
| v0.6.45 | Skip initial pull on restart to avoid sync hang |
| v0.6.46 | Handle 'Failed to connect to new control master' SSH error |
| v0.6.47 | Suppress SSH tickers during restart; add pane-capture timeout |
| v0.6.48 | Fix STS token push without assume-role |
| v0.6.49–50 | Fix Ctrl+Y recreate sync: skip wipe+pull, wait for Watching state |
| v0.6.51 | Show mutagen sync status/percentage during Ctrl+Y |
| v0.6.52 | Fix waitForSyncWatching state match |
| v0.6.53 | Per-call SSH timeouts + ServerAlive to unblock hangs |
| v0.6.54 | Reset SSH ControlMaster after tmux session stop |
| v0.6.55 | Atomic tmux restart; fix `\|\| true` masking errors |
| v0.6.56 | **Simplify restartSession to 1 SSH call**; fix data race |
| v0.6.57 | **Fix DB COALESCE** — prevent discovery from overwriting known-good session fields |
