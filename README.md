# Aiman

**Aiman** is a high-performance terminal UI (TUI) orchestrator built in Go. It manages the lifecycle of remote, agent-assisted coding sessions, turning a JIRA ticket into a ready-to-code environment in seconds.

## 🚀 What It Does

Aiman automates the entire development workflow:

1. **Select a JIRA Issue** - Search and filter your assigned issues
2. **Generate Branch Name** - Auto-creates git-compatible branch names
3. **Pick a Repository** - Browse your GitHub repos
4. **Choose Subdirectory** - Pick a repo sub-folder (monorepo-friendly)
5. **Scan Agents** - Detect available agents on the remote
6. **Review Summary** - Confirm settings before creation
7. **Create Session** - Worktree + tmux + agent launch (in progress)

## ✨ Features

### Core Workflow
- **JIRA Integration**: Real-time search with VSCode-style filtering
- **Smart Branch Names**: Auto-sanitizes issue titles for git compatibility
- **Repo & Directory Picker**: Choose repo + subdirectory from the remote
- **Multi-Agent Support**: Scan and select Claude Code, Gemini CLI, GitHub Copilot, or OpenCode
- **Session Management**: Track active sessions with live tmux previews

### Remote Development
- **SSH Multiplexing**: High-performance connections with ControlMaster
- **MOSH Support**: Handoff to MOSH for high-latency connections (coming soon)
- **Mutagen Sync**: Real-time file sync between local and remote
- **Tmux Integration**: Native tmux session management

### User Experience
- **Interactive TUI**: Built with Bubble Tea for a modern terminal UI
- **Real-time Previews**: Live tmux pane capture in the dashboard
- **VS Code Integration**: Open synced directories directly in VS Code (`v` key)
- **Health Checks**: Built-in "Doctor" validates all integrations on startup
- **Fuzzy Search**: Find issues, repos, and sessions quickly
- **Progress Loading**: Dedicated loaders between flow steps

### Configuration
- **YAML-based Config**: Simple `~/.aiman/config.yaml` configuration
- **SQLite Persistence**: Session state and history tracking
- **Secure Token Storage**: JIRA API tokens stored in config (use `op` or similar for production)

## 🛠 Installation

### Quick Install

```bash
# Download and run the installer
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash

# Or install to a custom location
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --prefix ~/bin

# Or install for current user only
curl -sSL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | bash -s -- --user
```

### Update

```bash
# Update to the latest version
./scripts/update.sh

# Or force update even if on latest version
./scripts/update.sh --force
```

### Manual Build

```bash
# Clone and build
git clone git@github.com:bouwerp/aiman.git
cd aiman
go build -o aiman ./cmd/aiman

# Install to PATH
sudo mv aiman /usr/local/bin/
```

## 📋 Prerequisites

- **Go 1.26+** (for building from source)
- **GitHub CLI (`gh`)**: Authenticated with `gh auth login`
- **SSH**: Key-based authentication configured for remote servers
- **JIRA API Token**: Generate at [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens)
- **Optional**: 
  - `tmux` for session management
  - `mutagen` for file syncing
  - `code` (VS Code CLI) for IDE integration
  - `mosh` for high-latency connections

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
| `n` | **New Session** - Start the workflow wizard |
| `m` | **Admin Menu** - Configure remotes, JIRA, etc. |
| `↑/↓` | Navigate sessions |
| `Enter` | Select item |
| `ESC` | Go back / Cancel |
| `Ctrl+S` | **Attach** to tmux session (full terminal) |
| `Ctrl+T` | **Toggle** between preview and terminal mode |
| `v` | **Open in VS Code** (local synced directory) |
| `r` | **Refresh** session list |
| `Ctrl+C` | Quit |

### Creating a New Session

1. Press `n` on the dashboard
2. **Select JIRA Issue**: Type to filter your issues in real-time
3. **Confirm Branch Name**: Edit the auto-generated git-compatible branch name
   - Invalid characters are blocked
   - Spaces automatically become dashes
4. **Select Repository**: Pick from your GitHub repos
5. **Select Subdirectory**: Choose a repo subdirectory (or `.` for root)
6. **Agent Selection**: Choose your AI coding assistant (Claude, Gemini, etc.)
7. **Summary**: Review selected issue/branch/repo/dir/agent before creation

