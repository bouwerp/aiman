# AGENTS.md — Technical Context for AI Agents

This file documents the architecture, key files, known gotchas, and current state of **Aiman** so that any AI coding agent can continue development without needing prior context.

---

## What Is Aiman?

Aiman is a **terminal UI (TUI) orchestrator** written in Go. It manages the full lifecycle of remote AI coding sessions:

- Turns a JIRA ticket into a git worktree + tmux session + mutagen sync + AI agent in one flow
- Tracks active sessions, provides live pane previews, git status, and AI summaries
- Supports Claude Code, Antigravity CLI, GitHub Copilot, OpenCode, and Cursor as agents

Binary: `aiman` — built from `./cmd/aiman/main.go`  
Module: `github.com/bouwerp/aiman`  
Go: 1.26  
Current release: **v0.14.0**

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
  agenthook/        — native-session hooks (install, payload, resume)
  aimanskill/       — bundled skill text + EnsureOnHost
  domain/           — pure Go domain types and interfaces (no external deps)
    session.go      — Session, SessionConfig, SessionStatus, GitStatus, PullRequest, Secret
    interfaces.go   — all domain interfaces (RemoteExecutor, SyncEngine, etc.)
    snapshot.go     — SessionSnapshot type
    context.go      — ContextStore, ContextEntry (shared notes)
    agent.go        — Agent type
    skills.go       — Skill type
    aws.go          — AWSConfig type
  contextstore/
    files.go        — markdown+YAML store under ~/.aiman/context/
  usecase/
    flow_manager.go — CreateSession (the main session-creation orchestrator)
    session_discoverer.go — discovers sessions from live remote (tmux/git/mutagen)
    snapshot.go     — SnapshotManager (archive, compress, AI summarise)
    doctor.go       — startup health checks
  infra/
    ssh/manager.go  — SSH ControlMaster multiplexer (Execute, ExecuteWithTimeout, WriteFile, ResetControlSocket)
    remotesvc/      — systemd --user unit + nohup scripts for remote serve/trigger
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
| `AgentSessionID` | vendor conversation id for resume (`claude --resume`, `grok --resume`, …) |
| `AgentSessionPath` | optional vendor transcript / session file |
| `AgentTitle` | vendor session title when a hook reports one |
| `AgentEnded` | SessionEnd: the agent finished (not a crashed pane) |
| `HookState` | last hook-reported idle/working/waiting_input, with source/message/seq |
| `Status` | `PROVISIONING → ACTIVE → CLEANUP` (or `ERROR`/`INACTIVE`) |
| `Tunnels` | local↔remote port forwards |
| `AWSProfileName` | legacy scoped AWS profile name retained only for migration/cleanup |

---

## SQLite Persistence — Critical Design Decision

**File:** `internal/infra/sqlite/repository.go`

The `Save` method uses `INSERT … ON CONFLICT(id) DO UPDATE SET`. As of **v0.6.57**, all critical session fields use:

```sql
field = COALESCE(NULLIF(excluded.field, ''), sessions.field)
```

**Why this matters:** The session discoverer (`Discover`) builds sessions from live remote state — it can only know what it can read from a running tmux process. Fields like `Branch`, `AgentName`, `WorktreePath`, and `WorkingDirectory` will often be empty in discovered sessions. Before this fix, discovery was silently overwriting the DB with empty strings on every scan cycle, causing restart to fail with "session has no working directory".

**Fields protected by COALESCE:** `name`, `group_name`, `issue_key`, `branch`, `repo_name`, `remote_host`, `worktree_path`, `working_directory`, `tmux_session`, `mutagen_sync_id`, `local_path`, `agent_name`, `agent_session_id`, `agent_session_path`, `agent_title`, `agent_ended`, `hook_state`, `hook_state_message`, `hook_state_source`.

**Fields always overwritten:** `status`, `updated_at`.

**Fields with null-safe COALESCE (not empty-string):** `tunnels_json`, `aws_profile`, `aws_config_json`.

---

## Session Discovery

**File:** `internal/usecase/session_discoverer.go`

`Discover(ctx, host)` scans the remote for running tmux sessions, correlates them with mutagen syncs, and returns `[]domain.Session`. It can only populate fields it can read live:

