# Aiman

**Aiman** is a high-performance terminal UI (TUI) orchestrator built in Go. It manages the lifecycle of remote, agent-assisted coding sessions, turning a JIRA ticket into a ready-to-code environment in seconds.

## 🚀 What It Does

Aiman automates the entire development workflow:

1. **Select a JIRA Issue** — Search and filter your assigned issues
2. **Generate Branch Name** — Auto-creates git-compatible branch names
3. **Pick a Repository** — Browse your GitHub repos
4. **Choose Subdirectory** — Pick a repo sub-folder (monorepo-friendly)
5. **Scan Agents** — Detect available agents on the remote
6. **Review Summary** — Confirm settings (and override AWS credentials) before creation
7. **Create Session** — Worktree + tmux + agent launch + mutagen sync + AWS credentials

Or use **Ad-hoc Sessions** to skip the JIRA/branch/repo steps entirely.

## ✨ Features

### Core Workflow
- **JIRA Integration**: Real-time search with VSCode-style filtering
- **Smart Branch Names**: Auto-sanitizes issue titles for git compatibility
- **Repo & Directory Picker**: Choose repo + subdirectory from the remote
- **Multi-Agent Support**: Scan and select Claude Code, Gemini CLI, GitHub Copilot, OpenCode, or Cursor
- **Ad-hoc Sessions**: Create quick sessions without a JIRA issue, branch, or repo
- **Session Management**: Track active sessions with live tmux pane previews

### AI Intelligence
- **Brief AI Summary**: Short summary shown in the session list sidebar (per active session)
- **Long AI Summary**: Detailed summary with action items generated at archive time
- **Session Archive**: Compress, AI-summarise, and persist a session snapshot in one step
- **Snapshot Browser**: Browse, search, and preview archived sessions — shows full AI summary and session content head/tail
- **Pane Debug Dump**: In the archive preview, press `d` to write raw and cleaned pane content to `/tmp/` for inspection

### Remote Development
- **SSH Multiplexing**: High-performance connections with ControlMaster
- **Mutagen Sync**: Real-time file sync between local and remote
- **Tmux Integration**: Native tmux session management

### AWS Credential Delegation
- **Session-scoped AWS profiles**: Each session gets a unique AWS profile (`aiman-<id>`) on the remote, isolated from other sessions
- **STS token push**: Fresh temporary credentials are pushed to the remote before the agent starts
- **Per-session overrides**: At session creation, override the AWS profile and region independently per session (e.g. `dev`/`us-east-1` in one session, `prod`/`eu-west-1` in another)
- **Automatic cleanup**: AWS profile removed from the remote when the session is terminated

### User Experience
- **Interactive TUI**: Built with Bubble Tea for a modern terminal UI
- **Real-time Previews**: Live tmux pane capture in the dashboard
- **VS Code Integration**: Open synced directories directly in VS Code (`v` key)
- **Health Checks**: Built-in "Doctor" validates all integrations on startup
- **Fuzzy Search**: Find issues, repos, and sessions quickly
- **Filter by Remote**: Show only sessions from a specific remote server (`f` key)
- **Self-update**: `aiman update` downloads and installs the latest release in-place

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
- **tmux**: For session management on remote servers
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
| `m` | **Admin Menu** — Configure remotes, JIRA, browse snapshots |
| `↑/↓` | Navigate sessions |
| `Enter` | Select item |
| `ESC` | Go back / Cancel |
| `a` | **Attach** to tmux session (full terminal) |
| `s` | **Restart** session (re-launch agent in existing tmux) |
| `c` | **Change** directory scope for the session |
| `t` | **Tunnels** — Manage per-session local↔remote port forwards |
| `p` | **Copy local path** to clipboard |
| `v` | **Open in VS Code** (local synced directory) |
| `y` | **Copy session output** (visible pane area) to clipboard |
| `Y` | **Copy session output** (full preview) to clipboard |
| `G` / `End` | Jump preview pane to latest output |
| `i` | **AI Insight** — Generate a brief AI summary of the session |
| `r` | **Refresh** session status |
| `f` | **Filter** session list by remote |
| `Ctrl+A` | **Archive Session** — AI-summarise and snapshot the session |
| `Ctrl+Y` | **Recreate Mutagen Sync** for the selected session |
| `Ctrl+K` | **Terminate Session** (with git safety checks) |
| `` ` `` | Toggle debug console |
| `Ctrl+C` | Quit |

### Creating a New Session

1. Press `n` on the dashboard
2. **Select JIRA Issue**: Type to filter your issues in real-time
3. **Confirm Branch Name**: Edit the auto-generated git-compatible branch name
   - Invalid characters are blocked; spaces automatically become dashes
4. **Select Repository**: Pick from your GitHub repos
5. **Select Subdirectory**: Choose a repo subdirectory (or `.` for root)
6. **Agent Selection**: Choose your AI coding assistant (Claude Code, Gemini CLI, Copilot, OpenCode, Cursor)
7. **Summary**: Review selected issue/branch/repo/dir/agent before creation
   - If AWS credential delegation is configured for the remote, editable **Profile** and **Region** fields appear — pre-filled with remote defaults, overridable per session (Tab cycles between fields)

### Creating an Ad-hoc Session

Skip the JIRA/branch/repo flow and jump straight to agent selection:

1. Press `n` on the dashboard
2. When prompted for a JIRA issue, press `Tab` to switch to ad-hoc mode
3. Optionally enter a label for the session, or leave blank for auto-generated
4. **Agent Selection**: Choose your AI coding assistant
5. **Summary**: Review and confirm

Ad-hoc sessions still get their own tmux session, mutagen sync, and AWS credentials.

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

### Restarting a Session

Press `s` on a selected session to restart it. You will be prompted to choose an agent. Aiman will:

1. Terminate any existing mutagen syncs for the session
2. Kill the existing tmux session
3. Start a fresh tmux session in the **same working directory** (the git worktree is preserved)
4. Launch the newly selected agent
5. Re-establish mutagen sync

The git worktree, branch, and all files are untouched — only the tmux process and agent are replaced.



Press `Ctrl+Y` on a selected session to recreate its mutagen sync binding using that session's current remote agent working directory and the canonical local path `~/.aiman/work/<session-name>`.

### Administrative Menu

Press `m` to access:
- **Manage Remote Servers**: Add, scan, or test SSH connections
- **JIRA Configuration**: Update credentials
- **Health Checks**: Re-run doctor checks
- **Session Snapshots**: Open the archive browser

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

## 📁 Configuration

All data is stored in `~/.aiman/`:

```
~/.aiman/
├── config.yaml          # Main configuration
├── aiman.db             # SQLite database (sessions + snapshots)
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