Note: Session creation (worktree, tmux, agent launch, sync) is in progress.

### Administrative Menu

Press `m` to access:
- **Manage Remote Servers**: Add, scan, or test SSH connections
- **JIRA Configuration**: Update credentials
- **Health Checks**: Re-run doctor checks

### Git Repository Configuration

By default, Aiman shows your personal GitHub repositories. You can customize which repositories appear in the picker by editing `~/.aiman/config.yaml`:

```yaml
git:
  # Include your personal repositories (default: true)
  include_personal: true
  
  # Include repositories from specific organizations
  include_orgs:
    - "mycompany"
    - "opensource-org"
  
  # Include only repos matching these patterns (optional)
  # Supports regex. If empty, includes all repos not excluded
  include_patterns:
    - "^mycompany/.*"
    - "^important-"
  
  # Exclude repos matching these patterns (optional)
  # Supports regex
  exclude_patterns:
    - "^personal/"
    - ".*\.github\.io$"
```

**Examples:**

Show only repos from your company org:
```yaml
git:
  include_personal: false
  include_orgs:
    - "mycompany"
```

Include personal repos and filter out forks:
```yaml
git:
  include_personal: true
  exclude_patterns:
    - ".*-fork$"
```

Include only specific repos by exact name:
```yaml
git:
  include_personal: true
  include_patterns:
    - "^mycompany/backend-api$"
    - "^mycompany/frontend-app$"
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
├── aiman.db             # SQLite database
└── sockets/             # SSH ControlMaster sockets
```

### Example Config

```yaml
integrations:
  jira:
    url: "https://company.atlassian.net"
    email: "you@company.com"
    api_token: "ATATT..."

# Git Repository Configuration
git:
  # Include your personal repositories (default: true)
  include_personal: true
  
  # Include repositories from specific organizations
  include_orgs:
    - "mycompany"
    - "opensource-org"
  
  # Include only repos matching these patterns (optional)
  # Supports regex. If empty, includes all repos not excluded
  include_patterns:
    - "^mycompany/.*"
    - "^important-"
  
  # Exclude repos matching these patterns (optional)
  # Supports regex
  exclude_patterns:
    - "^personal/"
    - ".*\.github\.io$"

remotes:
  - name: devbox
    host: devbox.company.com
    user: developer
    root: /home/developer/repos

active_remote: devbox
```

## 🏗 Architecture

Aiman follows **Clean Architecture** principles:

```
┌─────────────────────────────────────────┐
│  UI (Bubble Tea)                        │
│  - Dashboard, Pickers, Inputs           │
├─────────────────────────────────────────┤
│  Use Cases                              │
│  - Doctor, Session Discovery, Flow      │
├─────────────────────────────────────────┤
│  Domain                                 │
│  - Session, Issue, Repository           │
├─────────────────────────────────────────┤
│  Infrastructure                         │
│  - JIRA, Git, SSH, SQLite, Mutagen     │
└─────────────────────────────────────────┘
```

### Key Components

- **`JiraProvider`**: JIRA Cloud API v3 integration
- **`GitSlugger`**: Branch name sanitization
- **`SSHManager`**: ControlMaster multiplexing
- **`WorktreeManager`**: Git worktree operations
- **`MutagenBridge`**: File synchronization
- **`TmuxManager`**: Session lifecycle management
- **`SkillEngine`**: Agent configuration injection

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
- [ ] MOSH support
- [ ] SQLite persistence for sessions
- [ ] Skill injection system
- [ ] Claude Code integration
- [ ] Gemini CLI integration
- [ ] GitHub Copilot CLI support

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/amazing-feature`
3. Commit your changes: `git commit -m 'Add amazing feature'`
4. Push to the branch: `git push origin feature/amazing-feature`
5. Open a Pull Request

## 📄 License

MIT License - see LICENSE file for details

## 🙏 Acknowledgments

- Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) by Charm
- Inspired by [Claude Code](https://claude.ai/code) and other agentic tools
- Uses [Mutagen](https://mutagen.io/) for file synchronization

---

*Built with ❤️ in Go by Pieter Bouwer*
