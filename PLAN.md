# Aiman Implementation Plan: Architectural Blueprint

## 1. Architectural Strategy: Clean Architecture & DDD
To ensure **Aiman** is resilient to changes in external integrations (JIRA APIs, SSH variations, Agent CLI updates), we will follow a strict layered approach:

*   **Domain Layer (`/internal/domain`)**: Pure business logic and entities (e.g., `Session`, `Issue`, `Worktree`, `Agent`). No dependencies on external libraries.
*   **Usecase Layer (`/internal/usecase`)**: Orchestrators that coordinate the 11-step workflow defined in the SPEC.
*   **Infrastructure Layer (`/internal/infra`)**: Implementations of the "Driving" and "Driven" adapters.
    *   `jira`: JIRA Cloud API client.
    *   `git`: Wrapper for `gh` and local `git` commands.
    *   `ssh`: Managed SSH multiplexing and MOSH handoff.
    *   `sqlite`: Persistence for session state using `modernc.org/sqlite`.
*   **UI Layer (`/internal/ui`)**: `bubbletea` programs and `lipgloss` styles.

## 2. Core Domain Interfaces (The Contracts)
We will define high-level contracts to decouple our orchestrator:

```go
type IssueProvider interface {
    SearchIssues(query string) ([]domain.Issue, error)
    GetIssue(id string) (domain.Issue, error)
}

type RepositoryManager interface {
    ListRepos() ([]domain.Repo, error)
    SetupWorktree(repo domain.Repo, branch string) (domain.Worktree, error)
}

type RemoteOrchestrator interface {
    Connect(host string) (domain.Connection, error)
    Exec(cmd string) error
    StartTmux(sessionName string) error
}

type SyncEngine interface {
    Start(local, remote string) (domain.SyncStatus, error)
    Stop() error
}
```

## 3. Implementation Phases

### Phase 1: Foundation & Domain Modeling (TDD)
*   Initialize Go 1.24 module.
*   Define core entities: `Session`, `RemoteConfig`, `Agent`.
*   Implement `Session` state machine (Provisioning -> Active -> Syncing -> Cleanup).
*   **Deliverable**: Domain unit tests with 100% coverage.

### Phase 2: Infrastructure Adapters
*   Implement `JiraProvider` using a mockable HTTP client.
*   Implement `GitManager` wrapping `os/exec` for `gh` and `git`.
*   Implement `SSHManager` with `ControlMaster` support.
*   **Deliverable**: Integration tests for CLI wrappers.

### Phase 3: The Orchestrator (Usecase)
*   Implement the `FlowManager` which executes the 11 steps.
*   Integrate `modernc.org/sqlite` for session persistence and history.
*   Implement "Skill" injection logic (mapping files to agent config paths).

### Phase 4: TUI Development (Bubbletea)
*   **Dashboard View**: Sidebar for sessions, main panel for logs/metadata.
*   **Flow Wizard**: Multi-step form for creating a new session.
*   **Terminal Integration**: Handling the handoff to interactive SSH/Mosh.

### Phase 5: Verification & "Doctor" Command
*   Implement `aiman doctor` to validate environment (Mutagen, GH CLI, SSH).
*   End-to-end integration testing of the full 11-step cycle.

## 4. Design Patterns to Employ
*   **Strategy Pattern**: For different `AgentRunners` (Claude vs Gemini).
*   **Observer Pattern**: To push background sync/SSH status updates to the Bubbletea UI.
*   **Factory Pattern**: For creating `RemoteExecutor` instances based on config.
*   **Repository Pattern**: For managing `Session` persistence.

## 5. Technical Constraints
*   **Go 1.24+**: Leveraging latest performance improvements and `os.Root` if applicable for secure dir handling.
*   **Pure Go SQLite**: Avoid CGO to ensure easy cross-compilation for remote boxes.
*   **Storage**: Configuration and database are stored in `~/.aiman/`.
*   **Concurrency**: Use errgroups for parallelizing prep steps (e.g., JIRA fetch + Remote SSH probe).
