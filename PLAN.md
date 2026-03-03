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

## 2. Technical Gotchas ⚠️

- **SSH Backgrounding**: Avoid using `ssh -f` (backgrounding) inside Go's `os/exec` as it often receives a `SIGKILL` or `SIGHUP` when the parent Go process handles signals. Stick to `-o ControlMaster=auto` and `-o ControlPersist` for robust multiplexing.
- **Bubbletea Versioning**: Do **not** mix `bubbletea` v1 and v2. Libraries like `bubbleterm` that require v2 will cause interface conflicts with standard `bubbles` components. If a terminal is needed, use `vt10x` to build a custom v1-compatible component.
- **Socket Path Limits**: Unix sockets (used for SSH ControlPath) have strict character limits (~100-108 chars). Keep socket names in `~/.aiman/sockets/` short and hashed if necessary.
- **ANSI Capture**: When using `tmux capture-pane -e`, ensure the TUI handles the resulting ANSI sequences correctly to avoid rendering artifacts.

## 3. Immediate TODOs 🚀

- [ ] **Flow Manager Implementation**: Wire up the 11-step orchestration logic to create *new* sessions (currently we only discover existing ones).
- [ ] **JIRA Picker**: Create a TUI component to search and select JIRA issues to start new work.
- [ ] **Repo/Branch Wizard**: Interactive selection of target repository and automatic slug generation for branches.
- [ ] **SQLite Persistence**: Fully wire the existing `internal/infra/sqlite` repository to save discovered and manually created sessions.
- [ ] **Skill Injection**: Implement the logic to map local "skill" files to remote agent configuration paths.
- [ ] **Agent Orchestration**: Implement the `AgentRunner` interfaces for Claude, Gemini, **Cursor-Agent**, and **opencode** (or `opencode-cli` on Linux) CLI wrappers.
- [ ] **MOSH Support**: Add an option to hand off to MOSH for high-latency connections.

## 4. Architectural Strategy (Reminder)
Keep following the **Clean Architecture** pattern. Ensure that the `internal/usecase` layer remains the only place where domain entities are coordinated, and keep infrastructure-specific logic (like `mutagen` or `ssh` CLI flags) strictly within `internal/infra`.
