# Aiman: Agentic Development Orchestrator Specification (v2.2)

**Aiman** is a high-performance terminal UI (TUI) orchestrator built in Go. It manages the lifecycle of remote, agent-assisted coding sessions, turning a JIRA ticket into a ready-to-code environment in seconds.

---

## 1. Technical Stack
* **Language:** Go 1.24+
* **TUI Framework:** `charmbracelet/bubbletea` + `lipgloss` + `bubbles`.
* **Database:** `modernc.org/sqlite` (Pure Go) for session state and history.
* **Remote Execution:** `ssh` with `ControlMaster` + `mosh` for interactive sessions.
* **Sync Engine:** `mutagen` (Project-based orchestration).
* **Integrations:** JIRA Cloud API, GitHub CLI (`gh`), Git Worktrees.

---

## 2. Core Workflow Engine

| Step | Component | Logic |
| :--- | :--- | :--- |
| **1. Issue** | `JiraProvider` | Fuzzy-search JIRA issues. Cache results for instant TUI filtering. |
| **2. Branch** | `GitSlugger` | Transform JIRA title into a clean git branch name. |
| **3. Repo** | `RepoManager` | Select target repo via `gh repo list`. |
| **4. Connect** | `SSHManager` | Establish SSH multiplexing; prepare MOSH handoff. |
| **5. Prep** | `RemoteGit` | Ensure remote `main` is clean; fetch latest HEAD. |
| **6. Isolate** | `WorktreeManager` | `git worktree add ../<branch-name>`. |
| **7. Scope** | `DirPicker` | Set the active working directory for the agent. |
| **8. Session** | `TmuxManager` | `tmux new-session -d -s <branch>`. Forward SSH_AUTH_SOCK. |
| **9. Skills** | `SkillEngine` | Sync Skill-Repo; map to `.clauderc`, `.geminirc`, or `gh` aliases. |
| **10. Agent** | `AgentRunner` | Launch **Claude Code**, **Gemini**, **OpenCode**, or **Copilot CLI**. |
| **11. Sync** | `MutagenBridge` | Start `mutagen` sync between local and remote worktree. |

---

## 3. Targeted Agent Integration

Aiman provides specialized bootstrapping for the following tools:

### A. Claude Code (`claude`)
* **Strategy:** Manages the long-running interactive loop within tmux. 
* **Context:** Injects skill-based instructions into the initial session prompt.

### B. Gemini CLI (`gemini` / `gcloud`)
* **Strategy:** Forwards Google Cloud credentials or API keys. 
* **Context:** Loads "Skills" as system instructions via the CLI's configuration path.

### C. GitHub Copilot CLI (`gh copilot`)
* **Strategy:** Relies on the `gh` CLI. Aiman verifies the `copilot` extension is installed on the remote box (`gh extension install github/gh-copilot`).
* **Execution:** Primarily used for `gh copilot suggest` or `gh copilot explain` within the tmux environment.
* **Skills:** Maps "Skills" from the git repo into custom shell aliases or context files that Copilot can reference.

### D. OpenCode
* **Strategy:** Connects to local/remote LLM backends (Ollama/vLLM).
* **Context:** Maps the specific git worktree path to the OpenCode workspace.

---

## 4. The Skills Manager (Phase 2)

Aiman treats skills as "Context Injection Units" synchronized via Git.

* **Mapping Engine:**
    * `skills/refactor.md` -> Appended to **Claude's** system prompt.
    * `skills/k8s-expert.sh` -> Injected as an alias for **Copilot CLI** suggestions.
    * `skills/unittest.json` -> Provided as a tool definition for **Gemini**.

---

## 5. Configuration (~/.aiman/config.yaml)

Aiman stores its configuration and database in `~/.aiman`.

* **Config Path:** `~/.aiman/config.yaml`
* **Database Path:** `~/.aiman/aiman.db`

(Internal YAML structure)
integrations:
  jira:
    url: "https://company.atlassian.net"
    token_cmd: "op read op://vault/jira/token"
  github:
    use_gh_cli: true
    extensions: ["github/gh-copilot"]

remotes:
  main-dev:
    host: "dev-box.internal"
    user: "developer"
    mosh: true
    root: "/home/developer/src"

agents:
  supported:
    - name: "claude"
      bin: "claude"
    - name: "gemini"
      bin: "gemini-chat"
    - name: "copilot"
      bin: "gh copilot"
    - name: "opencode"
      bin: "opencode"

skills:
  repo: "https://github.com/realfi-co/agent-skills.git"

---

## 6. User Interface (The "Control Plane")

### The Dashboard
* **Sidebar:** Active sessions with real-time Mutagen sync status icons.
* **Main Panel:** JIRA metadata, active branch, and agent logs.
* **Footer Bindings:**
    * `ctrl+n`: New Flow
    * `ctrl+k`: Nuke Session (Cleanup tmux, Mutagen, and Worktree)
    * `ctrl+s`: Attach to Session (Interactive Terminal)
    * `s`: Open Skills Selector

---

## 7. Commands & Maintenance

* `aiman`: Launch TUI.
* `aiman init`: Setup wizard (Auth JIRA, GitHub, and Remote Probe).
* `aiman doctor`: Validates `gh copilot` auth and `mutagen` health.
* `aiman clean`: Remove stale worktrees and sessions.
