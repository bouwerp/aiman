# AGENTS.md — Technical Context for AI Agents

This file documents the architecture, key files, known gotchas, and current state of **Aiman** so that any AI coding agent can continue development without needing prior context.

---

## What Is Aiman?

Aiman is a **terminal UI (TUI) orchestrator** written in Go. It manages the full lifecycle of remote AI coding sessions:

- Turns a JIRA ticket into a git worktree + terminal session + mutagen sync + AI agent in one flow
- Tracks active sessions, provides live pane previews, git status, and AI summaries
- Supports Claude Code, Antigravity CLI, GitHub Copilot, Kilo Code, Cursor, Pi, Muse Code, and Deep Code as agents

Binary: `aiman` — built from `./cmd/aiman/main.go`  
Module: `github.com/bouwerp/aiman`  
Go: 1.26  
Current release: **v0.14.0**

---

## Build & Test

```bash
make ci          # fmt + vet + test + lint (full CI: run before every commit)
make eval        # deterministic harness evals (fake remote, temp DB and socket)
make build       # go build -o aiman ./cmd/aiman
go build ./...   # quick build check
go test ./...    # tests (skip -race if Go stdlib version mismatch)
```

`make eval` runs the cases under `evals/` against an in-process `aiman serve`,
a temp SQLite database and a temp Unix socket, with a fake remote and fake
creator. It needs no SSH host, agent binary or network, and removes its temp
directory. It covers lifecycle, steering, to-dos, handover, usage and the
verify pass/deny/reproduce paths. It is not part of `make ci`; CI runs it as a
separate five-minute job. Only the opt-in `-tags evalreal` track spends money,
and that track is nightly, cost-capped and never part of CI.

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
  ptyhold/          — durable PTY holder process and its on-disk contract
  ptyruntime/       — serve's client over ptyhold (the terminal runtime)
  todo/             — per-session to-do lists: JSON store, agent-snapshot mirror, `.aiman_todo.md` render
  usecase/
    flow_manager.go — CreateSession (the main session-creation orchestrator)
    session_discoverer.go — discovers sessions from live remote (PTY/git/mutagen)
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
    clean.go        — strips ANSI/noise from pane captures
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
| `BaseBranch` | ref the worktree branch was cut from; basis for diffs against the base |
| `RepoName` | `owner/repo` GitHub slug |
| `RemoteHost` | SSH host (from config) |
| `WorktreePath` | absolute path to git worktree on the remote |
| `WorkingDirectory` | active sub-directory inside worktree (what the agent cwd is) |
| `TerminalName` | human-readable terminal name (the PTY is addressed by `ID`); sqlite column `tmux_session`, JSON key `TmuxSession` |
| `Backend` | `pty` for every session aiman runs, `tmux` for a legacy row, empty when no terminal is recorded |
| `MutagenSyncID` | mutagen sync session name (usually `aiman-sync-<ID>`) |
| `LocalPath` | `~/.aiman/work/<ID>` — local mutagen sync root |
| `AgentName` | agent binary name, e.g. `claude` |
| `AgentModel` | model shown in the TUI: the launch model, else the one detected at create; never passed to a launch |
| `LaunchModel` / `LaunchEffort` | per-launch model and effort from an `agents.smart` or `agents.preferred` entry (or an explicit `--model`/`--effort`); same-agent restart and revive reuse them, an agent switch clears them |
| `Tier` | model tier the session was created from: `smart` or `worker` after fallback; cleared with the launch model on an agent switch |
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

**Why this matters:** The session discoverer (`Discover`) builds sessions from live remote state — it can only know what the PTY runtime and mutagen report. Fields like `Branch`, `AgentName`, `WorktreePath`, and `WorkingDirectory` will often be empty in discovered sessions. Before this fix, discovery was silently overwriting the DB with empty strings on every scan cycle, causing restart to fail with "session has no working directory".

**Fields protected by COALESCE:** `name`, `group_name`, `issue_key`, `branch`, `base_branch`, `repo_name`, `remote_host`, `worktree_path`, `working_directory`, `tmux_session`, `backend`, `mutagen_sync_id`, `local_path`, `agent_name`, `agent_model`, `launch_model`, `launch_effort`, `agent_session_id`, `agent_session_path`, `agent_title`, `agent_ended`, `hook_state`, `hook_state_message`, `hook_state_source`, `watch_pr`, `role`, `issue_type`.

**Fields always overwritten:** `status`, `updated_at`.

**Fields with null-safe COALESCE (not empty-string):** `tunnels_json`, `aws_profile`, `aws_config_json`.

The `tmux_session` column keeps its name and holds `TerminalName`. `backend` is `pty`, `tmux` (a legacy row, `domain.BackendLegacyTmux`) or empty (no terminal recorded). Opening the DB sets a NULL `backend`, a row written before the column existed, to `tmux`; an empty value stays empty.

---

## Session Discovery

