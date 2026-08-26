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

### Masked credential fields truncated their input ✅
The JIRA issue picker showed nothing to start a session from. The cause was not
the JQL or the status filter: the API token input carried the setup screen's
shared `CharLimit = 128`, and Atlassian's current tokens (`ATATT…`) run about
192 characters, so every token was silently cut to 128 on entry. The field is
masked, so there was nothing on screen to notice, and the saved token then
failed every request with 401 — which presents as "no issues in the picker",
pointing at exactly the wrong part of the system. (The JIRA doctor check does
report `Authentication failed`, which is the real clue.)

A credential field must never cap near the credential's length. Both masked
inputs — the JIRA token and env-secret values, the latter capped at 256 — are
now 4096. Guarded by tests that fail if either cap returns, since a truncated
secret is invisible by construction.

Note for anyone who entered a token before this fix: the stored value is
truncated and cannot be recovered, so it has to be re-entered.

### The per-session pty choice was discarded twice ✅
The run-target picker's `b` toggle looked like it had stopped existing. It was
still drawn, and still flipped the value — but two separate places threw the
choice away before it could reach session creation, so every session came out
tmux:

- `resetSessionCfg` rebuilt the config for each mode-picker branch keeping only
  the host, so picking *any* mode dropped the backend.
- `createSession` then assigned `remote.SessionBackend` unconditionally,
  overwriting whatever survived with the remote's default.

A remote's `session_backend` is a default, not an override: `resolveSessionBackend`
now applies it only when nothing was chosen, and `resetSessionCfg` carries the
backend alongside the host. Choosing pty on a tmux-default remote is the only
use for that toggle, and it was exactly the case that failed.

The summary screen now shows `Backend:` as well. Nothing on the confirmation
screen previously named it, which is why a toggle that did nothing was
invisible right up to session creation.

### serve listed nothing, because its database was never the index ✅
With the socket reachable, `aiman session list` still returned an empty list
while six sessions were running, and `AIMAN_SESSION_NAME`/`AIMAN_GROUP` were
absent from every session. Neither was a bug in the listing code: sessions are
created by the laptop TUI, which persists to the *laptop's* database, and
`loadSessions` read only serve's own — a database on the remote that nothing
ever writes to. So the agent API's whole premise, agents addressing sibling
sessions, could not work for any session the TUI had created.

