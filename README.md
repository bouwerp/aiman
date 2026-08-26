# Aiman

**Aiman** is a high-performance terminal UI (TUI) orchestrator built in Go. It manages the lifecycle of remote, agent-assisted coding sessions, turning a JIRA ticket into a ready-to-code environment in seconds.

## 🚀 What It Does

Aiman automates the entire development workflow:

1. **Choose Where It Runs** — A configured remote server from your registry
2. **Select a JIRA Issue** — Your issues in the statuses you actually work in
3. **Generate Branch Name** — Auto-creates git-compatible branch names
4. **Pick a Repository** — Browse your GitHub repos
5. **Choose Subdirectory** — Pick a repo sub-folder (monorepo-friendly)
6. **Scan Agents** — Detect available agents on the remote
7. **Review Summary** — Confirm settings (and override AWS credentials) before creation
8. **Create Session** — Worktree + terminal (tmux, or the built-in PTY runtime) + agent launch + mutagen sync + AWS credentials

Or use **Ad-hoc Sessions** to skip the JIRA/branch/repo steps entirely.

## ✨ Features

### Core Workflow
- **JIRA Integration**: Real-time search with VSCode-style filtering
- **Smart Branch Names**: Auto-sanitizes issue titles for git compatibility
- **Repo & Directory Picker**: Choose repo + subdirectory from the remote
- **Multi-Agent Support**: Scans the remote and offers whichever of these it finds — Claude Code, Antigravity CLI (`agy`), Grok Build CLI, Codex CLI, OpenCode, GitHub Copilot CLI, Cursor (`cursor-agent`), Pi, Ageni
- **Ad-hoc Sessions**: Create quick sessions without a JIRA issue, branch, or repo
- **Quick start (`N`)**: Default remote, agent picker only, generated `q1`/`q2`/… in group `quick`
- **Names and groups**: Every session has a unique display `name` and a `group`; the sidebar is a tree of groups, not a flat list
- **Session Management**: Track active sessions with live pane previews (tmux or PTY)
- **Agent defaults**: Menu → Agent defaults sets per-agent launch model and thinking/reasoning effort (e.g. Claude `sonnet` + `medium`, Grok `4.6` + `medium`)
- **Resume / restart (`s`)**: Save a handoff, then resume with the last-known agent automatically — no picker, no re-asking — including a worktree revived after a tmux crash or reboot. Falls back to the agent picker only when the agent can't be determined. Use `S` to deliberately switch agents instead
- **Agent-exited detection**: A pane that fell back to a bare shell (crashed or never-started agent) shows as a distinct `⚠ agent exited` state instead of blending in with idle
- **Revive Worktree** (Menu → Revive Worktree): finds worktrees under a remote's repo root that aiman has never tracked before (hand-created, or from before this existed) and lists each with the agent(s) that plausibly worked there, detected from git commit trailers — not project files, which are often committed and shared across every worktree of a repo. Zero candidates opens the full agent picker, one revives immediately, two or more show a short pick-list instead of guessing
- **AWS profile allowlist**: Menu → AWS Credentials toggles which local `~/.aws` profiles aiman uses; delete removes the remote profile and the config entry so it does not come back
- **Agent API (`aiman serve`)**: One JSON server per remote; start it from the TUI (Tab → **agent API** → `i`) so in-pane agents can list, create, prompt, and wait on sibling sessions
- **Shared context**: Durable markdown notes per host (`aiman context ls|find|get|put|pack|stats`); session create injects abstracts into `.aiman_context.md`, archive writes a note back. Menu → Shared context shows store size, lookups, and pack usage
- **Mobile TUI (`aiman phone`)**: Termius over Tailscale. Uses the host `tailscale` CLI; the phone still needs the Tailscale app connected first
- **Agent skill**: `aiman --skill` prints a Markdown skill gated on `AIMAN_ENV=1`

### AI Intelligence
- **Session activity detection**: Reports whether an agent is working, waiting on background agents, blocked on a question, or idle, from tmux's own last-output timestamp and the tail of the pane — no model required
- **Brief AI Summary**: Short summary shown in the session list sidebar (per active session)
- **Long AI Summary**: Detailed summary with action items generated at archive time
- **Session Archive**: Compress, AI-summarise, and persist a session snapshot in one step
- **Snapshot Browser**: Browse, search, and preview archived sessions — shows full AI summary and session content head/tail
- **Pane Debug Dump**: In the archive preview, press `d` to write raw and cleaned pane content to `/tmp/` for inspection

### Remote Development
- **SSH Multiplexing**: High-performance connections with ControlMaster
- **Mutagen Sync**: Real-time file sync between local and remote, excluding dependency and build directories by default so a new session starts syncing in seconds rather than minutes
- **Tmux Integration**: Native tmux session management
- **Built-in PTY runtime (opt-in)**: Host sessions in `aiman serve`'s own PTY runtime instead of tmux, per remote or per session. Sessions survive a serve restart and are re-adopted; see [Session backends](#session-backends)

### AWS Credential Delegation
- **Shared AWS credential sync**: Automatically syncs your local `~/.aws` configuration to the remote
- **STS token push**: Fresh temporary credentials are pushed to the remote when requested
- **Per-session overrides**: At session creation, override the AWS profile and region independently per session (e.g. `dev`/`us-east-1` in one session, `prod`/`eu-west-1` in another)
- **Time to expiry**: The credentials manager shows how long each remote profile has left, counting down live
- **Refresh anything, any time**: `shift+R` re-mints and pushes every delegated profile regardless of its current status, from the credentials screen or straight from the dashboard
- **Expiry warning banner**: The dashboard warns when a selected delegated credential is within 15 minutes of expiry (red once expired). Unchecked local profiles are not polled or shown

### User Experience
- **Interactive TUI**: Built with Bubble Tea for a modern terminal UI
- **Real-time Previews**: Live pane capture in the dashboard, for either backend
- **VS Code Integration**: Open synced directories directly in VS Code (`v` key)
- **Health Checks**: Built-in "Doctor" validates all integrations on startup
- **Fuzzy Search**: Find issues, repos, and sessions quickly
- **Filter by Remote**: Show only sessions from a specific remote server (`f` key)
- **Self-update**: `aiman update` downloads and installs the latest release in-place; `aiman downgrade` rolls back to the previous (or a given) release