**File:** `internal/usecase/session_discoverer.go`

`Discover(ctx, host)` asks the remote serve for its PTY sessions (`ScanPTYSessions`, `aiman pty list` over SSH), adds worktrees with no live session and mutagen syncs with no session, overlays hook reports, and returns `[]domain.Session`. It never asks tmux. `ScanPTYSessions` returns an empty list for a remote with no aiman binary and `ErrServeDown` when serve does not answer. A failed scan, `ErrServeDown` included, fails the whole call, because the merge step reads the result as every session on the host and would otherwise mark known sessions dead.

It can only populate fields it can read live:

- `ID`, `TerminalName`, `WorkingDirectory`, `Backend` (`pty`) from the PTY record
- `MutagenSyncID` and `LocalPath` from the matching mutagen sync (`mutagen sync list`), which also sets status `SYNCING`

It does **not** read `Name`, `Group`, `Branch`, `AgentName`, `IssueKey` or `WorktreePath`; those come from the DB.

Sessions merge by ID. Discovery keeps every live PTY session with an ID of its own even when two share a terminal name (children of one parent can). The dashboard merge (`lookupPersistedSession`) matches a stored row by ID first and falls back to `sessionTerminalKey` (host plus terminal name) only for rows without one. Preview captures, activity hints and git status are keyed by session ID. A selected session missing from one scan keeps its row for up to two scans.

A legacy tmux row is never discovered. It stays listed as inactive, in memory only, until the user stops it and restarts it into pty.

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
- `ResetControlSocket()`: sends `ssh -O exit` to gracefully stop the master, then removes the socket file. Call this before disruptive remote operations (e.g. stopping a session) to ensure a clean connection for the next call.

**Do not** add "permission denied" removal from `isRetriableSSHTransportError` without testing — it causes 4× retries on auth failures which wastes 120s.

---

## Remote serve / trigger as a service

**File:** `internal/infra/remotesvc/remotesvc.go`

`aiman serve` and `aiman-trigger` run on remotes as systemd `--user` units (`aiman-serve.service`, `aiman-trigger.service`) with `loginctl enable-linger`. They are independent of the laptop TUI. If user systemd is unavailable, fallback is `nohup` plus `~/.aiman/{serve,trigger}.pid`.

Admin Menu (`m`) → **Agent API** is the settings page for `aiman serve` (one row per remote). The Daemons tab (Tab) also lists **agent API** and **trigger**. The skill talks to the agent API only. Start it with `i` on that settings page.

| Key | Action |
|---|---|
| `i` | Install/enable (linger first, then the unit) |
| `s` | Start or restart |
| `c` | Reload (restart; serve re-reads remote `~/.aiman/config.yaml`) |
| `u` | Update binary from GitHub, then restart |
| `ctrl+k` | Stop |
| `t` | Install RTK and register its agent hooks (`internal/infra/rtk`) |
| `r` | Probe driver/status/version/socket/logs |

`t` runs `rtk.InstallScript` under `remotesvc.OpTimeout` and reports an `rtkInstallMsg`, routed in `Update` next to `serviceOpMsg`; the model then re-probes serve. The serve probe prints `RTK=<version>` into `Daemon.RTKVersion`, which the page shows as `RTK: <version>` or `RTK: not installed (t to install)`. Hooks are registered only for `rtk.HookTargets` (Claude, Codex, Cursor, Gemini, Copilot, Pi). The `$` usage view runs `rtk.FetchGain` per host and appends `rtkGainLine` to `usageHost.extra`. Never run plain `rtk gain`: without `--format json` it can open a live dashboard. Never export `RTK_VERSION` into the install script's environment; RTK's `install.sh` reads it as a version pin.

Session create also calls `ensureRemoteServer` if `~/.aiman/aiman.sock` is missing.

On start, `aiman serve` installs or updates the bundled skill (`internal/aimanskill`) in user-level agent skill dirs under `$HOME` and in each known session worktree, and registers native-session hooks (`internal/agenthook`) in each installed agent's config. Identity agents (Claude, Grok, Cursor, Codex, Copilot, agy, Muse Code) report session id, `SessionEnd`, and `idle_prompt` where the runtime has it. Muse Code uses `SessionStart`, `Stop`, and `UserPromptSubmit` in `~/.config/muse/settings.json` (`schema_version` 1). Kilo Code and Pi also report lifecycle state. Pi (pi.dev, `@earendil-works/pi-coding-agent` 1.4) gets `~/.pi/agent/extensions/aiman-native-session.ts`: `session_start` is idle with the session id and file, `agent_start` is working, `agent_settled` (not `agent_end`, which retries and queued work follow) is idle, `ui_prompt_start` is waiting_input, and `session_shutdown` with reason `quit` is the end. Plugin `seq` is `Date.now()`-based, because serve drops a report whose seq is below the last one applied. Pi resume is `pi --session <id>`; session files are `~/.pi/agent/sessions/--<cwd>--/<ts>_<id>.jsonl`. Pre-launch trust also writes the worktree into `~/.pi/agent/trust.json` (never flipping a `false`). Pi's model flag takes `provider/id:<thinking>`; `--thinking` (off…max) overrides that suffix, so aiman skips `--thinking` when the model carries a level. Deep Code has no hook API: `deepcode-launch.sh` runs `aiman session deepcode-watch` on `~/.deepcode/projects/<code>/sessions-index.json`, then the CLI. Resume is `deepcode --resume <uuid>`. Model and effort are `DEEPCODE_MODEL` and `DEEPCODE_REASONING_EFFORT`. Permissions stay the user's `defaultMode`. `session.wait` uses a hook report newer than 2 minutes instead of `pane.Classify`. Session create still writes the worktree skill copy. Ageni is not hooked.