serve already holds a local executor, so it can simply look at its own host.
`liveSessions` enumerates live tmux sessions (keyed by the `AIMAN_ID` in the
tmux environment, which doubles as the filter that keeps serve's and the
trigger daemon's own tmux sessions out) plus running PTY sessions, and
`mergeLiveSessions` folds them over the stored rows: the database still wins
for identity the host cannot know (name, group, issue, branch, repo), while
liveness and terminal facts come from the host. Discovered sessions are
labelled in memory only — this host does not own them, and writing rows here
would put identity in two places and let it drift.

Naming needed its own path rather than the existing `backfill`:
`AssignSessionName` serves session *creation*, where the first session earns
the friendly name "impl" and a long tmux name fails `ValidateSessionName` and
is discarded — which turned six distinct sessions into `impl` … `impl-6`,
indistinguishable and useless for addressing one. `labelDiscovered` uses the
tmux/PTY session name, the label already shown in the dashboard, and derives
the group from the issue key.

Verified against regent0's six live sessions: all six listed with their real
names, grouped by issue key (WTB-1895 … WTB-1917), resolvable by name through
`session get`, reporting live state, and correct under `--group` filtering.

### A stale socket path no longer needs a session restart to recover ✅
Not injecting the laptop's paths fixes *new* sessions, but every session
already running still had the bad `AIMAN_SOCKET_PATH` in its environment — and
`tmux set-environment` cannot re-environ a process that is already running, so
the only route was restarting six live agents mid-work.

`socketPath` now honours `AIMAN_SOCKET_PATH` only when it points at something
that exists, falling back to this host's own default otherwise. A path that
does not exist is never the right answer, so this repairs already-running
sessions in place once the *remote* binary is updated — no restart, no lost
agent context. An explicit override that does exist is still respected, and
when there is no better path the requested one is kept so the error names what
was actually asked for.

### The agent API was unreachable from inside a session ✅
An agent in a remote session could never talk to `aiman serve`: every
`aiman session …` call answered `server_not_running`, naming a socket under
`/Users/pieter/.aiman/` — the *laptop's* path — while serve was healthy on the
remote at `/home/code/.aiman/aiman.sock`. The skill telling agents to use the
API was correct; the environment handed to them was not.

`aimanRuntimeEnv` filled `AIMAN_SOCKET_PATH` from `config.GetDir()` and
`AIMAN_BIN_PATH` from `os.Executable()`. Both resolve on whichever machine
*creates* the session, which for the TUI is the laptop, while the session runs
on the remote — so both values were paths that do not exist where they were
used. `AIMAN_BIN_PATH` broke hook reporting the same way, silently, which is
why agent state and identity reporting had been unreliable.

Neither is injected now. Left unset, each resolves correctly on the machine
that actually uses it: the in-session binary falls back to its own
`config.GetDir()` (`cmd/aiman`'s `socketPath`), and the hook reporter resolves
the binary itself (PATH, then `$HOME/.local/bin/aiman`). Verified against
regent0: the injected path reproduces `server_not_running`, and unset returns
a real session list. Both restart and create still *unset* the stale variables
in the tmux environment, so sessions made by older builds are repaired too.

### Discovery could hide detached PTY sessions ✅
`ScanPTYSessions` swallowed every failure and returned nil, so a transient SSH
error looked identical to "this host has no PTY sessions" — for a host
discovery had just marked as scanned. The merge step reads a known session
missing from a scanned host as dead, so live PTY sessions dropped out of the
dashboard: the same flicker already fixed for tmux, still live for PTY, and
worse here because a detached PTY session that isn't listed cannot be
reattached.

The scan command now guards on `command -v aiman` and always prints a valid
empty list, so a remote with no runtime is a clean empty answer while a failed
call is a real error that fails the whole `Discover` — leaving the database's
view of that host untouched. The session list also marks PTY-hosted sessions
(`| pty`), since they are reattached and torn down differently from tmux ones.

### Previews smeared their colours; PTY previews had none ✅
Three separate causes behind "the previews all look a bit weird, and pty
sessions are still not color", found by capturing real panes off a live remote
rather than reading the render path.

**Colour bled out of every tmux preview.** `capture-pane -e` reproduces what the
agent painted and stops there, so the styling is never terminated: in the
capture used to diagnose this, **53 of 54 lines left colour open and not one
ended in a reset**. Written into a panel, that state carries past the end of the
line and colours whatever is drawn next — the rest of the row, the line below,
the dashboard's own chrome. Each line is now sealed with a reset when it ends
mid-style. Verified on the real capture: open lines 53 → 0, with the visible
text and width of every line unchanged.

**PTY previews were monochrome.** The renderer read only each cell's character
and dropped its attributes, so a tmux preview came back with 205 SGR sequences
and the equivalent PTY preview with zero. It now emits foreground and
background per run, sealing each row. Backgrounds matter as much as
foregrounds here: vt10x resolves reverse video by swapping the pair when it
stores the cell, so dropping the background would render reversed text
dark-on-dark. Confirmed against a live session's spool: 111 SGR sequences,
truecolour text, comment-grey and dim.

**Activity detection was reading escape bytes** — a pre-existing bug that hit
tmux sessions, and would have hit PTY ones the moment they gained colour.
`pane.Classify` is entirely regexes over visible text, but nothing stripped ANSI
first (`pane.Clean` does, and is only used for AI summarisation). Agents colour
phrases word by word, so a styled `(esc to interrupt)` classified as **unknown
instead of working**. `Classify` now strips both `Pane` and `Previous` — both,
because stripping one would make every sample differ from the last and every
session look busy forever.

Also: the preview panel now says `←/→ pan (154 of 273 cols)` when the session is
wider than the panel. Remote sessions are as wide as the terminal that last
sized them, the viewport already cuts and pans ANSI-aware, but with no hint a
right edge sliced mid-word just looks broken.

### PTY sessions survived serve restarts in theory only ✅
Updating or restarting `aiman serve` killed the agent inside every PTY session,
the one thing the holder design exists to prevent. `setsid` puts a holder in its
own session, so it escapes process-group signals, but cgroup membership is
inherited across `fork` and cannot be shed — and the generated systemd unit left
`KillMode` at its default of `control-group`. Stopping serve therefore SIGTERMed
everything in the unit's cgroup, holders included. Confirmed on a live remote:
the holder's `/proc/<pid>/cgroup` was `…/aiman-serve.service`, identical to
serve's own.

The serve unit now sets `KillMode=process`, so a stop signals only serve.
Verified against real systemd before shipping: the child survives the stop, the
unit still goes inactive, and it restarts cleanly with the leftover running. The
trigger unit keeps the default, which is correct for its transient ssh children.
Start/install/update already write and reload the unit *before* stopping, so the
very update that ships this fix is the first to honour it.

The attach client's half of the same event is fixed too: a lost stream printed
`Error: write /dev/stdout: use of closed network connection` and exited 1.
(`io.Copy(os.Stdout, conn)` uses `os.File.ReadFrom`, which splices, so a failed
read on the *source* is reported as a `PathError` against the write target.) It
now exits cleanly and says the stream ended and how to reattach.

### Detaching from a PTY session reported a failed attach ✅
`ctrl+q` landed the dashboard on its error screen with "Failed to attach to tmux
session: exit status 1". Detaching closes the attach connection from the input
side, and `Relay` returns whichever of its two `io.Copy` directions finishes
first, so the same keypress yields either `nil` or "use of closed network
connection" depending on which goroutine wins. The error path became a non-zero
exit from `aiman pty attach`, which over `ssh -t` reached the UI as exit status
1. The reader now records the deliberate detach and the exit is clean either way.
The message also no longer blames tmux for a session tmux never hosted.

### Built-in PTY runtime as an opt-in session backend ✅
Sessions are no longer tmux-only. `session_backend: pty` on a remote hosts its
sessions in `aiman serve`'s own PTY runtime instead, selectable per session in
the run-target step of the wizard. Sessions are owned by detached *holder*
processes (`internal/ptyhold`) rather than by serve, so they survive a serve
restart and are re-adopted — matching the durability tmux provided. Driveable
from the CLI via `aiman pty list|get|attach|kill`. Unix-only: the runtime needs
`setsid` and `SIGWINCH`, so the Windows build omits it and uses tmux.

**Breaking**: EC2 loop sessions are removed entirely (`feat!`). The
`SessionMode` EC2 path, its wizard screens, and its settings are gone; the
separate *EC2 Provisioning* roadmap item below (spinning up instances to use as
ordinary remotes) is unaffected.

Hardening done while getting this branch green — all of it found by finally
being able to compile and run the package:

- **`internal/ptyhold` had never been committed.** It was imported but absent
  from git, so the branch had never built and its CI failed on Build, Test and
  Lint. Recovered from the remote worktree where it sat untracked.
- **Fork bomb.** `ptyruntime.holderCmd()` fell back to `os.Executable()`, and
  the server tests used the production `NewManager()`, which never sets
  `HolderCmd`. Under `go test` that is the *test binary*, and a Go test binary
  ignores unknown positional args — so re-execing it as `<binary> pty hold <id>`
  silently re-ran the whole suite, which created more sessions, which forked
  more suites. Exponential, not a leak: it reached ~2000 resident processes and
  OOM-killed the dev box. `holderCmd()` now panics under `testing.Testing()`
  instead of defaulting, so the mistake cannot be made silently again.
- **Socket-path limit, not a race.** Unix socket paths are OS-capped (~104
  bytes on macOS) and `t.TempDir()` embeds the test name under a long
  `/var/folders/…` base, so the holder's `term.sock` went over the limit and
  `listen` failed — making failures track *test-name length*. Short-path roots
  fix it, and the same cause explains this repo's long-standing "internal/server
  tests always fail on macOS".
- **Silent startup failures.** The holder runs detached with stdio discarded and
  writes `meta.json` *before* binding its socket, so the manager accepted the
  session as started and any later failure surfaced only as an unexplained
  `exited`. `ptyhold.Run` now records startup failures in the exit file, and
  `Spawn` treats the *socket* as readiness rather than meta alone.
- **Resize readback implemented.** Live resize applied a size that could never
  be read back — nothing populated `SessionInfo.Size` and the contract had no
  size field at all. `Meta.Size` now carries it, set at startup and on every
  applied resize, surfaced by `ptyruntime.Get`.
- Test holders no longer leak (they are detached, so a forgotten `Kill` left one
  running for the whole run and starved the timing-sensitive tests), and the
  spool-rotation test no longer depends on bash's unpredictable echo yield.

### Restart runs in the background; discovery failures can't fake an empty host ✅
Live testing of the revive flow surfaced three problems at once:

- **A revive sat out the full 90 s handoff budget on a session that doesn't
  exist**: `isShellCommand` called `filepath.Base` before its emptiness
  check, and `Base("")` returns `"."` — so the pane probe's empty answer for
  a nonexistent tmux session read as "an agent is running", a handoff prompt
  was injected into nothing, and the restart waited for a file no one could
  ever write. Emptiness is now decided first, so a missing pane skips the
  handoff instantly.
- **Sessions still flickered in and out of the list at startup**: the
  v0.16.2 fix only covered discovery errors that *propagated*, but
  `ScanTmuxSessionDetails` swallowed every error (`return nil, nil`) on the
  incorrect theory that "no tmux server" exits non-zero — the scan script
  pipes `tmux ls` into a `while` loop, so it exits 0 with empty output in
  that case, meaning every swallowed error was a real transport failure
  masquerading as "zero sessions". The batch tmux scan now propagates
  errors; the per-item `ScanTmuxSessions` distinguishes tmux's own
  no-server message from transport failures; and a failed worktree sweep
  now fails the whole `Discover` rather than returning a partial result the
  merge step reads as complete.
- **Restart/revive blocked the whole dashboard on a loading screen**: every
  restart-shaped flow (`s` resume, `S` switch, revive, change-scope,
  post-picker) now runs through `startBackgroundRestart`, reusing the
  background-creation machinery — the session's row shows live progress
  steps in the preview panel while the dashboard stays fully usable, exactly
  like creating a new session.

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
