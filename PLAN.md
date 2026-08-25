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

### Restart/recovery robustness ✅
Restarting a session could leave a pane that only *looked* dead: both create and
restart append `; exec bash -i` after the agent command so a crashed process
never takes the whole tmux session down, but that meant a crashed or
never-started agent was indistinguishable from a normal idle shell — nothing in
the dashboard flagged it, and reviving a worktree that outlived its tmux
session (host reboot, tmux server crash) always forced the full agent-picker
flow even when aiman already knew which agent had been running there.

- `pane.Classify` now reports the bare-shell-prompt case as `domain.AgentStateExited`
  instead of folding it into `AgentStateIdle` — the signal already existed, it was
  just mislabeled. The dashboard shows it as a distinct `⚠ agent exited` state
  (`internal/ui/session_list.go`), colored the same as other stuck states.
- `s` on a session whose agent is already known (persisted `AgentName`, or
  inferred from a hook sidecar's transcript path via
  `agenthook.InferAgentName` when the DB row itself was lost) now skips the
  agent picker entirely and resumes directly — `agent.FindKnown` resolves the
  name back to a runnable command without a remote scan. `S` remains for
  deliberately switching agents; an unresolvable identity still falls back to
  the picker rather than guessing.
- `CaptureRestartSessionSummaryBestEffort` (`internal/usecase/restart_handoff.go`)
  wraps the restart-handoff capture so *any* failure — not only the
  already-graceful context-timeout case — degrades to "no handoff" instead of
  aborting the restart. A missing or failed handoff can no longer block
  reviving a session.

### Config-save test isolation and startup session flicker ✅
Two unrelated but severe robustness bugs, both found the hard way (running
this repo's own test suite locally overwrote the maintainer's real
`~/.aiman/config.yaml`, and the emptied remotes list then cascaded into the
whole session history being pruned from the database on the next launch):

- `TestSetupModelSavesOnEnterViaHarness` called the real `Config.Save()`
  (`internal/infra/config/config.go`), which resolves `~/.aiman/config.yaml`
  from the actual `HOME` env var with no test-injectable override, and never
  sandboxed `HOME` the way every other save-exercising test in the package
  does. `Config.Save()` also now creates `~/.aiman` if missing rather than
  silently failing the write (this was also the test's pre-existing CI
  failure, independent of the isolation bug).
- `loadConfiguredSessions` (`internal/ui/startup.go`) deleted any DB session
  whose `RemoteHost` didn't resolve against the current config — and an
  empty remotes list fails that check for *every* session. It now only
  prunes when the config has at least one remote; zero remotes is treated as
  "probably broken," never as "delete everything."
- Separately: `runDiscovery` (`internal/ui/startup.go`) and the dashboard's
  "r" refresh handler both marked a host `scannedHosts[host] = true` right
  after `Connect` succeeded, discarding `Discover`'s own error
  (`sessions, _ := discoverer.Discover(...)`). A transient scan failure (one
  flaky SSH command) was then read by the merge step as "this host was fully
  scanned and these sessions weren't found" — so a session would show on the
  DB-backed first paint and then disappear once the (failed) scan result
  landed. `usecase.DiscoverHostSessions` now reports scan success distinct
  from "found nothing," and both call sites use it.

### Revive abandoned worktrees, with multi-agent detection ✅
Discovery's remote scan already walks the whole configured repo root for
every git worktree, known to aiman or not — but a worktree aiman has never
tracked before was silently dropped before reaching the dashboard, to avoid
flooding the session list on a remote with dozens of them. There was also no
way to identify which agent had worked in such a worktree, since the
existing hook-sidecar signal only exists for sessions aiman itself launched.

- **Menu → Revive Worktree**: an on-demand screen (`internal/ui/revive_worktree.go`)
  that scans a remote's repo root for worktrees not already visible anywhere
  in the dashboard (`usecase.SessionDiscoverer.OrphanWorktreeSessions`) and
  lists each with its candidate agent(s).