`session.create` parameters include `tier`, `model` and `effort`: `tier` is exclusive with `agent`, and `model`/`effort` require an explicit `agent`. The result is `{"type":"session","session":{…,"tier":…},"requested_tier":…,"tier_fallback":…}`; `SessionInfo.Tier` carries the tier after fallback and `sessionFromInfo`/the remote-create result copy it.

`session.register` copies laptop sessions into serve's database. Identity fields come from the payload. When a row already exists, hook state, `AgentEnded`, `AgentTitle`, and `Status` stay on that row. A payload naming a different agent with no launch model clears the row's `launch_model` and `launch_effort` (`ClearLaunchModel`). `LocalPath`, `MutagenSyncID`, `Tunnels`, and `AWSConfig` are laptop-only and are dropped. Empty ids and ids with the `pending-` prefix are skipped.

To-do lists are `internal/todo`, stored as `~/.aiman/todos/<id>.json`. `EnableTodos` restores the steering queue from sqlite `steer_notes`, then turns writes on. Handlers refuse to-do writes while the store is unset. `MutateTodos` applies the change under a per-session lock, renders `.aiman_todo.md` in the session working directory when one is set, publishes `todo_changed`, and queues a steering note unless the agent made the change. `todo.mirror` folds an agent task-list snapshot into the same list. A session writing its own list, and every `todo.mirror`, is the agent. Other callers are the user. `steer.send` may use author `aiman`. A session cannot steer itself. The store keeps at most one item active.

Steering queues notes for the agent: free text, or the formatted summary of someone else's list change. Notes the agent authored are dropped, as are changes that only touch `Ref` links. A window of 800ms collapses what arrived into at most two persisted notes (one for list ops, one for joined text). The queue is in sqlite, so a serve restart neither loses nor repeats undelivered notes. Delivery waits until the agent is at a prompt. `AgentEnded` holds the note (`ended`). A hook report of `idle`, or `waiting_input` from an idle prompt, opens the gate only when `HookStateAt` is after the last delivery. Other `waiting_input` is `dialog`. Anything else reported by the hook is `working`. With no usable hook report, only a high-confidence idle pane classification opens the gate, and only more than 3 seconds after the last delivery. A hook report calls `Kick` so a new state can release a held note. `session.forget` drops that session's steering queue. It does not delete `~/.aiman/todos/<id>.json`.

`handover.write` writes `.aiman_handover.md` in the session directory and stores the shared-context note `handover-<id>`. The reason is `archive`, `forget`, `restart`, `agent_switch`, or `pre_compact`. The file has a fixed skeleton: Goal, Constraints & Preferences, Progress, Key Decisions, Next Steps, Critical Context, Evidence, Verification. `handover.MissingSections` checks it; the content is derived from the active to-do, hook title/message, undelivered notes, diffstat and pane tail, and error/test lines stay verbatim. The file is written only when its bytes change. `handover.point` takes an optional reason and queues `HandoverRestartNote` or, for `pre_compact`, `HandoverCompactionNote`; it queues only when the file is already there. `PreCompact` writes the handover and queues the compaction pointer. A revive calls `handover.point`, so the note waits on the delivery gate instead of being typed while the agent is booting.