### Configuration
- **YAML-based Config**: Simple `~/.aiman/config.yaml` configuration
- **SQLite Persistence**: Session state, history, and snapshot tracking
- **Secure Token Storage**: JIRA API tokens stored in config (use `op` or similar for production)

## 🛠 Installation

### Quick Install (recommended)

The installer downloads the correct pre-built binary for your platform, installs it to `~/.local/bin`, and ensures that directory is on your `PATH`. No `sudo` required.

```bash
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash
```

**Options:**

```bash
# Install to a custom directory
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --prefix ~/bin

# Install system-wide (requires sudo)
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --system
```

If no pre-built binary is available for your platform, the installer falls back to building from source (requires Go 1.26+).

**Supported Platforms:**
- macOS (Intel & Apple Silicon)
- Linux (amd64 & arm64)
- Windows (amd64)

### Self-Update

Once installed, keep aiman up to date with:

```bash
aiman update
```

This downloads the latest release binary and replaces the running binary in-place.

### Downgrade / recover a broken install

If the current binary still starts:

```bash
aiman downgrade          # previous stable release
aiman downgrade v0.9.1   # pin a specific tag
```

To capture a debug dump while reproducing a bug:

```bash
aiman --debug
aiman --debug=/tmp/aiman.debug session list
```

The default file is `~/.aiman/debug.log`. The TUI also keeps traces in `/tmp/aiman-debug.log` when `--debug` is not set.

If it will not start, reinstall a known-good tag with the installer (the flag must be on the `bash` side of the pipe):

```bash
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --version v0.9.1
```

### Manual Build

```bash
git clone git@github.com:bouwerp/aiman.git
cd aiman
go build -o aiman ./cmd/aiman
mv aiman ~/.local/bin/
```

## 📋 Prerequisites

### Required
- **GitHub CLI (`gh`)**: Authenticated with `gh auth login`
- **SSH**: Key-based authentication configured for remote servers
- **JIRA API Token**: Generate at [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens)

