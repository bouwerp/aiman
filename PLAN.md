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
- **Doctor Checks**: Automated validation of environment and credentials on startup.
- **Flow Wizard (Partial)**: Issue -> Branch -> Repo -> Directory -> Agent scan -> Summary.
- **Mutagen Sync Recovery**: Recreate sync for a selected session from the dashboard.

## 2. Technical Gotchas ⚠️

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
    7.  **Agent Selection**: Scan remote and select agent (Claude, Gemini, OpenCode, Copilot). ✅
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
- [ ] **Git Intelligence Panel**: Comprehensive git status display for each session showing:
    - Associated pull request (if exists) with link and status
    - PR review state: approved, changes requested, pending reviews
    - Open review comments count
    - Uncommitted changes (staged/unstaged)
    - Un-pushed commits count
    - Untracked files list
    - Changed files list with diff stats
    - Branch tracking status (ahead/behind remote)
    - Similar UX to lazygit but integrated into the dashboard
- [ ] **Agentic Patterns**:
    - Develop robust agentic patterns (e.g., Orchestrator-Worker-Validator).
    - Logic to translate these patterns for various supported coding tools.
    - Synchronize patterns to the remote dev server.
- [ ] **Skill Injection**: Implement the logic to map local "skill" files to remote agent configuration paths before agent launch.
- [ ] **MOSH Support**: Add an option to hand off to MOSH for high-latency interactive connections.
- [x] **CI/CD Pipeline & Releases**: GitHub Actions workflow for:
    - Running tests, linting, and type checking on PRs.
    - Building executables for macOS (Intel & Apple Silicon), Linux, and Windows.
    - Creating GitHub releases with all platform binaries as artifacts.
    - Automatic versioning based on git tags.
- [ ] **Remote VM Bootstrapper**:
    - Connect to a new remote VM and install baseline tooling (git, tmux, go, nodejs, npm, curl, claude, cursor, gemini, opencode, acli).
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