Each `handover.write` also starts a handover child (`internal/handoverchild`) when `handover.child` is on, the default. The mechanical write always happens first and stays whenever the child fails. Hidden roles (reviewer, handover) get no handover: `writeHandover` returns before writing. The child is a hidden ad-hoc session with role `handover` in the scratch directory `~/.aiman/handovers/<run-id>/`, a fresh repository. Run records are `~/.aiman/handover-runs/<run-id>.json`. The directory holds the inputs `brief.md` and `mechanical.md`; the child writes `handover.md`, `memories.json`, and `done` last. The brief names the transcript (`AgentSessionPath` at start), the worker worktree (read-only, may be gone), the reason, the eight headings and the JSON schema. It forbids code edits, commits, pushes, to-do changes, steering and child sessions. The child launches prompt-free, so that rule is not enforced; the scratch cwd is the only mitigation. The agent is the first installed entry of `HandoverCandidates`. A worker has at most one run. A trigger during a starting or running run still writes the mechanical file, records its reason on the run, replaces the run's fallback render (mechanical text, sha, label, abstract), and starts nothing; the response's `child` is that run's id. When a new render's sha256 matches the run that last applied a child handover and the file's sha256 equals the file that run wrote (`LastApplied`), serve keeps the file, returns `written:false`, and starts nothing. While a run is active, `queueHandoverPointer` holds the pointer on the run (a later one replaces it), and the run queues it when it ends. A run ends `complete` when `done` exists, `incomplete` when the child ends or its terminal dies first, `timed_out` after `handover.timeout`, and `failed` when no candidate agent is installed or the launch fails. Only a complete run contributes outputs. A `handover.md` that fails `MissingSections` or exceeds 64 KiB is rejected, but valid candidates still count; bad `memories.json`, or one over 256 KiB, gives none. Both files are read through a size cap. At the end the run writes the child's handover to the worker directory when it exists, stores note `handover-<id>` (child text, else the mechanical body), and enqueues the memory job with the run id as its id; `memoryjob.Queue.Enqueue` ignores an id it already holds, so a retried finish queues it once. A gone worker gets no file and no steer; the note and job still go out, the note in the namespace from the group, issue key and repo the run captured at start (`handover.NoteNamespace`, shared with serve). Only `domain.ErrSessionNotFound` from the registry means gone. Any other lookup error leaves the run active, the pointer held and the child running, and the finish is retried. A finish runs on its own 30s context, so `Close` does not cut it short. An applied restart or agent_switch run queues `HandoverRefreshedNote` unless it released a held pointer instead. The ledger records `handover.child_started` and `handover.child_finished` (run, agent, reason, status, candidates, error). Cleanup stops the child, waits up to 30s for it to exit, forgets it, and removes the scratch directory. `Resume` at serve start watches running runs again, fails starting ones with the mechanical fallback, and releases the pointer and cleans up finished runs left uncleaned. A starting run with no `ChildID` first adopts a child the last serve created but did not record: a `handover`-role child of the worker whose branch or directory is the run's (`ChildLister`, which `serverSessions` implements). Finish and `Resume` delete cleaned-up runs except each session's newest, which `LastApplied` reads.

Memory candidates are durable notes proposed from handovers and queued in serve (`internal/memoryjob`, `memory.jobs`, `memory.done`). A new job replaces the session's unfinished one. A child run enqueues its job at the end with its candidates. Without a child, serve enqueues the mechanical body only when the file changed. Admin Menu → **Memory candidates** fetches jobs from each remote. A job with candidates is shown directly. A job without them goes to Ollama (`ProposeMemories`) only when `ai.enabled`; otherwise the TUI marks it done. `a` accepts (a `context.put` under the job's group, else its repo), `e` edits, `d` drops, and the job is marked done once every candidate is decided.

The event hub (`internal/server/events.go`) fans each `Publish` out to every `session.events` subscriber. Each subscriber buffers 64 events. A full buffer drops the event, so a stalled client cannot block a socket handler. A to-do write publishes `todo_changed` with the list `Rev`. The steerer publishes `steer` with the queued count, and `DeliveredAt` once a note is submitted. The type names `usage`, `notify`, and `verify` are reserved on the same stream.

`session_events` is the durable append-only ledger behind the hub. `Server.appendEvent` writes one row (`session_id, seq, at, actor, kind, payload, hash`) and never fails the request; the sequence is allocated by the INSERT, and `hash` is sha256(payload). It records create, prompt, hook, handover and session-end facts (`session.created` carries `tier`, `requested_tier`, `model`, `effort`, `tier_fallback`), plus `pane.uncertain` (agent, backend, state and a sha256 of the cleaned pane, never its text) once per distinct pane the rules classify at low confidence; to-do, steer, verify and budget events join as their handlers land. The table is never read on the request path and has no COALESCE rules. `session.log` reads it (`since_seq`, `limit`) and `aiman session log` is the CLI. The event hub is still the live stream; the ledger is the history.

serve runs the PR watcher (`internal/ciwatch`) in one goroutine. Every 2 minutes it asks `gh` about each watched session's PR. A failed check becomes the to-do `Fix CI: <name>` (`Ref` `check:<name>`) plus a steering note with the log tail, and a new inline review comment becomes `Address review: …` (`Ref` `review:<id>`). A check that keeps failing past 3 fix attempts raises a `ci_escalation` notification. Autonomous sessions are watched by default; `w` on the sessions tab toggles the watch through `ci.watch {session, on}`. `ci.get {session}` returns `{"type":"ci","ci":{…}}` and only reads serve's state. The TUI reads it on selection, on `todo_changed` for the selected session, and on a `ci_escalation` event. It shows `watching PR #<n>` on the meta line and turns an escalated row into needs-you. `aiman-trigger` never types into a pane: it calls `steer.send` with mode `note` for scheduled prompts. It never answers a dialog in an autonomous session; serve's notification sweep reports the `waiting_input` instead.

