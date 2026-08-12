# Aiman Implementation Plan: Status & Roadmap

## 1. Work Completed ✅

### Infrastructure & Foundation
- **Project Rebranding**: Full rename from Flux to **Aiman** (module paths, docs, binary).
- **Centralized Config**: Implementation of `internal/infra/config` managing `~/.aiman/config.yaml`.
- **Robust SSH Multiplexing**: Implemented auto-managed SSH sockets (`ControlMaster=auto`) in `~/.aiman/sockets/` for high-performance, non-interactive command execution.
- **JIRA v3 Integration**: Migration to the latest Atlassian search APIs.
- **Mutagen Integration**: Background discovery of active sync sessions and mapping to remote worktrees.

### TUI Framework (`bubbletea`)
- **Splash Screen**: Real-time initialization sequence with visual feedback for JIRA, Git, and SSH health.
- **Unified Remote Management**: Single-screen UI for scanning `known_hosts`, manual server entry, and root directory validation.
- **Split-Pane Dashboard**: 
    - **Sidebar**: Dynamic session tracking with JIRA key and Repo mapping.
    - **Top Panel**: Session metadata and status.
    - **Main Panel**: Decoupled "Preview" (ANSI stream) and "Terminal" (interactive emulator) modes.
- **External Handoffs**: 
    - `ctrl+s`: Full-screen native terminal attach.
    - `v`: Open local VS Code (`code`) in synced directories.
    - `ctrl+t`: Toggle embedded interactive terminal.

### Use Cases
- **Session Discovery**: Engine to map remote tmux sessions -> CWD -> Git Repos -> JIRA keys.
- **Orphan Discovery**: Discovery of orphaned git worktrees and mutagen sync sessions.
- **Doctor Checks**: Automated validation of environment and credentials on startup.
- **Flow Wizard**: Issue -> Branch -> Repo -> Directory -> Agent scan -> Summary.
- **Session Restart**: Restart existing or inactive sessions with a new agent selection.
- **Mutagen Sync Recovery**: Recreate sync for a selected session from the dashboard.
- **Git Intelligence**: Real-time git status and PR tracking integrated into the dashboard.

### Bug Fixes & Reliability (v0.6.43 – v0.6.57)
- **Session restart**: Identified and fixed root causes of restart hangs and failures across many releases.
  - Simplified `restartSession` from 8+ SSH calls to 1 (eliminate ValidateDir, git metadata, trust cmds).
  - Fixed data race: capture `sessionCfg`, `db`, `flowManager` at goroutine dispatch time.
  - Fixed stale-reference bug: `Save` on discovery was overwriting DB fields with empty live-discovered values (`worktree_path`, `working_directory`, `branch`, `agent_name`, `mutagen_sync_id`). Fixed with `COALESCE(NULLIF(excluded.field,''), sessions.field)` for all critical fields.
  - Added per-call SSH timeouts (30 s) with `ServerAliveInterval`/`ServerAliveCountMax` to detect dead connections.
  - Added `ResetControlSocket()` call before restart SSH command to clear stale ControlMaster state.
- **Mutagen sync recreate** (`Ctrl+Y`): Added sync status/percentage progress display.
- **Session start latency**: `mutagen sync create` ran with no ignore patterns, so a new
  session mirrored `node_modules`, build output and caches over the wire before it became
  usable. Measured 2.8 MB/s to the dev box against mirrors of 1.4–3.5 GB, which is the
  10-minute session start. Now excludes a default set (`mutagen.DefaultIgnores`), tunable
  via the `sync:` config block. `.git` stays synced so the mirror remains a git checkout.
- **Unaddressable tmux session names**: tmux parses a target as `session:window.pane`, so a
  branch containing `.` produced a session tmux stored with `_` while aiman remembered the
  dot. Every later `kill-session`/`capture-pane`/`send-keys` then resolved to a pane and
  silently did nothing (`can't find pane: …`), so terminate left the session running and the
  next create failed with `duplicate session`. `domain.SanitizeTmuxSessionName` now applies
  tmux's own normalisation at both derivation sites.

- **Default AWS profile**: `aws.default_profile` / `aws.default_region`, with per-remote
  `aws_default_profile` / `aws_default_region` overrides, pre-fill the summary screen instead
  of always starting from the delegation's `source_profile`. Resolved by
  `Config.ResolveAWSSessionDefaults`; profile and region resolve independently.
- **Plural delegations ignored by the summary screen**: the AWS override section only read
  `remote.AWSDelegation`, so a remote configured with `aws_delegations` showed no AWS fields.
  Now selected via `AllDelegations()`.