### Optional
- **Go 1.26+**: Only needed if building from source (not required for pre-built binaries)
- **tmux**: Hosts sessions on the remote. Required unless you opt that remote into the built-in PTY backend instead — see [Session backends](#session-backends)
- **mutagen**: For local/remote file syncing
- **code** (VS Code CLI): For IDE integration
- **AWS CLI**: Required for AWS credential delegation (`aws sts`)

## 🎮 Usage

### Initial Setup

Run the initialization wizard to configure JIRA and remote servers:

```bash
aiman init
```

This will guide you through:
- JIRA URL, Email, and API Token
- Remote server configuration (scan `known_hosts` or manual entry)
- Testing SSH connectivity

Configuration is stored in `~/.aiman/config.yaml`.

### Main Dashboard

Launch the TUI:

```bash
aiman
```

**Keyboard Shortcuts:**

| Key | Action |
|-----|--------|
| `n` | **New Session** — Start the full JIRA-driven workflow wizard |
| `N` | **Quick session** — Default remote, pick an agent, generated name in group `quick` |
| `e` | **Rename** — Session name, or the group if a group header is selected |
| `g` | **Group** — Assign the session to a group, ungrouped, or a new group |
| `enter` / `space` | **Collapse/expand** the selected group |
| `m` | **Admin Menu** — Remotes, **Agent API**, JIRA, snapshots |
| `↑/↓` | Navigate sessions |
| `Enter` | Select item |
| `ESC` / `q` | Quit (asks to confirm). Inside a screen or an active list filter, cancels instead |
| `a` | **Attach** to tmux session (full terminal) |
| `s` | **Resume / restart** — auto-detects the last agent and resumes it, no picker |
| `S` | **Switch agent** — always shows the agent picker |
| `c` | **Change** directory scope for the session |
| `t` | **Tunnels** — Manage per-session local↔remote port forwards |
| `p` | **Copy local path** to clipboard |
| `v` | **Open in VS Code** (local synced directory) |
| `y` | **Copy session output** (visible pane area) to clipboard |
| `Y` | **Copy session output** (full preview) to clipboard |
| `G` / `End` | Jump preview pane to latest output |
| `i` | **Classify Session** — Report whether the agent is working, blocked, or idle (rules + local model, side by side) |
| `I` | **AI Insight** — Generate a brief AI summary of the session |
| `r` | **Refresh** session status |
| `R` | **Refresh AWS credentials** — re-mint and push every delegated profile, whatever its remaining lifetime |
| `f` | **Filter** session list by remote (only active with more than one remote configured) |
| `d` | **Trigger details** for an autonomous session |
| `tab` | Switch between the **sessions** list and the **agent API / daemons** tab |
| `T` | **Take over** an autonomous session (convert it to interactive) |
| `Ctrl+M` | Toggle mouse reporting — off lets the terminal do native text selection |
| `[` / `]`, `shift+↑/↓`, `PgUp` / `PgDn` | Scroll the preview panel |
| `Ctrl+R` / `Ctrl+S` | Aliases for `s` (resume) and `a` (attach) |
| `Ctrl+A` | **Archive Session** — AI-summarise and snapshot the session |
| `Ctrl+Y` | **Recreate Mutagen Sync** for the selected session |
| `Ctrl+K` | **Terminate Session** (with git safety checks) |
| `` ` `` | Toggle debug console |
| `Ctrl+C` | Quit |

### Creating a New Session

1. Press `n` on the dashboard
2. **Choose Where It Runs**: A numbered remote server from your registry.
3. **Choose How to Start**: From a JIRA issue, a new branch, an existing branch, ad-hoc, or
   an autonomous trigger
4. **Select JIRA Issue**: Type to filter your issues in real-time. The list holds issues
   assigned to you in the statuses configured under `integrations.jira.issue_statuses`
   (default: Groomed, Analysis In Progress, Research, Discovery, Dev Ready, In Development,
   Dev Review). Searches are scoped the same way, so a ticket sitting in To Do, Later, or a
   Done state will not appear here.
5. **Confirm Branch Name**: Edit the auto-generated git-compatible branch name
   - Invalid characters are blocked; spaces automatically become dashes
6. **Select Repository**: Pick from your GitHub repos
7. **Select Subdirectory**: Choose a repo subdirectory (or `.` for root)
8. **Agent Selection**: Choose your AI coding assistant (Claude Code, Antigravity CLI, Copilot, OpenCode, Cursor)
9. **Summary**: Review selected issue/branch/repo/dir/agent before creation
   - If AWS credential delegation is configured for the remote, editable **Profile** and **Region** fields appear — pre-filled with remote defaults, overridable per session (Tab cycles between fields)

### Creating an Ad-hoc Session

Skip the JIRA/branch/repo flow and jump straight to agent selection:

1. Press `n` on the dashboard
2. Pick the remote to run on
3. Choose `[4] Ad-hoc — no git repo, no JIRA ticket`
4. Optionally enter a label for the session, or leave blank for auto-generated
5. **Agent Selection**: Choose your AI coding assistant
6. **Summary**: Review and confirm

For an even shorter path, `N` creates a quick session directly on the default remote:
agent picker only, auto-named `q1`/`q2`/… in group `quick`.

Ad-hoc sessions still get their own tmux session, mutagen sync, and AWS credentials.

**Faster path:** `N` on the dashboard. It uses the active (or filtered) remote, skips JIRA/branch/repo, opens the agent picker, and names the session `q1`, `q2`, … in group `quick`. Rename afterwards with `e`.

### Terminating a Session

Press `Ctrl+K` from the dashboard, then confirm with `y`.

Before termination runs, Aiman checks the session worktree and blocks termination when:
- there are uncommitted tracked changes, or
- the current branch has commits not pushed to its upstream (or has no upstream yet).

### Archiving a Session

Press `Ctrl+A` on a selected session. Aiman will:

1. Capture the full tmux pane scrollback
2. Strip ANSI escape sequences and collapse noise (package manager spam, progress bars, timestamps)
3. Preserve user prompts and agent conversation content
4. Send to the AI model for a **long summary** (overview + action items) and a **short summary**
5. Compress the cleaned content (gzip)
6. Show a preview — press `Enter` to save, `ESC` to discard, `d` to dump raw/cleaned content to `/tmp/`

### Browsing Archived Sessions

Access the **Snapshot Browser** via the Admin Menu (`m`) → **Session Snapshots**:

- Left pane: list of archived sessions with short AI summary
- Right pane: full AI summary, git metadata, and a preview of the session head/tail
- `Delete` / `d`: delete the selected snapshot
- `ESC`: close the browser

### Restarting, resuming, and reviving a session

Press `s` on a selected session to resume it. Aiman resolves the last agent that ran
there — from the database, or (if that row was lost) inferred from a hook sidecar
file's transcript path — and restarts it in place with no picker and no re-asking:

1. Asks the current agent (if one is running) for a restart handoff; a failed or
   missing handoff never blocks the restart, the new agent just starts without one
2. Respawns the agent process in the existing tmux pane (`respawn-pane`), or starts a
   fresh tmux session in the **same working directory** if the session itself is gone
   (host reboot, tmux server crash) — the git worktree, branch, and all files are
   always untouched
3. Resumes the agent's own native conversation (`--resume`/`--session`/…) when a prior
   conversation id is known

Press `S` to deliberately switch agents instead — this always shows the agent picker,
even when the last agent is already known. If the agent identity truly can't be
determined (new worktree, no hook data), `s` falls back to the picker too.

A pane whose agent process crashed or never started shows as `⚠ agent exited` in the
session list (distinct from a normal idle agent) — press `s` there to bring it back.

`s`/`S` only work on a session already visible in the dashboard. A worktree aiman has
never tracked before (hand-created, or from before aiman was managing this remote) is
invisible there by design — use **Menu → Revive Worktree** to find and resume those; see
"Revive Worktree" above for how it detects which agent worked in one.



Press `Ctrl+Y` on a selected session to recreate its mutagen sync binding using that session's current remote agent working directory and the canonical local path `~/.aiman/work/<session-id>`.

### Session backends

Every session is hosted by a terminal runtime. `tmux` is the default and needs no
configuration. The alternative is aiman's own PTY runtime, served by `aiman serve` on
the remote — useful where tmux isn't available or wanted.

Opt in per remote in `~/.aiman/config.yaml`:

```yaml
remotes:
  - host: devbox
    user: code
    root: /home/code/repos
    session_backend: pty      # "tmux" (default) or "pty"
```

The run-target step of the new-session wizard always offers `b` to flip the backend for
the session being created, starting from the remote's configured default. So a
tmux-default remote can host a one-off pty session, and a pty-default remote a one-off
tmux session, without editing config. The summary screen shows the backend it will use.

PTY sessions are owned by detached *holder* processes rather than by `aiman serve`
itself, so they survive a serve restart or crash and are re-adopted when it comes
back — the same guarantee tmux gives.

That durability depends on the generated systemd unit setting `KillMode=process`.
A holder is placed in its own session (`setsid`), which is enough to escape
process-group signals, but a process cannot leave the cgroup it inherited — so
under systemd's default `KillMode=control-group`, stopping serve would SIGTERM
every holder along with it. If you manage the unit by hand, keep that setting.

Inspect and drive them directly with:

```bash
aiman pty list
aiman pty get <id>
aiman pty attach <id>     # interactive; detach with ctrl+q, which leaves it running
                          #   (a PTY session has no tmux prefix, so ctrl+b d does
                          #    nothing — the attach banner reminds you of ctrl+q)
aiman pty capture <id>    # read the pane without attaching
aiman pty input <id> --data TEXT | --file PATH
aiman pty create --id <id> --command CMD [--name N] [--dir D] [--env K=V]…
aiman pty kill <id>
aiman pty forget <id>     # drop an exited session's directory
```

`aiman pty hold` is the holder itself and is not meant to be run by hand.

The pane is rendered before it is shown: the spool is a raw byte stream of cursor
addressing and redraws, so previews and activity detection replay it through a terminal
emulator at the session's own size, the way `tmux capture-pane` returns a screen.
Sessions also get `TERM=xterm-256color` and `COLORTERM=truecolor` — `aiman serve` is a
daemon with no tty, so without that the agent inherits no terminal type and emits no
colour.

Requires a Unix remote: the runtime uses `setsid` and `SIGWINCH`, so the Windows build
omits it. Use the tmux backend there.

### Administrative Menu

Press `m` to access, in order:

| Item | What it does |
|---|---|
| **Manage Remote Servers** | Add, scan `known_hosts`, edit, or test SSH connections |
| **Agent API** | Install, start, reload, update or stop `aiman serve` per remote |
| **Provision Remote Server** | Install baseline tooling on a fresh host (gh, agents, node, skills) |
| **Auth Setup Wizard** | Guided auth checks and instructions per remote tool |
| **JIRA Configuration** | URL, email, API token, and which issue statuses the picker offers |
| **Git Configuration** | Which repositories and orgs the repo picker lists |
| **General Settings** | Experimental and general feature flags |
| **Agent defaults** | Per-agent launch model and thinking/reasoning effort |
| **Shared context** | Store size, lookups, and pack usage per remote |
| **AI Settings** | Enable the local model; Ollama host and model choice |
| **Secrets** | Env-var secrets injected into new sessions |
| **AWS Credentials** | Status, lifetime and renewal of delegated profiles; profile allowlist |
| **Session Snapshots** | Browse, search and preview archived sessions |
| **Scheduled Prompts** | Cron-scheduled prompt injection into a set of sessions |
| **Revive Worktree** | Find abandoned worktrees under a remote's repo root and resume the agent that worked there |

Doctor checks are not a menu item: they stream into the dashboard footer at startup,
and `r` re-runs them. If the issue picker comes up empty, read the JIRA line there
first — `Authentication failed` means the credentials are wrong, not the status filter.

### Git Repository Configuration

By default, Aiman shows your personal GitHub repositories. Customize which repos appear in the picker via `~/.aiman/config.yaml`:

```yaml
git:
  include_personal: true     # include your own repos (default: true)
  include_orgs:
    - "mycompany"            # include org repos
  include_patterns:
    - "^mycompany/.*"        # regex — only matching repos (optional)
  exclude_patterns:
    - ".*\.github\.io$"      # regex — exclude matching repos (optional)
```

### Repository Browser

Quickly browse GitHub repositories:

```bash
aiman repos
```

## Agent API (`aiman serve`)

Agents inside a session talk to **one server per remote**, not to the laptop TUI. That process is `aiman serve` (the agent API). The skill (`aiman --skill`) and `aiman session …` are clients of it. tmux (or the built-in PTY runtime) hosts the terminal. Mutagen, JIRA tokens, and the laptop SQLite file stay on the laptop.

`session list` reports the sessions actually running on that host, discovered from tmux
and the PTY runtime — it does not depend on the laptop having told the remote about them.
A session with no display name is addressable by its tmux/PTY session name, and grouped
by its issue key.

There is no HTTP, MCP, or TCP port. The CLI is a thin JSON wrapper over `~/.aiman/aiman.sock`.

That socket path is resolved by the binary that uses it, so an in-session agent
reaches its own host's server (`/home/<user>/.aiman/aiman.sock` on the remote), not the
laptop's. `AIMAN_SOCKET_PATH` overrides it, but only when it points at something that
exists — a path that doesn't is ignored in favour of this host's default, so an agent
that inherited another machine's path keeps working without being restarted.

### Start and manage it from the laptop

Do not run `aiman serve` on your laptop. That starts a local API, not the remote one.

1. Open the dashboard: `aiman`
2. Press **m** (Admin Menu)
3. Select **Agent API**
4. Select the remote
5. Press **i** to install and enable
6. Status should show `RUNNING` and socket `up`

Creating a session also starts it if `~/.aiman/aiman.sock` is missing. `aiman serve --help` prints this path. The Daemons tab (Tab) still lists `agent API` next to `trigger` (the GitHub/cron daemon); the skill talks only to the agent API.

| Key | Action |
|---|---|
| `i` | Install the binary if missing, enable linger, write the systemd `--user` unit, start it |
| `s` | Start or restart |
| `c` | Reload (restart; serve re-reads remote `~/.aiman/config.yaml`) |
| `u` | Reinstall the latest GitHub release, then restart |
| `ctrl+k` | Stop |
| `r` | Probe status, version, socket (serve), and logs |

Linger is enabled *before* `systemctl --user` so the first SSH to a host with no lingering user instance can still install. Scripts set `XDG_RUNTIME_DIR` and the session bus so `systemctl --user` works over SSH. If user systemd is still unavailable, Aiman falls back to `nohup` plus `~/.aiman/serve.pid` (or `trigger.pid`).

Foreground on the remote (debugging):

```bash
aiman serve
# or
systemctl --user status aiman-serve
```

| Item | Path |
|---|---|
| Socket | `~/.aiman/aiman.sock` (`0o600`) |
| Lock | `~/.aiman/aiman.sock.lock` (one instance per host) |
| Log | `~/.aiman/serve.log` (and `journalctl --user -u aiman-serve` when systemd) |
| Unit | `~/.config/systemd/user/aiman-serve.service` |
| Override | `AIMAN_SOCKET_PATH` |

A second instance fails with `already_running`. `aiman serve` is Unix-only (not Windows).

#### Protocol methods

The full surface the socket dispatches. `aiman session`, `aiman pty` and `aiman context`
are thin clients over these; an agent can also speak the JSON directly.

| Group | Methods |
|---|---|
| Sessions | `session.list`, `session.get`, `session.read`, `session.prompt`, `session.wait`, `session.create`, `session.rename`, `session.move` |
| PTY runtime | `pty.list`, `pty.get`, `pty.create`, `pty.input`, `pty.capture`, `pty.kill`, `pty.forget` |
| Shared context | `context.list`, `context.find`, `context.get`, `context.put`, `context.pack`, `context.stats` |

Errors come back with a machine-readable `code` — `server_not_running`, `not_found`,
`invalid_params`, `agent_blocked`, `already_running` — so callers can branch on the
reason rather than parsing prose.

The server uses the remote `~/.aiman/config.yaml` (often sparse) and `~/.aiman/aiman.db`. Session create on the remote does **not** start mutagen or push AWS STS; those remain TUI/laptop concerns. The new tmux session inherits whatever is already in remote `~/.aws`.

### `aiman session` CLI

From inside a pane (or any process on the remote with the socket):

```bash
aiman session list [--group GROUP]
aiman session get <target>
aiman session create --repo owner/repo --branch NAME --agent claude \
    [--name NAME] [--group GROUP] \
    [--dir SUBDIR] [--prompt TEXT] [--issue KEY] [--base BRANCH] [--existing]
aiman session create --quick --agent claude [--name NAME]
aiman session rename <target> NEW-NAME
aiman session move <target> --group GROUP
aiman session prompt <target> TEXT [--wait] [--until STATE] [--timeout 120s] [--force]
aiman session wait <target> [--until idle|working|waiting_input|waiting_background|blocked] [--timeout 120s]
aiman session read <target> [--lines 120]
aiman session report-agent-session --from-stdin
```

### `aiman phone`

Run this on the host you will SSH into from an iPhone (Termius). It uses the host `tailscale` CLI; it does not bundle Tailscale. The phone still needs the Tailscale app connected first.

```bash
aiman phone
aiman phone --up      # tailscale up if this host is not on the tailnet
aiman phone --json
```

Prints MagicDNS name, `100.x` IPv4, SSH user, and Termius fields.

Bare `aiman` starts the TUI. With `AIMAN_ENV=1` or no TTY, bare `aiman` is refused: use `aiman session …`.

`<target>` is resolved in this order: unique `name`, `group/name`, UUID (`id`), tmux session name.

`--quick` is ad-hoc, group `quick`, generated `q1`/`q2`/…, current host. `--agent` is required. `--wait` on prompt sends then waits until the first of `idle`, `waiting_input`, or `errored`. Default timeout is 120s; `--timeout 0` means no limit. `--until blocked` is an alias for `waiting_input`. Prompting a session that is already waiting for input fails with `agent_blocked` unless `--force`.

Rename changes the display name only. It does not rename tmux or the git worktree.

Stdout is indented JSON. Server errors are JSON on stderr, exit 1. Usage errors exit 2.

```json
{
  "type": "session_list",
  "sessions": [
    {
      "id": "uuid",
      "name": "impl",
      "group": "WTB-1925",
      "issue_key": "PROJ-123",
      "branch": "proj-123-fix-auth",
      "repo_name": "owner/repo",
      "tmux_session": "proj-123-fix-auth",
      "worktree_path": "/home/dev/src/repo@proj-123-fix-auth",
      "working_directory": "/home/dev/src/repo@proj-123-fix-auth",
      "agent_name": "claude",
      "status": "ACTIVE",
      "state": "working",
      "state_confidence": "high",
      "self": true
    }
  ]
}
```

`self` is true when `id` equals the caller's `AIMAN_ID`. `state` is `idle`, `working`, `waiting_input`, `waiting_background`, `errored`, or `unknown`.

| Code | Meaning |
|---|---|
| `invalid_params` | Missing or bad arguments |
| `not_found` | No session matches `<target>` |
| `name_taken` | Display name already in use on this host |
| `server_not_running` | Socket missing or `aiman serve` is down |
| `already_running` | Another `aiman serve` holds the lock |
| `agent_blocked` | Target is `waiting_input` and `--force` was not set |
| `timeout` | `--wait` / `wait` hit `--timeout` |
| `create_failed` | Worktree/tmux create failed |
| `jira_unavailable` | `--issue` given but JIRA is not configured on the remote |

### `aiman context` CLI

Shared notes on this host (`~/.aiman/context/`). Serve is used when the socket is up; otherwise the same commands read and write the files directly.

```bash
aiman context ls [--group GROUP | --repo owner/repo] [--limit N]
aiman context find QUERY [--group GROUP | --repo owner/repo]
aiman context get ID
aiman context put --title TITLE [--abstract TEXT] [--group GROUP | --repo owner/repo] [--body-file FILE]
aiman context pack [--group GROUP] [--repo owner/repo] [--limit N]
aiman context import [--agent all|claude,grok,codex,agy] [--group GROUP] [--repo owner/repo] [--dry-run]
```

`ls`/`find` return abstracts. `get` returns the body. Create and restart pack abstracts into `.aiman_context.md`. Archiving a session writes one note from the snapshot summary.

`import` copies this host's agent memories into the store: Claude auto-memory under `~/.claude/projects/*/memory/`, Grok `~/.grok/memory/**/MEMORY.md`, Codex `~/.codex/memories/*.md`, and agy `walkthrough.md` files. Project notes go under the git origin slug when it can be inferred. Re-running overwrites the same ids. `--group` / `--repo` pin every imported note to that namespace.

### Names and groups

Every session has a **name** (unique per host; up to 120 characters, no control characters, no leading or trailing spaces) and a **group** (work bucket: issue key, repo short name, `quick`, or `ungrouped`). The dashboard sidebar is a tree of groups. Each header shows the session count (`· N`).

| Key | On a group header | On a session |
|---|---|---|
| `enter` / `space` | Collapse or expand | (list select) |
| `e` | Rename the group (all members) | Rename the session |
| `g` | — | Assign to an existing group, **ungrouped**, or a **new** group |

Empty group is stored as `ungrouped` so discovery cannot blank it.

| | Name | Group |
|---|---|---|
| TUI `n` (full wizard) | `impl` if free, else from branch | issue key, else repo short name, else `ungrouped` |
| TUI `N` (quick) | `q1`, `q2`, … | `quick` |
| CLI `--quick` | same as `N` | `quick` |
| CLI `--name` / `--group` | as given, if unique | as given |

### Environment (inside a pane)

`CreateSession` injects these into the session environment (tmux or PTY) (not overridable by stored secrets):

| Variable | Value |
|---|---|
| `AIMAN_ENV` | `1` (skill/CLI gate) |
| `AIMAN_ID` / `AIMAN_SESSION_ID` | Session UUID |
| `AIMAN_SESSION_NAME` | Display name |
| `AIMAN_GROUP` | Group |
| `AIMAN_SOCKET_PATH` | `~/.aiman/aiman.sock` |
| `AIMAN_BIN_PATH` | Path of the `aiman` binary |

### Agent skill

Coding agents on the remote should load the bundled skill, not invent the CLI:

```bash
aiman --skill
```

That prints the Markdown skill (`internal/aimanskill/SKILL.md`, also under `skills/aiman/`). The skill is gated on `AIMAN_ENV=1`. If that is unset, the agent is not inside an Aiman session and must stop.

`aiman serve` on start (and on restart) installs or updates the skill in each agent's user skill dir under `$HOME` (`.claude/skills/aiman`, `.cursor/skills/aiman`, and the other known loaders) and in every session worktree it knows about. Missing files are created; stale copies are replaced with the copy embedded in the binary. Session create still writes the worktree copy so a new session does not wait for a serve restart.

It also registers **native-session hooks** in each installed agent's config. Every hooked agent reports vendor conversation id, `SessionEnd`, and (where the runtime has it) `Notification` `idle_prompt`. OpenCode and Pi also report lifecycle `idle` / `working` / `blocked` with an optional block reason and session title. `aiman session wait` / `prompt --wait` prefer a fresh hook report over tmux screen classification. Restart uses the native id (`claude --resume`, `codex resume`, …). Ageni is not hooked. Missing agent config directories are left alone.

Typical helper spawn from an in-pane agent:

```bash
test "${AIMAN_ENV:-}" = 1
aiman session list
aiman session create --repo owner/repo --branch BRANCH --agent claude \
  --name reviewer --group "$AIMAN_GROUP"
aiman session prompt reviewer "Review the current diff." --wait --timeout 120s
aiman session read reviewer --lines 120
```

Do not run bare `aiman` from a pane (TUI). Do not run `aiman serve` to stop the server. Prefer names over UUIDs. Put helpers in `"$AIMAN_GROUP"`.

## 📁 Configuration

All data is stored in `~/.aiman/`:

```
~/.aiman/
├── config.yaml          # Main configuration
├── aiman.db             # SQLite database (sessions + snapshots)
├── aiman.log            # TUI log — background goroutines write here, never to the screen
├── aiman.sock           # Remote: `aiman serve` Unix socket (`0o600`)
├── aiman.sock.lock      # Remote: serve singleton lock
├── serve.log            # Remote: serve log
├── serve.pid            # Remote: serve pid, when running under nohup instead of systemd
├── trigger.pid          # Remote: aiman-trigger pid, same fallback
├── debug.log            # `aiman --debug` dump (optional; the TUI otherwise
│                        #   traces to /tmp/aiman-debug.log)
├── context/             # Shared context notes (markdown + YAML frontmatter)
├── skills/              # Skills synced into agents and worktrees
├── hooks/               # Agent hook reporter script installed per host
├── native-sessions/     # Per-session vendor conversation ids reported by hooks
├── pty/                 # Remote: one directory per built-in PTY session
├── sockets/             # SSH ControlMaster sockets (hashed filenames)
└── work/                # Local mutagen sync roots — one subdirectory per session ID
```

### Example Config

```yaml
integrations:
  jira:
    url: "https://company.atlassian.net"
    email: "you@company.com"
    api_token: "ATATT..."
    # Statuses the issue picker offers. Omit to use the built-in defaults:
    # Groomed, Analysis In Progress, Research, Discovery, Dev Ready,
    # In Development, Dev Review
    issue_statuses:
      - "Dev Ready"
      - "In Development"
      - "Dev Review"
    # Optional: status to move an issue to when a session starts on it.
    transition_status: "In Development"

git:
  include_personal: true
  include_orgs:
    - "mycompany"

features:
  # Classify what each session is doing (busy / input / idle / agent exited)
  # and show it in the sidebar.
  input_prompt_detection: true

skills:
  # Skills synced into each agent's config and into new worktrees.
  repo: ""                       # optional git repo to source skills from
  path: ~/.aiman/skills          # local skill directory (this is the default)

# Per-agent launch defaults, keyed by the agent name as aiman reports it.
# Also editable in Menu → Agent defaults.
agent_defaults:
  "Claude Code":
    model: sonnet
    effort: medium
  "Grok Build CLI":
    model: "4.6"
    effort: medium

ai:
  enabled: true
  # Where Ollama listens. Defaults to http://localhost:11434.
  ollama_host: http://localhost:11434
  # Model used for summaries. Defaults to qwen3:4b.
  model: qwen3:4b
  # Model used to classify session activity. Defaults to `model`.
  # Smaller models were measured and rejected — see "Session activity" below.
  classify_model: qwen3:4b

aws:
  # Pre-fills the Profile and Region fields on the session summary screen so
  # they don't have to be retyped for every session. Still overridable there.
  default_profile: dev
  default_region: eu-west-1
  # Which local ~/.aws profiles aiman may use, managed from
  # Menu → AWS Credentials. Omit the key to allow every profile; an explicit
  # empty list allows none.
  include_profiles:
    - dev

sync:
  # Extra paths to exclude from the local mirror, on top of the built-in set.
  # Prefix with "!" to mirror a built-in exclusion after all (e.g. "!dist").
  ignore:
    - "tmp"
  # Set false to mirror everything, including node_modules and build output.
  use_default_ignores: true

remotes:
  - name: devbox
    host: devbox.company.com
    user: developer
    root: /home/developer/repos
    # Terminal runtime for sessions on this remote: "tmux" (default) or "pty".
    # Only a default — the run-target screen can override it per session.
    session_backend: tmux
    aws_delegation:
      profile: default                         # name of the profile written on the remote
      source_profile: my-local-aws-profile   # local ~/.aws profile with long-lived creds
      role_name: TemporaryDelegatedRole        # IAM role to assume on the remote
      account_id: "123456789012"               # 12-digit AWS account ID
      region: us-east-1                        # default region written to remote profile
      sync_credentials: true                   # push fresh STS tokens before each session
      duration_seconds: 3600                   # credential lifetime (900–43200)
      managed_role: false                      # create the IAM role if it does not exist
      regions:                                 # restrict creds via aws:RequestedRegion
        - us-east-1
      session_policy: ""                       # optional inline JSON IAM policy
    # For more than one delegated profile on the same remote, use the plural
    # form instead of (or alongside) aws_delegation:
    # aws_delegations:
    #   - profile: prod
    #     source_profile: my-prod-profile
    #     account_id: "210987654321"

active_remote: devbox
```

### AWS Credential Delegation

When `sync_credentials: true`, each new session on that remote gets:

1. Fresh STS tokens pushed into the remote `~/.aws/credentials`
2. The remote `~/.aws/config` updated for the managed profile/default region
3. Only region env vars injected into tmux when needed (`AWS_REGION` / `AWS_DEFAULT_REGION`)

#### Setting the default profile

Without configuration, the summary screen starts from the delegation's own
`source_profile` and `region`. Set a default so it doesn't have to be retyped:

```yaml
aws:
  default_profile: dev          # applies to every remote
  default_region: eu-west-1

remotes:
  - name: devbox
    aws_default_profile: prod   # this remote only, overrides the global value
    aws_default_region: us-east-1
```

Precedence, most specific first: what you type on the summary screen, then the
remote's `aws_default_profile` / `aws_default_region`, then the global `aws:`
block, then the delegation's `source_profile` / `region`. Profile and region
resolve independently, so setting only one leaves the other inheriting.

**Per-session overrides** are available in the session creation summary screen — edit the **Profile** and **Region** fields to override the remote defaults for just that session:

```
> Profile:  [dev                                    ]   ← tab to edit
  Region:   [eu-west-1                              ]
```

The profile and region can differ per session; all other settings (role, account, session policy, duration) inherit from the remote config.

### Session activity

The dashboard reports whether each session is **working**, **needs input**, or
**idle**. This is decided without a model, from two cheap signals:

- `#{session_activity}` — tmux's own timestamp of when a session last produced
  output. One call answers for every session, with no pane capture.
- The tail of the pane — an advancing elapsed timer means work in progress, a
  rendered choice list means the agent is blocked, and an agent sitting at its
  own input box means the turn is finished.

Only the last few lines are examined. Scanning the whole pane is what made the
previous detector unreliable: any occurrence of "confirm" or "thinking" anywhere
in scrollback decided the answer, so an agent working on a task it had once
asked about was reported as blocked.

Press `i` on a session to run both the rules and the local model and compare
them:

```
rules: working (high) — elapsed timer in status line  =  model(qwen3:4b): working in 601ms
```

`=` means they agree, `≠` that they do not. Every probe is written to
`/tmp/aiman-debug.log`.

Against seventeen live sessions the rules resolved every one at high confidence,
so the model is not consulted automatically. On panes captured from those
sessions `qwen3:4b` scored 3/3 at ~600 ms while `qwen3:1.7b` managed 1/3 at
~210 ms — small models read an agent idling at its own input box as working — so
`classify_model` defaults to the same model used for summaries.

### What gets synced

The local mirror at `~/.aiman/work/<session-id>` excludes dependency and build
directories by default: `node_modules`, `target`, `dist`, `build`, `out`,
`.venv`, `__pycache__`, `.next`, `.turbo`, `.gradle`, `.terraform`, `coverage`
and similar. These are large, regenerable, and rebuilt on the remote anyway, so
mirroring them only costs transfer time. On a 3 MB/s link a worktree carrying
`node_modules` took several minutes to reach a usable state; excluding them
brings that down to seconds.

`.git` is deliberately still synced, so the mirror stays a working git checkout
for the VS Code handoff.

Tune it with the `sync:` block shown in the example config above. Ignored paths
are never propagated in either direction, so excluding a directory leaves the
remote copy untouched rather than deleting it.

Legacy `~/.aiman/aws/...` session files from older releases are cleaned up automatically when encountered, but current sync flows operate on `~/.aws/{credentials,config}` directly.

A session's profile is validated before it is exported: legacy per-session `aiman-<id>` profiles (written before v0.8.11) and profiles missing from `~/.aws` are dropped, so the session uses the default credential chain instead of an `AWS_PROFILE` that cannot resolve. Stored copies of those names are cleared when aiman opens its database; to do it on demand and see what was removed:

```bash
aiman clear-aws-profiles
```

Affected sessions need a restart to pick up the change.

#### Expiry tracking and refresh

Every push records the STS expiry in the remote credentials file as `x_security_token_expires` (a key the AWS CLI ignores), so the credentials manager can show an **Expires in** column without calling STS:

```
  Status        Host                        Local profile   Remote profile   Lifetime  Expires in
  ✓ Valid       ubuntu@worker.example.com   prod            default          12h       9h13m
  ✓ Valid       ubuntu@worker.example.com   dev             dev              1h        36m
  ✗ Expired     ec2-user@build.example.com  lab             lab              3h        expired
  ✓ Valid       ec2-user@build.example.com  —               aiman-58f485ff   —         ~2h59m
```

A `~` marks an estimate derived from the credentials file's age plus the configured `duration_seconds`, used for profiles pushed before aiman recorded expiry. Refreshing replaces it with the exact time.

**Lifetime** is the configured `duration_seconds` for the profile, editable in place with `t` (900–43200 seconds; empty means the 12-hour default). The new value applies the next time the profile is renewed — saving it does not re-mint credentials on its own. Profiles with no local delegation config show `—` and cannot be edited, since aiman has nothing to mint from.

`shift+R` refreshes **all** delegated profiles regardless of status, from either the credentials screen or the main dashboard. Profiles on the same host are refreshed sequentially, since they share one `~/.aws/credentials`. The dashboard shows an amber banner when anything is within 15 minutes of expiry, turning red once a credential has actually expired.

Refreshes and status checks run in the background, so you can leave the credentials page while they finish:

- A blue dashboard banner reports `Refreshing AWS credentials… (continues in the background)` while any refresh is in flight, wherever you started it.
- Returning to the page while work is outstanding shows what is still running (`⟳ 2 refresh(es) and 1 check(s) in flight`) with per-row `⟳ Renewing` / `· Checking` status, instead of restarting the scan and losing it.
- A refresh that completes while you are on another screen raises a toast, including a failure count if any profile did not renew.

## 🏗 Architecture

Aiman follows **Clean Architecture** principles:

```
┌──────────────────────────────────────────────────────────┐
│  UI (Bubble Tea)                internal/ui              │
│  - Dashboard, pickers, wizards, admin screens            │
├──────────────────────────────────────────────────────────┤
│  Use Cases                      internal/usecase         │
│  - Doctor, session discovery, FlowManager (create)        │
│  - Restart/revive handoff, SnapshotManager, context       │
├──────────────────────────────────────────────────────────┤
│  Domain                         internal/domain          │
│  - Session, Issue, Repo, Snapshot, agent state           │
├──────────────────────────────────────────────────────────┤
│  Infrastructure                 internal/infra/*         │
│  - jira, git, ssh, sqlite, mutagen, agent (scan),        │
│    ai (Ollama), awsdelegation, config, local, remotesvc, │
│    skills, tailscale                                     │
└──────────────────────────────────────────────────────────┘
```

Two processes run *on the remote*, both driven from the laptop TUI but
independent of it once started:

```
laptop                          remote host
──────                          ───────────
aiman (TUI) ──ssh──────────────► tmux ──► agent
            │                    │
            │                    └─ or ─► aiman serve ──► ptyhold (holder) ──► agent
            └──ssh──────────────► aiman-trigger (scheduled / GitHub-driven runs)
```

The laptop owns mutagen, JIRA credentials and the authoritative SQLite database.
`aiman serve` answers the agent API over a Unix socket and, when the PTY backend
is used, owns the terminals — via detached holder processes, so sessions outlive
a serve restart.

### Key Components

- **`jira.Provider`**: JIRA Cloud API v3 integration
- **`GitSlugger`**: Branch name sanitization
- **`ssh.Manager`**: ControlMaster multiplexing with per-call 30s timeout and automatic retry/socket reset, plus batched whole-remote discovery scans
- **`mutagen.Engine`**: File synchronization
- **Worktree and terminal lifecycle**: not single types — worktree setup lives in `internal/infra/git`, while session start/stop/restart is coordinated in `internal/usecase` (`flow_manager.go`, `session_terminal.go`, `restart_handoff.go`) and driven from `internal/ui`
- **`SkillEngine`**: Agent configuration injection
- **`aiman serve`**: Headless Unix-socket JSON server on the remote (`internal/server`)
- **`ptyruntime` / `ptyhold`**: The built-in PTY backend. `ptyruntime` is a thin client over a file-and-socket contract; `ptyhold` is the detached holder process that actually owns a terminal, which is what lets a session survive a serve restart
- **`pane`**: Classifies what a session is doing (working, blocked on a question, waiting on background agents, idle, agent exited) from the tail of the pane plus tmux's own last-output timestamp — no model required
- **`agenthook`**: Installs per-vendor hooks so agents report their own lifecycle (session id, title, idle/blocked, session end) back to aiman, and infers which agent worked in a directory when reviving one
- **`contextstore`**: The shared markdown note store behind `aiman context`
- **`remotesvc`**: Installs and supervises the two remote daemons (`aiman serve`, `aiman-trigger`) over systemd `--user`, falling back to `nohup`
- **Agent skill**: `aiman --skill` / `internal/aimanskill/SKILL.md`
- **`SnapshotManager`**: Session archiving (capture → clean → compress → AI → persist)
- **`IntelligenceProvider`**: AI summarisation via Ollama (local LLM)
- **`AWSDelegation`**: Session-scoped AWS credential push and cleanup

> For a deep dive into implementation details, architectural decisions, and known gotchas relevant to contributors and AI agents, see [AGENTS.md](AGENTS.md).

## 🔄 Development Workflow

```
┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│  JIRA    │───▶│  Branch  │───▶│  Repo    │───▶│  Connect │
│  Issue   │    │  Name    │    │  Select  │    │   SSH    │
└──────────┘    └──────────┘    └──────────┘    └──────────┘
                                                        │
┌──────────┐    ┌──────────┐    ┌──────────┐           │
│   Sync   │◀───│  Launch  │◀───│  Tmux    │◀──────────┘
│ Mutagen  │    │  Agent   │    │ Session  │
└──────────┘    └──────────┘    └──────────┘
```

## 🚧 Roadmap

- [x] JIRA issue search with filtering
- [x] Git branch name sanitization
- [x] SSH multiplexing
- [x] Tmux session management
- [x] Real-time pane previews
- [x] VS Code integration
- [x] SQLite persistence for sessions
- [x] JIRA-driven initial prompt injection (auto-generates `.aiman_task.md` and seeds agent with task context)
- [x] Skill injection system
- [x] Claude Code integration
- [x] Antigravity CLI integration
- [x] GitHub Copilot CLI support
- [x] OpenCode integration
- [x] Cursor integration
- [x] Ad-hoc sessions (no JIRA issue required)
- [x] AWS credential delegation to remotes (`~/.aws`-based sync, per-session overrides)
- [x] Session tunnel management (local port forwarding)
- [x] AI session summaries (brief + long) with action items
- [x] Session archiving and snapshot browser
- [x] Self-update (`aiman update`) and downgrade (`aiman downgrade [tag]`)
- [x] Autonomous trigger daemon (`aiman-trigger`) released per platform and installable onto a remote from the Daemons tab
- [x] Remote `aiman serve` + JSON `aiman session` CLI (list/get/create/prompt/wait/read/rename/move)
- [x] `aiman serve` / `aiman-trigger` as systemd `--user` services (linger), install/monitor/restart/update from the Daemons tab
- [x] Session names and groups, TUI grouped sidebar, quick start (`N`) and rename (`e`)
- [x] Bundled agent skill (`aiman --skill`), gated on `AIMAN_ENV=1`
- [ ] Git intelligence panel
- [ ] MOSH support

## 🔧 Development

### Prerequisites

- Go 1.26 or later
- Make (optional, but recommended)
- golangci-lint (for linting)

### Building from Source

```bash
git clone https://github.com/bouwerp/aiman.git
cd aiman

# Build the binary
make build

# Or use go directly
go build -o aiman ./cmd/aiman
```

### Running Tests

```bash
# Run all tests with coverage
make test

# Run tests for a specific package
go test -v ./internal/domain
```

### Linting

```bash
# Install golangci-lint (if not already installed)
brew install golangci-lint

# Run the linter
make lint
```

### Development Workflow

```bash
# Format code
make fmt

# Run all CI checks locally (format + vet + test + lint)
make ci

# Clean build artifacts
make clean
```

### CI/CD Pipeline

Aiman uses GitHub Actions for continuous integration and releases:

#### Pull Request Checks
Every PR automatically runs:
- **Tests** with race detection and coverage reporting
- **Linting** using golangci-lint
- **Build verification** across platforms

#### Releases
To create a new release:

1. Tag the commit with a semantic version:
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

2. GitHub Actions automatically:
   - Builds binaries for macOS (Intel & Apple Silicon), Linux (amd64 & arm64), and Windows (amd64)
   - Creates a GitHub release with changelog
   - Attaches all binaries with SHA256 checksums

## 🤝 Contributing

Contributions are welcome! Please follow these steps:

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/amazing-feature`
3. Make your changes with tests (all code changes must include tests)
4. Run the CI checks locally: `make ci`
5. Commit your changes: `git commit -m 'Add amazing feature'`
6. Push to the branch: `git push origin feature/amazing-feature`
7. Open a Pull Request

### Contribution Guidelines

- **All code changes must include unit tests**
- Code must pass `make ci` before submission
- Follow Go best practices and idioms
- Keep commits atomic and messages descriptive
- Update documentation for user-facing changes

## 📄 License

MIT License — see LICENSE file for details

## 🙏 Acknowledgments

- Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) by Charm
- Inspired by [Claude Code](https://claude.ai/code) and other agentic tools
- Uses [Mutagen](https://mutagen.io/) for file synchronization

---

*Built with ❤️ in Go by Pieter Bouwer*