Notifications are `internal/notify`. Serve is the only writer of the log (sqlite `notifications`, plus `notify_last` for `min_interval` and `notify_cursors`). The recorder is fed by a 2s sweep of active sessions and by `handleReportAgentSession`; it counts only hook states and `pane.High` classifications, primes each session's first observation without notifying, and enforces `min_interval` inside one transaction (`AppendIfDue`). Each append publishes a `notify` hub event with `Kind`. Other plans emit through `(*Server).NotifyRecorder().Emit`. `notify.since` (`after`, `limit`, `after: -1` for the tail) reads the log. Webhook and Slack sinks run in serve under cursor `serve`; `aiman-gateway` runs the same `notify.Dispatcher` over `notify.since` with only the phone sink and starts at the tail when it has no cursor. The TUI toasts `notify` events only when `domain.NeedsYou` (never `ended`) and does not refresh the pane for them. Config is `notifications` on the laptop with optional `remotes[].notifications`, pushed per host by `MarshalServeConfig(host)`. Never log the webhook URL, Slack token or Expo tokens.

SSH `systemctl --user` needs `XDG_RUNTIME_DIR` and the session bus; the scripts set those. Linger is enabled *before* `systemctl --user` so a first SSH to a host with no lingering user instance can still install.

Probe on select, `r`, after an op, and after discovery, not on the pane ticker.

`aiman serve` runs a usage tracker (`internal/usage`) that scans Claude, Codex and Pi transcripts into per-session hourly model buckets and prices them when they are read. `usage.get` returns `{"type":"usage","session":"<id or empty>","usage":<report>}`: an empty session is every session on that host, and `by` of `issue` (30 days) or `day` (14 days) with `tz_offset_minutes` (the laptop zone offset in minutes) builds the rollup. `aiman usage` is that call from the CLI. `$` on the sessions tab opens the same rollup, last 30 days by ticket, with `tab` for the last 14 days by day. The cost column is `usage.FormatUsageCost` and is `-` when the row has no priced tokens. Unpriced model ids are listed under the table. Each host can show one dim extra line under its name; the usage view leaves that line empty.

serve keeps agent CLIs current through `agentupdate.Manager` (`cmd/aiman/agent_updates.go`), configured by `agents.update` (`mode` off/notify/auto, default auto; `window` default `03:00-06:00` host-local; `check_every` default 6h; `hold`). Unless the mode is off it probes 2 minutes after start and then every `check_every`; auto mode also retries due updates every 15 minutes, applies only inside the window, skips held agents, and does not retry a failed update until a newer version appears. State is `~/.aiman/agent-versions.json`; each run is appended to `~/.aiman/logs/agent-updates.log`. The gate reads live sessions from serve's database (`Server.LiveAgentSessions`: not stopped, not ended, base from `agent.BaseOf(AgentName)`, state from `ResolveHookState`). After a successful update serve re-probes the agent and reruns `agenthook.EnsureOnHost`. `agents.status {refresh}` returns `{"type":"agents","host","mode","window","agents":[AgentState + status, reason, held]}`; status is `current`, `update available`, `latest unknown`, `deferred: N sessions busy|running`, `needs root`, `updating`, `failed` or `not installed`. `agents.update {agent | all}` returns `{"type":"agents_update","host","results":[{agent, accepted, reason}]}` and runs accepted updates in the background; manual updates ignore mode, window and hold but not the gate. `aiman agents status [--json] [--refresh]` and `aiman agents update <cmd>|--all` are those calls. Serve wires `Manager.OnNewVersion` and `Manager.OnApplied` in `wireAgentUpdateNotify` to emit `domain.NotifyAgentUpdate` (`agent_update`, NeedsYou false) through the recorder with an empty session id; `Recorder.Emit` skips `min_interval` for empty ids, `notify.Format` uses the detail as the whole text, and the phone sink skips the kind. Notify mode (and held agents in auto mode) sends `codex 0.164.0 → 0.170.0 on <host> (press a on Agent API)` once per (agent, latest); auto mode sends `updated codex 0.164.0 → 0.170.0` or `codex update failed: <first line>` for updates it applied. Manual updates are not notified. The TUI page is `internal/ui/agent_updates.go`: `a` on the Agent API page opens "Agents on <host>" for the selected remote (Agent, Installed, Latest, Method, Status; `u` update selected, `U` all outdated, `r` refresh, `esc` back). It calls the RPCs through `usecase.ServeRPC` (`agentsRPC`), its results are routed in `routeRemoteServiceMsg` on every view, and it polls status every 2s only while the page is open and a row is `updating`. `agents.move_prefix {agent}` (`Manager.MovePrefix`, `internal/agentupdate/prefix.go`) reinstalls an npm agent under `~/.npm-global` through the runner: `npm config set prefix <home>/.npm-global`, then `npm install -g <pkg>@latest`, never sudo and never uninstalling the root copy; it passes the same gate as an update, then re-probes. `AgentStatus.RootOwned` (`OwnedByRoot`, Unix uid 0) marks a root-owned package-manager install, and the page offers `m` with a confirm screen listing `MovePrefixCommands("~", entry)` when `CanMovePrefix`. `~/.npm-global/bin` precedes the system PATH in `remoteAimanPreamble`, `ptyhold.childShell` and serve's `pathExec`. The provisioner installs Claude with `curl -fsSL https://claude.ai/install.sh | bash` (`claudeInstallCommand`), not npm.