### Discovery performance ✅
Startup discovery issued ~200 strictly sequential SSH commands, each a fresh local `ssh`
process at a measured 250 ms, so the splash screen held for ~50 s and got worse as the remote
accumulated worktrees.

- `domain.BatchDiscovery`: optional capability letting a RemoteExecutor answer a whole
  discovery pass in two round trips. Implemented for `ssh.Manager` (`ScanWorktreeTree`,
  `ScanTmuxSessionDetails`), which resolve each worktree's liveness and aiman id server-side.
- `SessionDiscoverer` type-asserts for it and falls back to the per-item calls when absent or
  when a batch call fails, so non-SSH executors and tests are unaffected.
- `Manager.executeWithTimeout` gives batch scans a 2-minute budget instead of the 30 s
  single-command one.
- `runDiscovery` now carries a 3-minute timeout; it previously used `context.Background()`,
  so a wedged remote held the splash open indefinitely.
- `--absolute-git-dir` replaces `--git-dir` for aiman-id lookups: the latter returns a bare
  `.git` for a main worktree, resolving the id path against the SSH login directory.
- Measured against regent0 (33 repos, 91 worktrees, 9 tmux sessions):
  worktree sweep 149 calls / ~39 s → **1 call / 1.31 s**; tmux sweep ~45 calls / ~11 s →
  **1 call / 0.60 s**.
- `discoverSession` deleted; `tmuxRecordsPerItem` + `sessionFromRecord` replace it.

### Autonomous trigger daemon now actually reaches remotes ✅
Scheduled Prompts and autonomous GitHub triggers were configurable in the TUI but could never
fire. The execution path was already complete: the dashboard installs `aiman-trigger` onto a
remote (`install.sh | BINARY_NAME=aiman-trigger sh`), launches it there in a tmux session, and
manages it from the Daemons tab. The daemon runs *on* the remote, which is why `local.Executor`
is the correct executor for it.

The only missing piece was release plumbing:
- `release.yml` built only `./cmd/aiman`, so `install.sh` resolved
  `aiman-trigger-<goos>-<goarch>` to an asset that was never published and the remote install
  failed. The matrix now builds and packages both binaries per platform; the existing
  `aiman-*` release glob already picks up the new assets.
- `ci.yml` now compiles `./cmd/aiman-trigger` too, so it cannot silently rot again.
- `install.sh` hardcoded `./cmd/aiman` in its build-from-source fallback, so
  `BINARY_NAME=aiman-trigger` would have installed the TUI under the daemon's name. It now
  builds `./cmd/$BINARY_NAME` and fails loudly if no such command exists.

Also removed `schedule` from the CLI usage text: it was advertised but had no case, so it fell
through to "unknown command".

### Dashboard renders from SQLite first ✅
The splash screen gated on four tasks, one of which was the remote scan, so the dashboard
could not appear until every remote had been walked. It now gates on the three doctor checks
only and opens on whatever the database already holds.

- `StartupModel.pending` drops to 3; `discoveryResultMsg` is recorded but no longer decrements
  the gate.
- Discovery still starts in `Init`. Whichever way the race resolves, the result reaches the
  dashboard: if it lands first the handoff replays it as a command, otherwise bubbletea
  delivers it to the dashboard directly once the model has been swapped.
- The ~100-line merge in `startup.go` is deleted. `Model.applyDiscoveryResult` already
  performs the full merge and reloads the database itself, so there is now one merge
  implementation instead of two that had already drifted (the startup copy did not apply the
  `WorktreePath` fallback).
- `Model.discoveryPending` marks the window between opening on database contents and the first
  scan landing; the session list title shows `· scanning remotes…` so stale rows are not
  presented as confirmed.



- **SSH Backgrounding**: Avoid using `ssh -f` (backgrounding) inside Go's `os/exec` as it often receives a `SIGKILL` or `SIGHUP` when the parent Go process handles signals. Stick to `-o ControlMaster=auto` and `-o ControlPersist` for robust multiplexing.
- **Bubbletea Versioning**: Do **not** mix `bubbletea` v1 and v2. Libraries like `bubbleterm` that require v2 will cause interface conflicts with standard `bubbles` components. If a terminal is needed, use `vt10x` to build a custom v1-compatible component.
- **Socket Path Limits**: Unix sockets (used for SSH ControlPath) have strict character limits (~100-108 chars). Keep socket names in `~/.aiman/sockets/` short and hashed if necessary.
- **ANSI Capture**: When using `tmux capture-pane -e`, ensure the TUI handles the resulting ANSI sequences correctly to avoid rendering artifacts.

