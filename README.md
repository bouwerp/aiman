# Aiman

**Aiman** is a high-performance terminal UI (TUI) orchestrator built in Go. It simplifies the lifecycle of remote, agent-assisted coding sessions by turning JIRA tickets into ready-to-code environments on remote dev servers in seconds.

## 🚀 General Functionality

Aiman automates the "prep work" for agentic development:
- **JIRA Integration**: Search and select JIRA issues directly from the TUI.
- **Git Orchestration**: Automatically creates slugs for branches and sets up git worktrees.
- **Remote Dev Support**: Manages SSH connections (with multiplexing) to remote dev servers.
- **GitHub Integration**: Lists and filters your accessible repositories via the GitHub CLI (`gh`).
- **Health Checks**: Built-in "Doctor" checks verify your JIRA, Git, and SSH connectivity on startup.
- **Persistence**: Stores session states and history in a local SQLite database.

## 🛠 Getting Started

### Prerequisites

- **Go 1.25+**
- **GitHub CLI (`gh`)**: Authenticated (`gh auth login`).
- **SSH**: Configured with key-based authentication for your remote servers.
- **JIRA API Token**: Generate one at [id.atlassian.com](https://id.atlassian.com/manage-profile/security/api-tokens).

### Installation

1. Clone the repository:
   ```bash
   git clone git@github.com:bouwerp/aiman.git
   cd aiman
   ```

2. Build and install:
   ```bash
   go install ./cmd/aiman
   ```

### Initial Setup

Run the initialization wizard to configure your JIRA instance:

```bash
aiman init
```

This will guide you through setting up your JIRA URL, Email, and API Token. Configuration is stored in `~/.aiman/config.yaml`.

## 💻 How To Use

### Launching the Dashboard

Simply run `aiman` to launch the main TUI:

```bash
aiman
```

Upon startup, Aiman runs a series of **Doctor Checks** to ensure your integrations are healthy.

### Administrative Tasks (Context Menu)

From the main dashboard, press **`m`** to open the **Administrative Menu**. From here you can:

1.  **Configure Remote Servers**:
    - **Scan**: Aiman can scan your `known_hosts` file to suggest servers.
    - **Manual**: Add a new server by IP/Hostname and Username.
    - **Test**: Aiman automatically tests the SSH connection before adding it to your config.
2.  **JIRA Configuration**: Re-run the setup wizard to update your JIRA credentials.

### Managing Repositories

You can quickly browse and filter your GitHub repositories:

```bash
aiman repos
```

This uses a fuzzy-search interface to help you find the right repository for your next task.

## 📁 Configuration & Data

Aiman keeps all its data in a hidden directory in your home folder:

- **Config**: `~/.aiman/config.yaml`
- **Database**: `~/.aiman/aiman.db`

## 🏗 Architecture

Aiman follows a strict **Clean Architecture** approach:
- **Domain**: Core business logic and entities.
- **Usecase**: Orchestrators for the development workflow.
- **Infrastructure**: Adapters for JIRA, Git, SSH, and SQLite.
- **UI**: Interactive TUI components built with `charmbracelet/bubbletea`.

---
*Built with ❤️ in Go.*