---

## Session Restart

**Files:** `internal/ui/dashboard.go` (`restartSession()`), `internal/ui/restart_terminal.go` (`restartTerminal`)

The restart flow (triggered by `s` / `ctrl+r`, including switching agent):
1. Agent picker (same or different agent)
2. Serve writes `.aiman_handover.md`. The agent-written `.aiman_session_summary.md` is only the fallback.
3. `PrepareSession` for the chosen agent (configured `--model` / effort flags, summary prompt). The same agent gets the session's `LaunchModel`/`LaunchEffort` as its pick; `adoptRestartAgent` clears them on a switch and `ClearNativeAgentIdentity` wipes them in the DB
4. `restartTerminal`: `usecase.RecreatePTYSession` replaces the agent process in serve's PTY runtime under the same session ID
5. `DeliverInitialPromptPTY` sends the handoff prompt

**The worktree is never touched.** Only the agent process is replaced.

A legacy tmux row restarts only after the user confirms they stopped it on the host (`LegacyStopped`); `s` on such a row shows the stop command first. A successful restart sets `Backend` to `pty`.

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

1. Run-target picker (`viewStateRunTargetPicker`) → a configured remote server. Shown for
   any remote count, including zero, so a fresh install still has somewhere actionable to go.
   Number selects; `enter` confirms.
2. Mode picker → JIRA issue / new branch / existing branch / ad-hoc / autonomous trigger
3. JIRA issue picker → `m.sessionCfg.IssueKey` / `m.sessionCfg.Issue`. Only issues assigned
   to the current user in `integrations.jira.issue_statuses` (default:
   `jira.DefaultIssueStatuses`) are listed, and typed searches are scoped the same way.
4. Branch name editor
5. Repo picker (via `gh repo list`)
6. Directory picker (remote subdirectory of repo)
7. Agent picker (scans the remote for installed agents). The create wizard lists two tier
   rows above the agents — "Smart tier: <first entry>" and "Worker tier: <first entry>" —
   and the cursor starts on smart when `agents.smart` has entries. Choosing a tier sends
   `tier` and no agent; choosing an agent clears the tier.
8. Summary + AWS override screen
9. → `flowManager.CreateSession(ctx, sessionCfg)` in a goroutine

`CreateSession` does:
- git clone/fetch on remote
- `git worktree add`
- places worktrees in repo-scoped sibling directories named `<repo>@<branch>` to avoid cross-repo path collisions
- trust the working directory for the agent
- write `.aiman_task.md` (JIRA description + prior snapshot context)
- `pty.create` in the remote serve with the agent command
- `mutagen sync create`
- build the quality kit: push `~/.aiman/checklists`, check repo `requires`, list/match repo and user checklists, merge JIRA acceptance criteria with repo `done_when`, and write the kit blocks into `.aiman_task.md`; add the `Done-when met` to-do when conditions exist
- push AWS STS credentials