## 3. Immediate TODOs 🚀

- [x] **Flow Manager Implementation (The Core Workflow)**:
    1.  **Initiation**: Bind `n` key on the dashboard to start the new session wizard. ✅
    2.  **JIRA Issue Selector**: Searchable/browsable list of assigned or recent JIRA issues. ✅
    3.  **Branch Generation**: Auto-propose git-compatible branch name + editable input. ✅
    4.  **Repo Selection**: List accessible Git repositories (via `gh repo list`). ✅
    5.  **Remote Execution Orchestration**:
        *   Verify if the selected repository is cloned on the active remote server's configured `root` path. ✅
        *   If missing, execute `git clone`. ✅
        *   If present, fetch and ensure the base repository is updated on the `main` branch. ✅
        *   Check for an existing git worktree matching the proposed branch name. ✅
        *   If missing, execute `git worktree add ../<branch-name> <branch-name>`. ✅
    6.  **Scope Selection**: Directory picker to select a sub-directory within the repo. ✅
    7.  **Agent Selection**: Scan remote and select agent (Claude, Antigravity, OpenCode, Copilot). ✅
    8.  **Summary Confirmation**: Show selected issue/branch/repo/dir/agent before creation. ✅
    9.  **Session Bootstrapping**:
        *   Launch a new `tmux` session named after the issue key/branch. ✅
        *   Start the selected agent CLI within that tmux session, scoped to the chosen directory. ✅
    10. **Local Sync**: Establish a `mutagen` sync session to a local path. ✅
- [x] **Session Termination** (Key: `ctrl+k`):
    - Terminate mutagen sync session.
    - Stop the agent process running in the tmux session.
    - Kill the tmux session.
    - Remove the associated git worktree.
    - Clean up local sync directory.
    - Update session status in database.
- [x] **SQLite Persistence**: Fully wire the existing `internal/infra/sqlite` repository to save both discovered and newly created sessions, tracking their full lifecycle.
- [x] **Git Intelligence Panel**: Comprehensive git status display for each session showing:
    - Associated pull request (if exists) with link and status
    - PR review state: approved, changes requested, pending reviews
    - Open review comments count
    - PR check status (CI/CD passes/fails)
    - Uncommitted changes (staged/unstaged)
    - Un-pushed commits count
    - Untracked files list
    - Branch tracking status (ahead/behind remote)
    - Similar UX to lazygit but integrated into the dashboard ✅
- [ ] **Agentic Patterns**:
    - Develop robust agentic patterns (e.g., Orchestrator-Worker-Validator).
    - Logic to translate these patterns for various supported coding tools.
    - Synchronize patterns to the remote dev server.
- [x] **JIRA-Driven Initial Prompt**: When launching a session from a JIRA issue, the issue description is written to `.aiman_task.md` in the worktree and the agent receives an initial prompt to read it, gather context, and prepare a plan. Works for Claude Code, Antigravity, Cursor, and OpenCode.
- [ ] **Skill Injection**: Implement the logic to map local "skill" files to remote agent configuration paths before agent launch.
- [ ] **MOSH Support**: Add an option to hand off to MOSH for high-latency interactive connections.
- [x] **CI/CD Pipeline & Releases**: GitHub Actions workflow for:
    - Running tests, linting, and type checking on PRs.
    - Building executables for macOS (Intel & Apple Silicon), Linux, and Windows.
    - Creating GitHub releases with all platform binaries as artifacts.
    - Automatic versioning based on git tags.
- [ ] **Remote VM Bootstrapper**:
    - Connect to a new remote VM and install baseline tooling (git, tmux, go, nodejs, npm, curl, claude, cursor, agy, opencode, acli).
    - Configure SSH keys and git SSH auth.
    - Authenticate Atlassian (acli) and supported coding agents.
- [ ] **AI Compute Monitoring**:
    - Provider subscription/usage monitoring (Anthropic, Google, OpenAI, etc.)
    - Credit balances and general usage tracking (via APIs or MCP servers).
- [ ] **EC2 Provisioning**:
    - Spin up and terminate EC2 instances to use as remote servers.
    - Wire instance lifecycle to Aiman’s remote registry.
- [x] **Dev Console Panel**:
    - Collapsible dev console panel to view logs and debug output in-app (toggle with backtick key).

## 4. Architectural Strategy (Reminder)
Keep following the **Clean Architecture** pattern. Ensure that the `internal/usecase` layer remains the only place where domain entities are coordinated, and keep infrastructure-specific logic (like `mutagen` or `ssh` CLI flags) strictly within `internal/infra`.