- **Agent identity from git history, not project files**: per-vendor project
  files (`.claude/`, `AGENTS.md`, …) are often committed to the repo and
  shared across every worktree of it, so they say "this repo supports agent
  X," not "agent X worked in *this* worktree." Commit trailers are genuinely
  per-worktree and already capture sequential multi-agent work: many agent
  CLIs (including this one) append `Co-authored-by: <name> <email>` when
  they commit. `ssh.Manager.WorktreeCoAuthorHints` collects those in one
  batched round trip; `agenthook.InferAgentNameFromText` and
  `usecase.ResolveWorktreeAgentCandidates` turn them into a ranked,
  deduplicated candidate list per worktree.
- Selecting a worktree resumes with as little friction as an already-known
  session does: zero candidates falls back to the full agent picker, exactly
  one revives immediately with no picker or confirm, and two or more show a
  short pick-list of just those candidates (never a guess) before reviving.
  All three paths hand off to the existing `restartSession()` — no new
  session-launch logic was needed.

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

### Session activity detection ✅
`detectSessionActivity` substring-matched the whole pane, lowercased, with input patterns checked
before working ones — so "confirm" or "are you sure" anywhere in scrollback decided the state and
a busy agent read as blocked. The pattern list carried `[y/n]` and `(y/n)` three times each.

Replaced by `pane.Classify`, which uses positive signals over keywords:
- `ssh.Manager.SessionActivityAges` reads tmux's own `#{session_activity}` for every session in one
  round trip — no pane capture, which is what makes it scale past a handful of sessions.
- Only the last `pane.TailLines` lines are examined. An advancing elapsed timer means working, a
  rendered choice list means blocked, an agent input box with no timer means idle.
- `Result.Confidence` marks the cases worth escalating.

Validated against seventeen live sessions on regent0: all resolved at high confidence, none unknown.

Corrected in v0.9.5 after a live miss: a working Claude session read as idle because the six-line
tail cut the spinner off. Agents draw their input box *below* the spinner, not above it — Claude Code
puts seven lines of chrome underneath — so the evidence sat outside the window and only the
idle-looking furniture remained. `StatusLines` (20) is now used to look for a running turn while
`TailLines` (6) still anchors prompt detection, and the model tier is given the same wider window;
feeding it the truncated tail is why both tiers agreed on the wrong answer.
Real panes corrected two assumptions — `new task? /clear` is Claude's *idle* hint rather than a
question, and `Brewed for 42s` is past-tense completion.

The local-model tier exists but is not automatic. `i` runs the rules and the model side by side and
reports both; `I` keeps the summary `i` used to trigger. Model choice was measured against captured
panes rather than invented ones:

	qwen3:4b     3/3  ~600 ms
	qwen3:1.7b   1/3  ~210 ms
	gemma3:270m  2/7  ~270 ms  (on an easier synthetic set)

Small models read an agent idling at its own input box as working, so `ai.classify_model` defaults
to the summarisation model. Two prompt findings: instructions inline beat the same text in the
`system` field by one of three, and temperature and schema shape made no difference.

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

### Startup is instant; doctor checks stream in ✅
With discovery off the critical path the slowest doctor check became the gate. Those checks
report into the dashboard footer permanently, so gating the splash on them only delayed the
first paint of information the dashboard shows anyway.

- `startupReadyMsg` is emitted by `Init`, so the dashboard opens on database contents alone.
  Checks that land first are carried across; the rest arrive as `checkResultMsg` and are
  applied by `Model.applyCheckResult`.
- The footer reserves a row per known check via `startupCheckNames` and shows in-flight ones as
  `checking…`, so streaming results fill rows in place. Pane sizing uses the same fixed count;
  previously it sized off the running result count, which resized the panes three times per
  launch.
- Re-running checks from the admin menu now replaces rows instead of appending duplicates.

Two checks were also doing far more work than they needed:
- `CheckGit` called `ListRepos`, fetching every personal and org repository over the network
  purely to report a count, then discarding the list — the repo picker fetches them again when
  opened. `gh auth status` answers the question the check asks. **3.7 s → 596 ms.**
- `CheckSSH` opened a fresh connection per remote, sequentially. It now probes concurrently
  over the shared ControlMaster socket. **1.8 s → 160 ms.**

`git.Manager.ListRepos` logged org failures with `fmt.Printf` to **stdout**, which would have
corrupted the TUI mid-frame. It now uses `log`, and the TUI redirects the standard logger to
`~/.aiman/aiman.log` (`config.GetLogPath`) for its lifetime so no background goroutine can
write onto the rendered frame.



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