After the agent starts, `CreateSession` runs `nice graphify update .` detached in the worktree when `graphify` is on the remote PATH (`internal/infra/graphify`), adding `/graphify-out/` to `info/exclude` when the repo does not ignore it. `.aiman_task.md` then gains one line pointing at `graphify query`.

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
- **A1 focus modes**: `ctrl+p` focuses the reply line and `ctrl+t` focuses the selected session's to-do list (`focusTodos`). Keys edit that cached list through the agent API. `todo_changed` and `steer` update the cache and never refresh the pane.

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
- `repos` — per-repo worktree bootstrap keyed by `owner/name`: `copy` and `symlink` (paths relative to the repo root, taken from the main checkout, never overwriting) and `setup` (runs detached; log `aiman-setup.log` in the worktree's git dir). The same block carries the session quality kit: `done_when` (default Done-when conditions), `requires` (tools or `mcp:<name>` entries), and `agent_overrides` (per-agent model/effort). `CreateSession` applies it after the worktree is resolved, only for worktrees that create made (not attach, reuse, ad-hoc, no-repo or prepared). `MarshalServeConfig` copies it so serve-side creates see it.
- `integrations.jira.acceptance_criteria_field` — optional JIRA custom field id whose values become acceptance criteria; otherwise the list under the description's acceptance-criteria heading is used.
- `roles.verifier` — reviewer agent/model/effort override; the role entry goes first in `ReviewerCandidates()`, ahead of `verify.agents`, then `agents.smart`. Launch precedence is `agent_defaults` < `repos.<slug>.agent_overrides` < picked tier entry < `roles.verifier`.
- `agents.smart` / `agents.preferred` — the two model tiers (`internal/infra/config/tiers.go`, `config.TierSmart`/`TierWorker`, `ValidTier`, `AgentsConfig.Tier/SetTier`). Each entry is a command string or `{agent, model, effort}` (`config.PreferredAgent`); bare entries marshal back as strings so old configs round-trip unchanged, and an unset tier stays out of the file while a cleared one is written as `[]`. `agents.preferred` is the worker tier; `agents.smart` is the smart tier. The same agent may repeat with different models.
  - `coordinator.PickTier(agents, want, installed)` returns a `Pick{Entry, Tier, Requested, FellBack}`: the first installed entry of the requested tier, else the other tier with `FellBack` set. An empty `want` means worker. Both tiers missing is `ErrNoPreferredAgent` naming what each tier tried. `coordinator.PickAgent` is the single-list primitive underneath.
  - serve's `session.create` with no `--agent` picks a tier: smart for a session the user starts, worker for one created inside a session (`--tier` overrides). Its model/effort land on `domain.Agent.LaunchModel/LaunchEffort`, which `ResolveLaunchDefaultsWith` layers as `agent_defaults` < repo `agent_overrides` < tier entry < role. `--tier` with `--agent`, or `--model`/`--effort` without `--agent`, is `invalid_params`.
  - Menu → Model tiers (`internal/ui/agent_preferred.go`) edits both: tabs smart/worker (tab and shift+tab in the list step), each with its own "Used for" guidance and empty-tab fallback hint; `a` agent → model (Pi: provider then type-to-filter model, listed by `pi --list-models` on the default remote via `agent.ModelCache`; others: `LaunchCatalog` plus Other) → effort; `e` edits an entry's model/effort; a listing error falls back to typing the model; `enter` saves both tiers, `esc` discards both.
- `agents.update.mode` — `off`, `notify` or `auto` (default). Unless `off`, every launch disables the agent's own updater: `infraAgent.GuardFor` flags are added in `PrepareSession` and `skills.InteractiveLaunch(cmd, worktree, guard)`, and its env is merged in `CreatePTYSession` without overwriting keys the caller set (`PTYSpec.NoUpdateGuard` skips it). Revive takes `usecase.ReviveOptions{NoUpdateGuard}`; the dashboard passes `!cfg.AgentUpdateGuards()`, so mode off adds neither guard flags nor guard env on revive. Codex keeps `--disable in_app_updates` in every mode, because its update dialog exits the holder.
- `roles.handover`: handover child agent/model/effort. `HandoverCandidates(worker)` returns entries: `roles.handover` (with its model/effort), then the worker tier entries with their models, then the worker's own agent. The picked entry's model and effort reach the child's `domain.Agent`, so a worker-tier model runs the handover child. The role model and effort apply only when the role names no agent or names the one picked. With no role model and no entry model the child runs the agent's `LaunchCatalog.Cheap` model (claude `haiku`, codex `gpt-5.4-mini`, copilot `gpt-5-mini`, agy `gemini-3.7-flash-low`).
- `handover.child`: start a handover child on each handover write; unset means on. `handover.timeout`: limit for one child run; default 10m. `MarshalServeConfig` pushes both to serve.
- `verify.test_command` — command run against a reviewer snapshot; a review passes only when it exits 0. `verify.before_pr` gates PR creation on a passing review; `verify.reproduce` runs the test command at the base first. `verify.PickReviewer` skips a candidate only when its binary and its model both match the worker's (`worker.LaunchModel`); a bare entry keeps the binary-only rule, so an all-Pi setup still gets an independent reviewer. The picked entry's model and effort go on the reviewer child's `domain.Agent` (`verify.Review.AgentModel`/`AgentEffort`).

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

10. **AWS_PROFILE is sanitised, not trusted**: `usecase.SharedSessionAWSEnv` exports a profile only if it survives `SanitizeSessionAWSProfile` — legacy `aiman-<id>` names and names missing from `~/.aws` are dropped so the session falls back to the default credential chain rather than pointing at a profile that cannot resolve. Do not bypass it by setting `AWS_PROFILE` directly in the terminal environment code.

11. **`AgentEnded` is not cleared by `Save`**: `agent_ended` is COALESCE-protected, and an unset `AgentEnded` is written as an empty string, so saving `AgentEnded: false` leaves a stored `"1"` in place. `agenthook.ApplyReport` clears the in-memory flag only when a later report carries a lifecycle state and is not itself an end. A report with no state returns first and leaves the latch set. `handleReportAgentSession` saves, then must call `Repository.ClearAgentEnded` when the flag flipped from true to false. That method writes `agent_ended = ''` directly. Dropping the call leaves the row ended after the process has reported it is alive. `ClearNativeAgentIdentity` also blanks `agent_ended`, but only as part of wiping vendor identity on an agent switch. It is not the report path.

12. **Teardown writes the forget handover first**: the TUI removes the worktree before it calls `session.forget`, so serve's own forget handover (`noteForgetHandover`) cannot write the file and fails by design. `terminateSaveContext` calls `WriteHandoverOnRemote(..., "forget")` at teardown step 0 instead, best effort with a 20s cap. That write starts the child too. If the child finishes after the worker row is gone, only the note and the memory job go out. Do not move the call after worktree removal.

---

## Terminal runtime

Every session runs in the PTY runtime of the remote `aiman serve`. aiman never
starts, attaches to or stops tmux.

`internal/ptyhold` is the durable holder: `aiman pty hold` is a small process,
detached with `setsid`, that owns one PTY. Its state is files under
`~/.aiman/pty/<id>/` (`request.json`, `meta.json`, an append-only `spool`
rotated to `spool.old` at 8 MiB, `term.sock`, `resize`, `kill`, `exit`,
`activity.json`). `internal/ptyruntime` is serve's thin client over that
contract. The serve unit uses `KillMode=process`, so stopping, restarting or
updating serve leaves holders and their agents running; the next serve finds
them again by scanning `~/.aiman/pty/`. Sessions also survive laptop
disconnects. A holder ends when its agent exits, on `pty.kill`, or on a host
reboot.

Socket API (`internal/server/handlers_pty.go`): `pty.create/list/get/input/
capture/resize/run/kill/forget` are plain JSON methods; `pty.attach` answers
once then takes over the connection: output streams out as raw bytes, client
messages are framed (`internal/server/attach_framing.go`, `[len BE][kind][payload]`,
kinds 0x01 input, 0x02 resize), which is what makes live window resize possible.

**Revive**: when a session's holder is gone (the agent exited or the host
rebooted), attaching revives it via `usecase.ReviveIfNeeded`. The agent
relaunches with its stored `LaunchModel`/`LaunchEffort` and its native resume
flag (sidecar id first, stored `AgentSessionID` as fallback; refuses if neither
exists so it never silently forks a fresh conversation).