git:
  include_personal: true
  include_orgs:
    - "mycompany"

remotes:
  - name: devbox
    host: devbox.company.com
    user: developer
    root: /home/developer/repos
    aws_delegation:
      source_profile: my-local-aws-profile   # local ~/.aws profile with long-lived creds
      role_name: TemporaryDelegatedRole        # IAM role to assume on the remote
      account_id: "123456789012"               # 12-digit AWS account ID
      region: us-east-1                        # default region written to remote profile
      sync_credentials: true                   # push fresh STS tokens before each session
      duration_seconds: 3600                   # credential lifetime (900–43200)

active_remote: devbox
```

### AWS Credential Delegation

When `sync_credentials: true`, each new session on that remote gets:

1. A unique AWS profile `aiman-<session-id>` on the remote
2. Fresh STS tokens pushed before the agent starts
3. `AWS_PROFILE=aiman-<id>` injected into the tmux session environment

**Per-session overrides** are available in the session creation summary screen — edit the **Profile** and **Region** fields to override the remote defaults for just that session:

```
> Profile:  [dev                                    ]   ← tab to edit
  Region:   [eu-west-1                              ]
```

The profile and region can differ per session; all other settings (role, account, session policy, duration) inherit from the remote config.

AWS profiles are automatically removed from the remote when a session is terminated.

## 🏗 Architecture

Aiman follows **Clean Architecture** principles:

```
┌─────────────────────────────────────────┐
│  UI (Bubble Tea)                        │
│  - Dashboard, Pickers, Inputs           │
├─────────────────────────────────────────┤
│  Use Cases                              │
│  - Doctor, Session Discovery, Flow      │
│  - SnapshotManager, IntelligenceLayer   │
├─────────────────────────────────────────┤
│  Domain                                 │
│  - Session, Issue, Repository, Snapshot │
├─────────────────────────────────────────┤
│  Infrastructure                         │
│  - JIRA, Git, SSH, SQLite, Mutagen      │
│  - AI (Ollama), AWS Delegation          │
└─────────────────────────────────────────┘
```

### Key Components

- **`JiraProvider`**: JIRA Cloud API v3 integration
- **`GitSlugger`**: Branch name sanitization
- **`SSHManager`**: ControlMaster multiplexing with per-call 30s timeout and automatic retry/socket reset
- **`WorktreeManager`**: Git worktree operations
- **`MutagenBridge`**: File synchronization
- **`TmuxManager`**: Session lifecycle management
- **`SkillEngine`**: Agent configuration injection
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
- [x] Gemini CLI integration
- [x] GitHub Copilot CLI support
- [x] OpenCode integration
- [x] Cursor integration
- [x] Ad-hoc sessions (no JIRA issue required)
- [x] AWS credential delegation to remotes (session-scoped, per-session overrides)
- [x] Session tunnel management (local port forwarding)
- [x] AI session summaries (brief + long) with action items
- [x] Session archiving and snapshot browser
- [x] Self-update (`aiman update`)
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