- `TmuxSession`, `WorkingDirectory` (from `AIMAN_ID` env var in tmux pane)
- `MutagenSyncID` (from `mutagen sync list`)
- Basic git branch info if the CWD is a git repo

It does **not** read `Name`, `Group`, `Branch`, `AgentName`, `IssueKey`, `WorktreePath`, or `LocalPath` — those come from the DB.

`applyDiscoveryResult` overlays those DB fields onto live sessions (`overlayPersistedSessionFields`) so a scan cannot flatten names and groups in the TUI. Save still uses COALESCE so empty discovery writes do not wipe the DB.

The `discoveryResultMsg` handler in `dashboard.go` saves discovered sessions to DB after each discovery cycle. Empty discovered fields are safe because of the COALESCE fix.

Startup also prunes DB sessions whose explicit `RemoteHost` no longer matches any configured remote. This prevents deleted/retired remotes from resurrecting sticky orphaned sessions on restart.

---

## SSH Manager

**File:** `internal/infra/ssh/manager.go`

Key behaviours:
- Uses `ControlMaster=auto` + `ControlPersist=10m` — all calls to the same host share one socket
- Socket path: `~/.aiman/sockets/ssh-<sha1(user@host)[:16]>.sock`
- Per-call timeout: **30 seconds** (`Execute`). `ExecuteWithTimeout` is for work that exceeds that (remote install/update uses `remotesvc.OpTimeout`, 3 minutes).
- On transport errors: clears socket, retries up to 2×, then falls back to `ControlMaster=no`
- `ResetControlSocket()`: sends `ssh -O exit` to gracefully stop the master, then removes the socket file. Call this before disruptive remote operations (e.g. tmux kill-session) to ensure a clean connection for the next call.

**Do not** add "permission denied" removal from `isRetriableSSHTransportError` without testing — it causes 4× retries on auth failures which wastes 120s.

---

## Remote serve / trigger as a service

**File:** `internal/infra/remotesvc/remotesvc.go`

`aiman serve` and `aiman-trigger` run on remotes as systemd `--user` units (`aiman-serve.service`, `aiman-trigger.service`) with `loginctl enable-linger`. They are independent of the laptop TUI and of tmux. If user systemd is unavailable, fallback is `nohup` plus `~/.aiman/{serve,trigger}.pid`.

Admin Menu (`m`) → **Agent API** is the settings page for `aiman serve` (one row per remote). The Daemons tab (Tab) also lists **agent API** and **trigger**. The skill talks to the agent API only. Start it with `i` on that settings page.

| Key | Action |
|---|---|
| `i` | Install/enable (linger first, then the unit) |
| `s` | Start or restart |
| `c` | Reload (restart; serve re-reads remote `~/.aiman/config.yaml`) |
| `u` | Update binary from GitHub, then restart |
| `ctrl+k` | Stop |
| `r` | Probe driver/status/version/socket/logs |

Session create also calls `ensureRemoteServer` if `~/.aiman/aiman.sock` is missing.

On start, `aiman serve` installs or updates the bundled skill (`internal/aimanskill`) in user-level agent skill dirs under `$HOME` and in each known session worktree, and registers native-session hooks (`internal/agenthook`) in each installed agent's config. Identity agents (Claude, Grok, Cursor, Codex, Copilot, agy) report session id, `SessionEnd`, and `idle_prompt`. OpenCode and Pi also report lifecycle state. `session.wait` uses a hook report newer than 2 minutes instead of `pane.Classify`. Session create still writes the worktree skill copy. Ageni is not hooked.

SSH `systemctl --user` needs `XDG_RUNTIME_DIR` and the session bus; the scripts set those. Linger is enabled *before* `systemctl --user` so a first SSH to a host with no lingering user instance can still install.

Do not run serve inside tmux. Probe on select, `r`, after an op, and after discovery — not on the tmux pane ticker.

---

## Session Restart

**File:** `internal/ui/dashboard.go` — `restartSession()` (~line 4885)