**Boot wait**: `acceptWorkspaceTrustPTY` polls the new pty (budget 20) before
the first prompt is typed. Each poll checks, in order: a startup update dialog
from `infraAgent.UpdateDialogs` in the bottom `UpdateDialogTailLines` rows
(press its skip key, never Enter, and keep polling), a workspace-trust dialog
(Enter), then a ready composer. The outcome line is
`AIMAN_BOOTWAIT:<timeout|trust|ready|update>:<attempts>`. Codex's update menu
rows start with `›`, so the update check must stay ahead of `composerReadyRe`.
`steerReady` holds `dialog` while a pty screen ends in one of those dialogs,
whatever the hook reports. Fixtures are in `internal/usecase/testdata/bootdialogs/`.

CLI (`cmd/aiman/pty_cmd.go`): `aiman pty list|create|get|capture|input|kill|
forget|attach`. Attach puts the local tty in raw mode and relays; ctrl+q
detaches without stopping the session. `--params-file` / `--file` flags keep
secrets and prompt text out of argv.

The laptop drives everything through SSH (`$HOME/.local/bin/aiman pty …`),
routed by `usecase.CaptureSessionPane / SendSessionPrompt /
TerminateSessionTerminal`. Call those rather than building pty commands.
`remotes[].session_backend` is ignored with a config warning.

**Legacy tmux rows**: a session created when aiman still ran tmux keeps
`Backend` `tmux` (`domain.IsLegacyTmux`). The row shows the badge
`tmux (legacy) · stop it on the host, then s`. Attach, revive, capture and
prompts refuse it with `domain.LegacyTmuxError`, whose `Hint` names the host and the
exact stop command (`tmux kill-session -t '=<name>'`); serve answers
`CodeLegacyTerminal`. Terminate leaves the tmux session to the user and still
removes the worktree after confirmation. `s` asks the user to confirm the tmux
session is stopped, then restarts the agent into pty. Non-test string literals
that run or name tmux live only in `internal/domain/legacy_tmux.go` and
`internal/ui/legacy_tmux.go`; `internal/usecase/notmux_guard_test.go` fails on
one anywhere else, and on any literal naming mosh.

---

## Remaining TODOs (from PLAN.md)

- **Remote VM Bootstrapper**: Connect to a new VM and install baseline tooling (git, go, node, claude, cursor, agy, kilo, acli), configure SSH keys, and authenticate agents.
- **AI Compute Monitoring**: Provider subscription/usage monitoring (Anthropic, Google, OpenAI) — credit balances and usage tracking.
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