The restart flow (triggered by `s` / `ctrl+r`, including switching agent):
1. Agent picker (same or different agent)
2. Ask the current agent to write `.aiman_session_summary.md`, then Ctrl+C
3. `PrepareSession` for the chosen agent (configured `--model` / effort flags, summary prompt)
4. **1 SSH call**: `tmux kill-session … && tmux new-session … "bash -l -c '<agent>; exec bash'"`
5. send-keys the handoff prompt

**The worktree is never touched.** Only the tmux process + agent is replaced.

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

1. Run-target picker (`viewStateRunTargetPicker`) → a configured remote server, or `[e]` for
   an EC2 autonomous loop. Shown for any remote count, including zero, because the EC2 loop
   launches its own instance and needs no remote.
2. Mode picker (remote targets only) → JIRA issue / new branch / existing branch / ad-hoc /
   autonomous trigger
3. JIRA issue picker → `m.sessionCfg.IssueKey` / `m.sessionCfg.Issue`. Only issues assigned
   to the current user in `integrations.jira.issue_statuses` (default:
   `jira.DefaultIssueStatuses`) are listed, and typed searches are scoped the same way.
4. Branch name editor
5. Repo picker (via `gh repo list`)
6. Directory picker (remote subdirectory of repo)
7. Agent picker (scans the remote for installed agents; the EC2 path has no host to scan
   yet, so it offers `agent.KnownAgents()` instead)
8. Summary + AWS override screen
9. → `flowManager.CreateSession(ctx, sessionCfg)` in a goroutine, or
   `createEC2LoopSession()` for an EC2 loop

`CreateSession` does:
- git clone/fetch on remote
- `git worktree add`
- places worktrees in repo-scoped sibling directories named `<repo>@<branch>` to avoid cross-repo path collisions
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
- `agent_defaults` — per-binary `model` and `effort` applied at session launch
- `aws.include_profiles` — local `~/.aws` profiles aiman may use (omit = all)
- `skills.repo` — git URL for skills repository

`config.DirName = ".aiman"` — used to construct all local paths.

---

## Known Gotchas

1. **Bubble Tea versioning**: The project is fully on `bubbletea` v2 (`charm.land/bubbletea/v2`) with `bubbles/v2` and `lipgloss/v2`. Do NOT reintroduce v1 (`github.com/charmbracelet/bubbletea`, `/bubbles`, `/lipgloss`) — the type systems are incompatible and cannot coexist in one model graph. Key facts: key events are `tea.KeyPressMsg` (match via `.String()`; the space key renders as `"space"`, not `" "`); the alt screen and mouse mode come from flags on the returned `tea.View` (see `newView` in `internal/ui/views.go`), not program options; sub-models render strings via `viewString()` while only models handed to `tea.NewProgram` implement `View() tea.View`. Tests must build key events through `pressKey`/`pressRune` in `internal/ui/keystrokes_test.go` — never hand-construct key literals.

2. **SSH backgrounding**: Never use `ssh -f` from Go's `os/exec` — the child gets `SIGHUP`/`SIGKILL` when the parent handles signals. Use `ControlMaster=auto` + `ControlPersist` exclusively.

3. **Unix socket path length**: macOS limits Unix socket paths to ~104 chars. Keep paths in `~/.aiman/sockets/` short; `manager.go` hashes `user@host` to 16 hex chars to stay under the limit.

4. **`COALESCE` in Save**: Do not remove the `NULLIF(excluded.field, '')` guards from `repository.go` — the discovery cycle runs every few seconds and will silently blank out session fields if those guards are removed.

5. **`isRetriableSSHTransportError` includes "permission denied"**: This is intentional (handles intermittent SSH auth glitches) but means auth failures retry 4× at 30s each. Do not change without testing.

6. **mutagen sync naming**: The canonical sync name is `aiman-sync-<session-ID>`. A pull-only transient sync uses `aiman-sync-<session-ID>-pull`. Older sessions may have `MutagenSyncID` set to something else — always terminate `s.MutagenSyncID` in addition to the computed name.

7. **Pane content compression**: `SessionSnapshot.PaneContent` is gzip bytes, not raw text. Always decompress before displaying or passing to AI.

8. **AWS credential expiry is read from the remote file, not STS**: every push records `x_security_token_expires` (`awsdelegation.ExpiryKey`) in the remote `~/.aws/credentials`, and `ReadCredentialExpirations` reads it back in one round trip. Profiles pushed before that existed have no key, so `RemoteCredentialExpiry.For` approximates from the file mtime plus `duration_seconds` and flags it as approximate — never present an approximate value as exact. All mint-and-push flows must go through `pushFreshCredentials` so the expiry keeps being recorded.

9. **AWS credential messages are routed globally, not by view state**: `awsCredLoadedMsg`, `awsCredCheckResultMsg`, `awsCredRenewResultMsg`, `awsCredBatchRenewResultMsg`, `awsCredRemoveResultMsg` and `awsCredRenameResultMsg` are handled in `Update` *before* the `switch m.state` dispatch and forwarded via `routeAWSCredentialsMsg`. Renewals and probes are bubbletea commands that keep running after the user leaves the page; routing them by view state drops the results (the remote gets new credentials but the model never learns and never re-verifies). Do not move them into `handleAWSCredentialsUpdate`. For the same reason, the 30s repaint tick (`awsCredTickMsg`) is owned by the dashboard, not by `AWSCredentialsModel.Init` — per-visit ticks would stack one chain per page visit. `enterAWSCredentials` must keep a `Busy()` model rather than rebuilding it.

10. **AWS_PROFILE is sanitised, not trusted**: `usecase.SharedSessionAWSEnv` exports a profile only if it survives `SanitizeSessionAWSProfile` — legacy `aiman-<id>` names and names missing from `~/.aws` are dropped so the session falls back to the default credential chain rather than pointing at a profile that cannot resolve. Do not bypass it by setting `AWS_PROFILE` directly in tmux env code.

---

## Remaining TODOs (from PLAN.md)

- **Remote VM Bootstrapper**: Connect to a new VM and install baseline tooling (git, tmux, go, node, claude, cursor, agy, opencode, acli), configure SSH keys, and authenticate agents.
- **AI Compute Monitoring**: Provider subscription/usage monitoring (Anthropic, Google, OpenAI) — credit balances and usage tracking.
- **EC2 Provisioning**: Spin up/terminate EC2 instances and wire to Aiman's remote registry.
- **MOSH Support**: Hand off to MOSH for high-latency interactive connections.
- **Agentic Patterns**: Robust orchestrator-worker-validator patterns; translate for each supported coding tool.

---

## Recent History (Bug Fixes)

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
| v0.7.36 | **Restore shared AWS credential sync** — sync directly manages `~/.aws/{credentials,config}` again |
| v0.7.37 | **Namespace worktree paths** — use `<repo>@<branch>` to prevent cross-repo worktree collisions |
| v0.7.38 | **Prune stale removed-remote sessions** — explicit `RemoteHost` entries no longer fall back and stale DB rows are deleted on load |
| v0.8.11 | **Remove legacy aiman-* AWS profiles** — fully migrated to direct global profiles; removed legacy session-scoped profile cleanups and schema fields |
| v0.8.22 | **AWS credential expiry visibility** — credentials manager gained an "Expires in" column (live countdown, `~` for values estimated from file age), `shift+R` refreshes every delegated profile regardless of status from both the credentials screen and the dashboard, and the dashboard warns when anything is within `awsCredExpiryWarnWindow` (1h) of expiry |
| v0.8.22 | **Credential work survives leaving the page** — `awsCred*` results are routed to `m.awsCredentials` globally instead of via the view-state dispatch (they were silently dropped off-page, so a refresh landed on the remote but never verified), re-entry keeps a busy model instead of rebuilding it, the page reports what is still in flight, and a wave finishing off-page raises a toast |
| v0.8.22 | **Purge leftover aiman-* AWS profile names** — v0.8.11 removed the generators but not the values already stored, so old sessions kept exporting `AWS_PROFILE=aiman-<id>` for a profile that no longer exists in `~/.aws`. Opening the DB now clears them (`sessions.aws_profile` and `aws_config_json.SourceProfile`), `SharedSessionAWSEnv` drops legacy and unknown profile names instead of exporting them, `aiman clear-aws-profiles` reports the cleanup, and the credentials screen no longer hides `aiman-` profiles |
