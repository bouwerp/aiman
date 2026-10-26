# W5 Verifier Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `aiman verify` snapshots a worker session's working tree, runs a different-vendor reviewer agent on it as a hidden child session, and turns its REQUIRED findings into worker to-dos.

**Architecture:** A new `internal/verify` package owns the snapshot (temporary-index commit plus detached worktree), the reviewer brief, the findings parser, a JSON review store and a `Manager` that drives one review from start to cleanup. `aiman serve` wires the manager to its session creator, nl-1's `Server.TodoAccess()`, the context store, the event hub and nl-5's `notify.Recorder`, and exposes `verify.start` / `verify.status`. The CLI polls `verify.status` for `--wait`; the TUI starts a review with `V` (through nl-1's `aiman rpc`) and shows `reviewing…` on the meta line.

**Tech Stack:** Go 1.26, `os/exec` git, Bubble Tea v2, `modernc.org/sqlite`, newline JSON over `~/.aiman/aiman.sock`.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (W5, Constraints, Noise budget, Shared interfaces).

**Depends on:**
- nl-1 (W0 + W1): `domain.Session.BaseBranch`, `domain.TodoItem`/`TodoList`, `domain.AuthorVerifier`, `todo.FormatList`, `(*Server).TodoAccess()` (its `Mutate` goes through `Server.MutateTodos`, which renders `.aiman_todo.md`, publishes `todo_changed` and enqueues the steering note), `s.hub` and the reserved `verify` event type, `FlowManager.resolveWorktree` (nl-1 Task 3), `aiman rpc`/`usecase.ServeRPC`, the `finding:` `Ref` prefix.
- nl-2 (A1 dashboard): `detailMetaExtras.Reviewing`, `m.metaExtras`.
- nl-3b (W6b): `domain.ContextEntry.Type`, `domain.ContextTypeReview`. nl-3b lands before this plan.
- nl-5 (W2): `(*Server).NotifyRecorder()`, `(*notify.Recorder).Emit(ctx, sessionID, label string, kind domain.NotifyKind, detail string) error` (nil-safe), `domain.NotifyReviewDone`, `notify.Label`.

## Global Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only. Key tests build events with `pressKey`/`pressRune` (`internal/ui/keystrokes_test.go:16,32`).
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- No socket method may block longer than 30 s (`server.Call` sets a 30 s deadline, `internal/server/client.go:47`). Long waits are client-side polling.
- Both terminal backends (tmux, pty) must work.
- Serve is the only writer of review state and of to-dos. Other processes go through the socket.
- `.aiman_review.md` uses the `.aiman_context.md` "DO NOT COMMIT" header (`internal/contextstore/files.go:545-548`) and is covered by the `.aiman_*` ignore glob (`internal/domain/session_files.go:12`).
- `internal/verify` must not import `internal/server` or `internal/gateway`.
- Initial prompts are delivered as one line; newlines can submit early (`internal/usecase/flow_manager.go:48-52`). Long reviewer instructions go in a file.
- Reviewer sessions are hidden from the default dashboard list. The meta line gains only `reviewing…` while a verifier runs.
- `review_done` is emitted only when a review has REQUIRED findings; its toast and push come from nl-5. A clean pass is logged by serve and stored as a `review` note, with no toast or push. This plan adds only the acknowledgement toast when the user presses the key.

## Review Focus

1. **Re-running `aiman verify --wait` after a client timeout.** The worker's agent tool (Claude's Bash tool caps at 10 min) kills a long wait. Running it again on an unchanged tree must re-attach to the same review, not start a second reviewer. Test: `TestStartReturnsExistingReviewForSameTree` (Task 7).
2. **Reviewer exits without writing the marker** (crash, quota, user quits). The review must end as `incomplete`, still clean up, and `--wait` must not report a pass. Tests: `TestWatchProcessExitWithoutMarkerIsIncomplete` (Task 8), `TestWaitExitCodeIncomplete` (Task 10).
3. **Serve restarts mid-review.** Running reviews must resume watching on start and still clean up their worktree. Test: `TestResumeWatchesRunningReviews` (Task 8).
4. **Repeated reviews re-raise the same finding.** An open verifier to-do with the same text must not be duplicated. Test: `TestCompleteSkipsDuplicateOpenFinding` (Task 8).
5. **Worker with no `BaseBranch`** (sessions created before W0). The diff base must fall back to the remote default branch instead of failing. Test: `TestResolveBaseFallsBackToOriginHead` (Task 3).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/domain/session.go` (modify) | `Session.Role`, `SessionConfig.Role`, `SessionConfig.PreparedWorktree`, role constants |
| `internal/domain/session_files.go` (modify) | `AimanReviewFileName` |
| `internal/infra/sqlite/repository.go`, `repository_scan.go` (modify) | `role` column, COALESCE-protected |
| `internal/server/handlers_session.go` (modify) | `SessionInfo.Role` |
| `internal/infra/config/config.go` (modify) | `VerifyConfig` in `Config` and `ServeConfig` |
| `internal/infra/agent/scanner.go` (modify) | exported `Installed` |
| `internal/verify/agent.go` | reviewer agent selection |
| `internal/verify/snapshot.go` | temp-index snapshot, detached worktree, base resolution, diff |
| `internal/verify/findings.go` | marker, `Finding`, `ParseReview` |
| `internal/verify/brief.go` | reviewer `.aiman_task.md` content and one-line prompt |
| `internal/verify/review.go` | `Review` type, statuses, JSON file store |
| `internal/verify/manager.go` | `Manager.Start/Status/LatestFor/Resume`, watcher, completion, cleanup |
| `internal/usecase/flow_manager.go` (modify) | `PreparedWorktree` case in nl-1's `resolveWorktree` |
| `internal/server/handlers_verify.go` | `verify.start`, `verify.status`, adapters |
| `cmd/aiman/serve.go`, `cmd/aiman/rpc_cmd.go` (modify) | build and resume the manager; allowlist `verify.start`, `verify.status` |
| `cmd/aiman/verify_cmd.go` | `aiman verify` CLI and `--wait` polling |
| `internal/usecase/verify_remote.go` | laptop-side `StartVerifyOnRemote` over `ServeRPC` |
| `internal/ui/verify.go` | `V` key, `reviewing…`, reviewer filtering |
| `internal/aimanskill/SKILL.md` (modify) | "Before you open a PR" section |

---

### Task 1: Session role, persistence and wire field

**Files:**
- Modify: `internal/domain/session.go:54-91` (Session), `:209-260` (SessionConfig)
- Modify: `internal/infra/sqlite/repository.go:103` (ALTER), `:218-260` (Save), `internal/infra/sqlite/repository_scan.go:11,20-34`
- Modify: `internal/server/handlers_session.go:18-69` (`SessionInfo`, `sessionInfo`)
- Modify: `internal/ui/session_events.go:275-296` (`sessionFromInfo`), `internal/ui/session_merge.go:101` (`overlayPersistedSessionFields`)
- Test: `internal/infra/sqlite/session_role_test.go`, `internal/server/handlers_session_test.go`, `internal/ui/session_merge_test.go`

**Interfaces:**
- Produces: `type SessionRole string`; `const SessionRoleReviewer SessionRole = "reviewer"`; `Session.Role SessionRole`; `SessionConfig.Role SessionRole`; `SessionInfo.Role string` (`json:"role,omitempty"`). Empty role means an ordinary worker session.

- [ ] **Step 1: Write the failing tests**

```go
func TestSaveAndGetRole(t *testing.T)            // Save Role "reviewer"; Get returns it
func TestSavePreservesRoleWhenEmpty(t *testing.T) // Save with Role then Save same ID with "" → still "reviewer"
```
In `handlers_session_test.go`: `TestSessionInfoCarriesRole` asserts `sessionInfo(domain.Session{Role: domain.SessionRoleReviewer}, "").Role == "reviewer"`. In `session_merge_test.go`: `TestOverlayKeepsStoredRole` asserts a live session with empty Role takes the stored `"reviewer"`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/infra/sqlite/ ./internal/server/ ./internal/ui/ -run 'Role' -v`
Expected: FAIL, `Role` undefined.

- [ ] **Step 3: Implement**

Add the type, constant and both fields. Add `ALTER TABLE sessions ADD COLUMN role TEXT` after `:103`; append `role` to the INSERT column list, VALUES and `sessionSelectCols` (after any columns W0 added), with `role = COALESCE(NULLIF(excluded.role, ''), sessions.role)`; scan into `sql.NullString`. `CreateSession` copies `config.Role` into the new `Session` at `flow_manager.go:257-271`. `sessionFromInfo` maps `info.Role`; `overlayPersistedSessionFields` fills an empty live Role from stored.

- [ ] **Step 4: Run them and see them pass**

Run: `go test ./internal/infra/sqlite/ ./internal/server/ ./internal/ui/ ./internal/usecase/ -v -run 'Role|Overlay'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain internal/infra/sqlite internal/server/handlers_session.go internal/server/handlers_session_test.go internal/ui/session_events.go internal/ui/session_merge.go internal/ui/session_merge_test.go internal/usecase/flow_manager.go
git commit -m "feat: add a session role and persist it"
```

---

### Task 2: Verify config and reviewer agent selection

**Files:**
- Modify: `internal/infra/config/config.go:20-31` (`Config`), `:43-63` (`ServeConfig`, `MarshalServeConfig`)
- Modify: `internal/infra/agent/scanner.go:239` (export a wrapper over `commandExists`)
- Create: `internal/verify/agent.go`
- Test: `internal/infra/config/config_test.go`, `internal/verify/agent_test.go`

**Interfaces:**
- Produces:
  - `type VerifyConfig struct { Agents []string \`yaml:"agents,omitempty"\`; BeforePR bool \`yaml:"before_pr,omitempty"\`; Timeout time.Duration \`yaml:"timeout,omitempty"\` }`, field `Verify VerifyConfig \`yaml:"verify,omitempty"\`` on both `Config` and `ServeConfig`.
  - `func (s *Scanner) Installed(ctx context.Context, command string) bool`
  - `verify.DefaultTimeout = 45 * time.Minute`
  - `func PickReviewer(agents []string, workerAgent, override string, installed func(string) bool) (string, error)`; `var ErrNoReviewer = errors.New("no installed reviewer agent differs from the worker's")`

- [ ] **Step 1: Write the failing tests**

`TestMarshalServeConfigCarriesVerify`: a `Config{Verify: VerifyConfig{Agents: []string{"codex","claude"}, BeforePR: true}}` marshals with `MarshalServeConfig("h")` (nl-5's signature) to YAML that unmarshals into `ServeConfig` with the same values.

```go
func TestPickReviewer(t *testing.T) {
	installed := func(c string) bool { return c == "codex" || c == "claude" }
	cases := []struct{ name, worker, override, want string; agents []string; err error }{
		{name: "first differing installed", agents: []string{"claude", "codex"}, worker: "Claude Code", want: "codex"},
		{name: "skips not installed", agents: []string{"grok", "claude"}, worker: "codex", want: "claude"},
		{name: "override wins", agents: []string{"codex"}, worker: "claude", override: "claude", want: "claude"},
		{name: "override not installed", agents: nil, worker: "claude", override: "grok", err: ErrNoReviewer},
		{name: "none left", agents: []string{"claude"}, worker: "claude", err: ErrNoReviewer},
	}
	// assert got == want and errors.Is(err, tc.err)
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/infra/config/ ./internal/verify/ -run 'Verify|PickReviewer' -v`
Expected: FAIL, undefined.

- [ ] **Step 3: Implement**

`PickReviewer` normalises the worker's agent with `agent.FindKnown(worker)` then `agent.CommandBase(.Command)` (falling back to `CommandBase(worker)`), so a display name like "Claude Code" compares as `claude`. An installed override is returned as is. Empty `agents` means no candidates.

- [ ] **Step 4: Run them and see them pass**

Run: `go test ./internal/infra/config/ ./internal/verify/ -run 'Verify|PickReviewer' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/config internal/infra/agent/scanner.go internal/verify/agent.go internal/verify/agent_test.go
git commit -m "feat: add verify config and reviewer agent selection"
```

---

### Task 3: Working-tree snapshot, detached worktree and diff

**Files:**
- Create: `internal/verify/snapshot.go`
- Test: `internal/verify/snapshot_test.go` (real git in `t.TempDir()`, skip when `git` is missing)

**Interfaces:**
- Produces:
  - `type Snapshot struct { Commit, Tree string }`
  - `func TakeSnapshot(ctx context.Context, worktree string) (Snapshot, error)`
  - `func AddDetachedWorktree(ctx context.Context, repoWorktree, path, commit string) error`
  - `func RemoveWorktree(ctx context.Context, repoWorktree, path string) error`
  - `func ResolveBase(ctx context.Context, worktree, base string) (string, error)`
  - `type Diff struct { Stat, Patch string; Truncated bool; MergeBase string }`
  - `func DiffAgainst(ctx context.Context, worktree, baseRef, commit string, maxPatchBytes int) (Diff, error)`
  - `const MaxPatchBytes = 150 * 1024`

- [ ] **Step 1: Write the failing tests**

```go
func TestTakeSnapshotIncludesUncommittedAndUntracked(t *testing.T)
// repo with one commit; modify a tracked file, add an untracked file, add an ignored file (.gitignore "*.log");
// snap.Commit's tree (git ls-tree -r) contains the untracked file and the modified content, not the .log file;
// `git status --porcelain` and the real index are unchanged afterwards.
func TestTakeSnapshotSameTreeTwice(t *testing.T)        // two calls, no edits → equal Tree
func TestAddAndRemoveDetachedWorktree(t *testing.T)     // worktree HEAD == commit and detached; after Remove the dir is gone and `git worktree list` omits it
func TestResolveBaseFallsBackToOriginHead(t *testing.T) // clone with origin; base "" → "origin/<default>"; base "main" with no local main → "origin/main"
func TestDiffAgainstTruncates(t *testing.T)            // maxPatchBytes 64 → Truncated, Stat non-empty, len(Patch) <= 64
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/verify/ -run 'Snapshot|Worktree|ResolveBase|DiffAgainst' -v`
Expected: FAIL, undefined.

- [ ] **Step 3: Implement**

Run git with `exec.CommandContext(ctx, "git", "-C", dir, …)`, separating stdout from stderr. `TakeSnapshot`: index path `filepath.Join(os.MkdirTemp(…), "index")` (a path that does not exist yet; an empty file is not a valid index), env `GIT_INDEX_FILE=<path>`, then `read-tree HEAD`, `add -A`, `write-tree`, `commit-tree <tree> -p HEAD -m "aiman review snapshot"`; remove the temp dir. `AddDetachedWorktree`: `worktree add --detach <path> <commit>`. `RemoveWorktree`: `worktree remove --force <path>` then `worktree prune`. `ResolveBase`: empty base uses `rev-parse --abbrev-ref refs/remotes/origin/HEAD`, then `origin/main`, `origin/master` (same order as `detectBaseBranch`, `internal/infra/git/manager.go:1031-1050`); a named base tries `<base>` then `origin/<base>` with `rev-parse --verify`. `DiffAgainst`: `merge-base <baseRef> <commit>`, then `diff --stat` and `diff` between the merge base and the commit.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/verify/ -run 'Snapshot|Worktree|ResolveBase|DiffAgainst' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/verify/snapshot.go internal/verify/snapshot_test.go
git commit -m "feat: snapshot a worktree into a detached review worktree"
```

---

### Task 4: Findings parser and reviewer brief

**Files:**
- Modify: `internal/domain/session_files.go:3-13`
- Create: `internal/verify/findings.go`, `internal/verify/brief.go`
- Test: `internal/verify/findings_test.go`, `internal/verify/brief_test.go`

**Interfaces:**
- Produces:
  - `domain.AimanReviewFileName = ".aiman_review.md"`
  - `const CompleteMarker = "<!-- aiman:review-complete -->"`
  - `type Severity string`; `SeverityRequired = "REQUIRED"`, `SeveritySuggestion = "SUGGESTION"`
  - `type Finding struct { ID string; Severity Severity; Title, Location, Body string }` (`json` tags lower snake case)
  - `func ParseReview(text string) (findings []Finding, complete bool)`
  - `type BriefInput struct { WorkerName, BaseRef, Commit string; Diff Diff; Task string; Todos []domain.TodoItem }`
  - `func BuildBrief(in BriefInput) string`
  - `const InitialPrompt = "Read .aiman_task.md and carry out the code review it describes. Write your findings to .aiman_review.md exactly in the format it gives, ending with the completion marker. Do not edit any other file, commit, push or open a pull request."`

- [ ] **Step 1: Write the failing tests**

```go
func TestParseReview(t *testing.T) {
	text := "# Review\n\n## [REQUIRED] Nil map write\ninternal/a.go:42\nwrites to m before make.\n\n" +
		"## [SUGGESTION] Rename x\nb.go:7\nclearer.\n\n<!-- aiman:review-complete -->\n"
	got, complete := ParseReview(text)
	// complete == true; len(got) == 2
	// got[0] == Finding{ID: "f1", Severity: SeverityRequired, Title: "Nil map write", Location: "internal/a.go:42", Body: "internal/a.go:42\nwrites to m before make."}
	// got[1].ID == "f2", Severity == SeveritySuggestion
}
func TestParseReviewWithoutMarker(t *testing.T)   // complete == false, findings still parsed
func TestParseReviewIgnoresUnknownTags(t *testing.T) // "## [NIT] x" is not a finding; lowercase "[required]" is REQUIRED
func TestBuildBrief(t *testing.T)
// contains "DO NOT COMMIT", the worker's task text, "t3 ▸" style to-do lines, the diff stat and patch inside ```diff,
// "## [REQUIRED|SUGGESTION] <title>", CompleteMarker, and when Diff.Truncated: "git diff <MergeBase> HEAD".
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/verify/ -run 'ParseReview|BuildBrief' -v`
Expected: FAIL, undefined.

- [ ] **Step 3: Implement**

A finding header matches `^##\s*\[(REQUIRED|SUGGESTION)\]\s*(.+)$` case-insensitively; its body runs to the next `## ` header or the marker. `Location` is the first `path:line` match (`[\w./-]+:\d+`) in the body. IDs are `f1`, `f2`… in document order. The brief starts with nl-1's `domain.AimanScaffoldHeader` ("DO NOT COMMIT"), then sections: what to review (snapshot of `<WorkerName>` against `<BaseRef>`), the worker's task (`.aiman_task.md` content, or "No task file."), the worker's to-do list (one line per item, same glyphs as the steering note), the diff, and the output format with the marker as its last line. To-do lines come from nl-1's `todo.FormatList`.

- [ ] **Step 4: Run them and see them pass**

Run: `go test ./internal/verify/ -run 'ParseReview|BuildBrief' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/session_files.go internal/verify/findings.go internal/verify/findings_test.go internal/verify/brief.go internal/verify/brief_test.go
git commit -m "feat: parse review findings and build the reviewer brief"
```

---

### Task 5: Review record and store

**Files:**
- Create: `internal/verify/review.go`
- Test: `internal/verify/review_test.go`

**Interfaces:**
- Produces:
  - `type Status string`: `StatusStarting "starting"`, `StatusRunning "running"`, `StatusComplete "complete"`, `StatusIncomplete "incomplete"`, `StatusTimedOut "timed_out"`, `StatusFailed "failed"`, `StatusDisabled "disabled"`; `func (s Status) Active() bool` (starting or running)
  - `type Review struct { ID, WorkerID, ReviewerID, Agent, Tree, Commit, BaseRef, RepoWorktree, Worktree, Error string; Status Status; Findings []Finding; Required, Suggestions int; CleanedUp bool; StartedAt, FinishedAt time.Time }` (JSON tags snake case)
  - `func (r Review) Passed() bool` (Status complete and Required == 0)
  - `type Store struct{ … }`; `func NewStore(dir string) *Store`; `func (s *Store) Put(r Review) error`; `Get(id string) (Review, error)`; `List() ([]Review, error)`; `var ErrNotFound`
  - Store dir: `filepath.Join(<aiman dir>, "reviews")`

- [ ] **Step 1: Write the failing tests**

`TestStorePutGetList` (round trip, List sorted by StartedAt), `TestStoreGetMissing` (`errors.Is(err, ErrNotFound)`), `TestStorePutIsAtomic` (no `*.tmp` files left in the dir), `TestReviewPassed` (table: complete/0 → true; complete/1, incomplete/0, timed_out/0 → false).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/verify/ -run 'Store|Passed' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

One `<id>.json` per review, written temp file plus rename under a mutex, file mode `0600`, dir `0700`.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/verify/ -run 'Store|Passed' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/verify/review.go internal/verify/review_test.go
git commit -m "feat: persist review records as JSON"
```

---

### Task 6: Create a session in a prepared worktree

**Files:**
- Modify: `internal/domain/session.go:226-230` (SessionConfig)
- Modify: `internal/usecase/flow_manager.go` (`resolveWorktree`, extracted by nl-1 Task 3)
- Test: `internal/usecase/flow_manager_validation_test.go`

**Interfaces:**
- Produces: `SessionConfig.PreparedWorktree string`: an existing directory used as `WorktreePath`, skipping all git setup. It is a new first case in nl-1's `func (m *FlowManager) resolveWorktree(ctx, sshMgr, config, branch) (domain.Worktree, error)`, returning `Worktree{Path: config.PreparedWorktree}`.

- [ ] **Step 1: Write the failing test**

There are no `CreateSession` fakes beyond `TestCreateSessionRequiresAgent`, so nl-1 extracted the switch and this test drives it directly. `TestResolveWorktreePrepared`: `(&FlowManager{}).resolveWorktree(ctx, nil, SessionConfig{PreparedWorktree: "/r/repo@review-ab12", Repo: domain.Repo{Name: "o/repo"}}, "review-ab12")` returns `Worktree{Path: "/r/repo@review-ab12"}, nil`. The nil `gitManager` and nil executor prove no git call is made.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/usecase/ -run TestResolveWorktreePrepared -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add `case config.PreparedWorktree != "": return domain.Worktree{Path: config.PreparedWorktree}, nil` as the first case of `resolveWorktree`. `CreateSession` already assigns the result to `session.WorktreePath`. The rest of the flow (ignore rule, aiman-id in the git dir, skills, launch) runs unchanged.

- [ ] **Step 4: Run it and see it pass**

Run: `go test ./internal/usecase/ -v -run "CreateSession|ResolveWorktree"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/session.go internal/usecase/flow_manager.go internal/usecase/flow_manager_validation_test.go
git commit -m "feat: let CreateSession run in a prepared worktree"
```

---

### Task 7: Manager start and status

**Files:**
- Create: `internal/verify/manager.go`
- Test: `internal/verify/manager_test.go`

**Interfaces:**
- Consumes: Tasks 2-6; nl-1 domain to-do types.
- Produces:

```go
type Config struct { Agents []string; BeforePR bool; Timeout time.Duration }
type Sessions interface {
	Get(ctx context.Context, id string) (domain.Session, error)
	CreateChild(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) // persists the child
	Forget(ctx context.Context, id string) error
}
type Terminal interface {
	Alive(ctx context.Context, s domain.Session) bool // false once the terminal process is gone
	Stop(ctx context.Context, s domain.Session) error
}
type Todos interface {
	Get(sessionID string) (domain.TodoList, error)
	Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error
}
type Notifier interface { ReviewFinished(ctx context.Context, worker domain.Session, r Review) } // called for every finished review
type Deps struct {
	Store *Store; Sessions Sessions; Terminal Terminal; Todos Todos
	Context domain.ContextStore; Notifier Notifier; Installed func(string) bool
	Now func() time.Time; Poll time.Duration // default 5s
}
type StartRequest struct { WorkerID, Agent string; IfEnabled bool }
func NewManager(cfg Config, d Deps) *Manager
func (m *Manager) Start(ctx context.Context, req StartRequest) (Review, error)
func (m *Manager) Status(id string) (Review, error)
func (m *Manager) LatestFor(workerID string) (Review, bool)
var ErrReviewerSession = errors.New("cannot verify a reviewer session")
```

- [ ] **Step 1: Write the failing tests**

Fakes: in-memory `Sessions`, `Terminal`, `Notifier`, `Todos`; a real git repo from Task 3's helper as the worker worktree.

```go
func TestStartIfEnabledDisabled(t *testing.T)            // BeforePR false, IfEnabled → Review{Status: StatusDisabled}, nothing created
func TestStartRejectsReviewerSession(t *testing.T)       // worker.Role reviewer → ErrReviewerSession
func TestStartNoReviewer(t *testing.T)                   // ErrNoReviewer
func TestStartCreatesHiddenChild(t *testing.T)
// eventually (poll ≤ 2s) CreateChild got cfg with Role reviewer, ParentID worker.ID, Group worker.Group,
// PreparedWorktree a sibling "<repo>@review-<first 8 of review id>", PromptFree true, IssueKey "",
// InitialPrompt == InitialPrompt, Agent.Command == picked agent;
// the worktree holds .aiman_task.md (BuildBrief output) at the worker's relative working dir;
// record status running with ReviewerID set.
func TestStartReturnsExistingReviewForSameTree(t *testing.T) // second Start with no edits returns the same ID, one CreateChild call
func TestStartFailsCleanly(t *testing.T)                // CreateChild error → status failed, Error set, review worktree removed
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/verify/ -run 'TestStart' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`Start` runs synchronously only what is fast: load the worker, reject reviewers, honour `IfEnabled`, pick the agent, `TakeSnapshot`. If a stored review for this worker has the same `Tree` and is active, complete or incomplete, return it. Otherwise persist a `starting` record and launch a goroutine (on a background context owned by the manager, not the request context) that resolves the base from `worker.BaseBranch`, builds the diff and brief, adds the detached worktree beside `worker.WorktreePath`, writes `.aiman_task.md` there (worker's task read from `<worker.WorkingDirectory>/.aiman_task.md`; to-dos from `Todos.Get(worker.ID)`), calls `CreateChild`, records `running`, and starts the watcher from Task 8. Session name: `<worker.Name>-review-<4 hex>`. The worker's `Directory` relative to its worktree is carried to the reviewer. `Status` and `LatestFor` read the store.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/verify/ -run 'TestStart' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/verify/manager.go internal/verify/manager_test.go
git commit -m "feat: start reviewer sessions on a worktree snapshot"
```

---

### Task 8: Completion, findings to to-dos, cleanup, resume

**Files:**
- Modify: `internal/verify/manager.go`
- Test: `internal/verify/manager_complete_test.go`

**Interfaces:**
- Produces: `func (m *Manager) Resume(ctx context.Context)`; `func (m *Manager) Close()` (stops watchers).
- Consumes: `Todos.Mutate(…, domain.AuthorVerifier, …)`. In serve this is nl-1's `Server.TodoAccess()`, so a verifier mutation renders `.aiman_todo.md`, publishes `todo_changed` and steers the worker through the normal delivery gate.

- [ ] **Step 1: Write the failing tests**

```go
func TestWatchCompletesOnMarker(t *testing.T)
// write .aiman_review.md with one REQUIRED and one SUGGESTION and the marker →
// status complete, Required 1, Suggestions 1;
// fake Todos saw author domain.AuthorVerifier and the worker list gains one pending item, Text "Review: Nil map write (internal/a.go:42)", Ref "finding:<id>/f1";
// Context.Put got Type ContextTypeReview, Namespace "group", Key worker.Group, SessionID worker.ID, Body == file content;
// Notifier.ReviewFinished got (worker, review with Required 1, Suggestions 1);
// Terminal.Stop called for the reviewer; after Alive turns false: worktree removed, Sessions.Forget(reviewer), CleanedUp true.
func TestWatchProcessExitWithoutMarkerIsIncomplete(t *testing.T) // Alive false, no marker → incomplete, partial findings still added, cleanup runs
func TestWatchProcessExitUsesAgentEnded(t *testing.T)            // reviewer AgentEnded true counts as exit
func TestWatchTimesOut(t *testing.T)                            // Timeout 50ms → timed_out, Stop, cleanup
func TestCompleteSkipsDuplicateOpenFinding(t *testing.T)        // an open verifier item with the same text exists → no new item
func TestResumeWatchesRunningReviews(t *testing.T)              // stored running review; Resume; marker appears → complete
func TestResumeCleansFinishedButNotCleaned(t *testing.T)        // stored complete, CleanedUp false, Alive false → worktree removed
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/verify/ -run 'Watch|Complete|Resume' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

The watcher ticks every `Poll`: read `<reviewer worktree + rel dir>/.aiman_review.md`; complete when `ParseReview` reports the marker. Otherwise the reviewer has exited when `Sessions.Get(reviewer).AgentEnded` or `!Terminal.Alive`. Timeout counts from `StartedAt` (`Config.Timeout`, default `DefaultTimeout`). Completion order, as the spec lists it: parse; one `Mutate` per review that adds every REQUIRED finding not already open; `Context.Put` with `Type: domain.ContextTypeReview`, `Title: "Review of <worker name>: <n> required, <m> suggestion(s)"`; `Notifier.ReviewFinished(…, review)` for every finished review (complete, incomplete, timed out); persist; `Terminal.Stop`; then poll `Alive` (up to 30 s, every `Poll`) before `RemoveWorktree` and `Sessions.Forget`. A cleanup that cannot confirm the process is gone leaves `CleanedUp` false for `Resume` to retry.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/verify/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/verify/manager.go internal/verify/manager_complete_test.go
git commit -m "feat: turn reviewer findings into worker to-dos and clean up"
```

---

### Task 9: Serve wiring and socket methods

**Files:**
- Create: `internal/server/handlers_verify.go`
- Modify: `internal/server/server.go:24-45` (field), `:121-188` (dispatch cases)
- Modify: `cmd/aiman/serve.go:52-100`
- Test: `internal/server/handlers_verify_test.go`

**Interfaces:**
- Consumes: `verify.Manager` (Tasks 7-8), nl-1's `TodoAccess()` and `s.hub`, nl-5's `NotifyRecorder`.
- Produces:
  - `func (s *Server) SetVerifier(v *verify.Manager)`
  - `verify.start` params `{session?: string, agent?: string, if_enabled?: bool}`; `session` defaults to `req.Caller`. Result `{"type":"review","review":<Review JSON>}`.
  - `verify.status` params `{review?: string, session?: string}`; `review` wins, else the latest review for `session`. Same result shape; `not_found` when none.
  - Error codes: `invalid_params` for a reviewer target, new `CodeNoReviewer = "no_reviewer_agent"`.
  - Adapters in this file: `serverSessions` (Get via `liveSession`; `CreateChild` calls `s.create.CreateSession` then sets Name/Group/ParentID/Role and `s.repo.Save`; `Forget` as `handleForget`, `handlers_session.go:708-727`), `serverTerminal` (pty: `s.pty.Get` status `running` / `s.pty.Kill`; tmux: local `tmux has-session -t` / `tmux kill-session -t` through `s.remote.Execute`), `serveNotifier.ReviewFinished` (always publishes `SessionEvent{Type: "verify", ID: worker.ID, State: string(r.Status)}` on `s.hub`; when `r.Required > 0` also `s.NotifyRecorder().Emit(ctx, w.ID, notify.Label(w.IssueKey, w.Name, w.ID), domain.NotifyReviewDone, "<n> required, <m> suggestion(s)")`; otherwise `log.Printf("verify: %s %s, no required findings", w.ID, r.Status)` and no notification). To-dos use nl-1's `s.TodoAccess()` directly; no adapter here.
  - `rpcMethods` (nl-1 Task 3a) gains `verify.start`, `verify.status`.

- [ ] **Step 1: Write the failing tests**

With `startTestServer` (`internal/server/handlers_session_test.go:15`) plus `SetVerifier` over fakes:

```go
func TestVerifyStartDefaultsToCaller(t *testing.T)   // Call with Caller = worker id, no session → review.worker_id == caller
func TestVerifyStartReviewerTarget(t *testing.T)    // target with Role reviewer → error code invalid_params
func TestVerifyStatusLatestForSession(t *testing.T) // two reviews → status by session returns the newer
func TestVerifyStartReturnsQuickly(t *testing.T)    // CreateChild blocks 40s in the fake; Call returns < 5s with status starting
func TestVerifyNotConfigured(t *testing.T)          // no SetVerifier → invalid_params "verify not available"
func TestServeNotifierRequiredFindingsNotify(t *testing.T) // Required 1 → one review_done Emit and one verify hub event
func TestServeNotifierCleanPassIsQuiet(t *testing.T)       // Required 0 → no Emit; verify hub event still published
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/server/ -run Verify -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `runServe`, after `srv := server.New(…)` (`cmd/aiman/serve.go:97`) and nl-1's `srv.EnableTodos`: build `verify.NewManager(verify.Config{Agents: cfg.Verify.Agents, BeforePR: cfg.Verify.BeforePR, Timeout: cfg.Verify.Timeout}, deps)` with `Todos: srv.TodoAccess()`, `verify.NewStore(filepath.Join(dir, "reviews"))`, `Installed: agent.NewScanner(localExec).Installed` bound to `ctx`, then `srv.SetVerifier(m)`, `m.Resume(ctx)`, `defer m.Close()`.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/server/ -run Verify -v && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/handlers_verify.go internal/server/handlers_verify_test.go internal/server/server.go cmd/aiman/serve.go cmd/aiman/rpc_cmd.go cmd/aiman/rpc_cmd_test.go
git commit -m "feat: serve verify.start and verify.status"
```

---

### Task 10: `aiman verify` CLI

**Files:**
- Create: `cmd/aiman/verify_cmd.go`
- Modify: `cmd/aiman/main.go:43-50` (exit path), `:72-80` (early socket-only dispatch)
- Test: `cmd/aiman/verify_cmd_test.go`, `cmd/aiman/socket_only_dispatch_test.go`

**Interfaces:**
- Produces: `aiman verify [--session NAME] [--agent NAME] [--wait] [--if-enabled] [--timeout DUR] [--json]`; `var errVerifyNotPassed = errors.New("verify: not passed")`; `func runVerify(args []string) error`; `func waitForReview(call func(method string, params any) (server.Response, error), id string, timeout, every time.Duration, out io.Writer) error`.

- [ ] **Step 1: Write the failing tests**

```go
func TestWaitExitCodePassed(t *testing.T)     // statuses running, running, complete/0 required → nil; stdout has "verify: passed"
func TestWaitExitCodeRequired(t *testing.T)   // complete/1 required → errVerifyNotPassed; stdout lists "[REQUIRED] <title> (<location>)" and body
func TestWaitExitCodeIncomplete(t *testing.T) // incomplete → errVerifyNotPassed; stdout "review incomplete"
func TestWaitTimeout(t *testing.T)            // never completes, timeout 30ms → errVerifyNotPassed; stdout "still running; run `aiman verify --wait` again"
func TestWaitDisabled(t *testing.T)           // start returns disabled → nil, stdout "verify: disabled"
// and add {"verify"} to the table in TestSocketOnlyCommandsSkipTheDatabase (cmd/aiman/socket_only_dispatch_test.go:30-35)
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./cmd/aiman/ -run 'Wait|Verify' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`runVerify` calls `verify.start`; without `--wait` it prints the review JSON. With `--wait` it polls `verify.status {review: id}` every 5 s until a non-active status or `--timeout` (default `9m`, so it fits an agent's shell-tool limit; a rerun re-attaches by tree). Default output is plain text; `--json` prints the final review JSON. `main()` skips its `Error:` line for `errVerifyNotPassed` like `errUsage`, so the exit code is 1 with only the findings on stdout. Add `case "verify": return runVerify(parsed.Rest[1:])` to the early switch.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./cmd/aiman/ -v -run 'Wait|Verify|SocketOnly'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/aiman/verify_cmd.go cmd/aiman/verify_cmd_test.go cmd/aiman/main.go cmd/aiman/socket_only_dispatch_test.go
git commit -m "feat: add aiman verify with --wait"
```

---

### Task 11: TUI key, `reviewing…`, hidden reviewers

**Files:**
- Create: `internal/usecase/verify_remote.go`, `internal/ui/verify.go`
- Modify: `internal/ui/dashboard.go:784-808` (help), `:4527,4537` (blocked-key lists), `:661-668` (`applyRemoteFilter`), `:5051-5076` (`handleSessionManageKey`, after the daemons block)
- Modify: nl-2's `renderSessionDetail` caller (fills `detailMetaExtras.Reviewing`), `applySessionEvent` (new `verify` branch, handled before the activity path, never queues a preview)
- Test: `internal/usecase/verify_remote_test.go`, `internal/ui/verify_test.go`

**Interfaces:**
- Produces:
  - `func usecase.StartVerifyOnRemote(ctx context.Context, remote TerminalExecutor, sessionID string) (status string, err error)`: `ServeRPC(ctx, remote, "verify.start", {session: id})` and returns the review status.
  - `func (m *Model) startVerify() (tea.Model, tea.Cmd, bool)`; message `verifyStartedMsg{sessionID, status string; err error}`
  - `func reviewerRunning(workerID string, all []domain.Session) bool`: true when a session with `Role == reviewer`, `ParentID == workerID` and `!AgentEnded` exists.

- [ ] **Step 1: Write the failing tests**

```go
func TestStartVerifyOnRemoteParams(t *testing.T)  // fake rpc records verify.start {session: "s1"}; returns "starting"
func TestVerifyKeyStartsReview(t *testing.T)     // newTestModelWithSession; handleSessionManageKey(pressRune('V')) handled, cmd != nil, toast "Review starting…"
func TestVerifyKeyIgnoredOnDaemonsTab(t *testing.T)
func TestVerifyKeyBlockedWhileCreating(t *testing.T) // creatingSessions entry → the "still being created" toast
func TestReviewerRunning(t *testing.T)          // table: live reviewer child → true; ended child, other parent, no child → false
func TestMetaLineShowsReviewing(t *testing.T)   // renderSessionDetail for a worker with a live reviewer child contains "reviewing…"
func TestVerifyEventDropsReviewerRows(t *testing.T) // verify event (any state, incl. a clean pass) for w1 removes reviewer rows with ParentID w1
func TestApplyRemoteFilterHidesReviewers(t *testing.T) // reviewer session absent from m.list.Items()
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/usecase/ ./internal/ui/ -run 'Verify|Reviewer|Reviewing|HidesReviewers' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`V` (shift+v; `v` is VS Code at `dashboard.go:4821`, and `V` is unbound in the main view and in the bubbles list keymap) resolves the selected session's remote with `remoteByHost` (`internal/ui/session_events.go:343`), builds `ssh.NewManager`, and calls `StartVerifyOnRemote` in a command. Errors toast. Success toasts once (`Review starting…`) since the user pressed the key. Add `"V"` to both blocked-key lists and a help binding `V  verify with a different agent`. `applyRemoteFilter` skips `Role == reviewer`. Where nl-2 reads `m.metaExtras[s.ID]` for the detail pane, set `Reviewing = reviewerRunning(s.ID, m.allSessions)`; nl-2's `renderMetaLine` prints `reviewing…`. When a `verify` event arrives (published for every finished review, clean or not), drop reviewer rows whose `ParentID` is that session from `m.allSessions` so the marker clears even for tmux reviewers that emit no `session_gone`.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/usecase/ ./internal/ui/ -v -run 'Verify|Reviewer|Reviewing|HidesReviewers'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/usecase/verify_remote.go internal/usecase/verify_remote_test.go internal/ui/verify.go internal/ui/verify_test.go internal/ui/dashboard.go
git commit -m "feat: start a verifier from the dashboard and show reviewing"
```

---

### Task 12: Skill text and release check

**Files:**
- Modify: `internal/aimanskill/SKILL.md` (new section before `## Safety`, line 128)
- Test: `internal/aimanskill/install_test.go`

- [ ] **Step 1: Write the failing test**

`TestSkillDescribesVerify`: `aimanskill.Text` contains `aiman verify --wait --if-enabled` and `gh pr create`.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/aimanskill/ -run TestSkillDescribesVerify -v`
Expected: FAIL.

- [ ] **Step 3: Add the section (exact copy)**

```markdown
## Before you open a pull request

Run this before every `gh pr create`:

    aiman verify --wait --if-enabled

- Exit 0: open the PR. `verify: disabled` means this host does not require review.
- Exit 1 with findings: each REQUIRED finding is also on your to-do list. Fix them, then run the command again.
- Exit 1 with "still running": run the same command again. It waits on the same review.

Do not run `aiman verify` from a reviewer session.
```

- [ ] **Step 4: Run the full gate**

Run: `go test ./internal/aimanskill/ -v && make ci`
Expected: PASS apart from `TestOllamaIntelligenceSummariseSession`.

- [ ] **Step 5: Commit**

```bash
git add internal/aimanskill/SKILL.md internal/aimanskill/install_test.go
git commit -m "docs: tell agents to verify before opening a PR"
```

---

## Spec deviations

1. **Prompt goes in a file.** The spec puts diff, task, to-dos and format in the prompt. Prompts are delivered as one line (`flow_manager.go:48-52`), so the brief is the reviewer worktree's `.aiman_task.md` and the prompt only points at it. The diff is capped at 150 KB with the stat plus a `git diff` command beyond that.
2. **`incomplete` and `timed_out` fail `--wait`.** The spec gives exit 0 for "no REQUIRED findings". A reviewer that died or timed out has produced none, so this plan exits 1. Added a 45 min `verify.timeout`.
3. **`--wait` defaults to a 9 min client timeout** and reruns re-attach by snapshot tree hash, because agent shell tools cut long commands off.
4. **`--if-enabled`** implements "automatically when `verify.before_pr: true`" with fixed skill text: the agent always runs it and serve decides.
5. **Review state lives in `~/.aiman/reviews/*.json`**, not serve's SQLite. The spec's DB list does not name reviews; files match the to-do store.
6. **Depends on W6b's note type.** The spec orders W5 before W6b, but `domain.ContextTypeReview` comes from nl-3b, so nl-3b must land first (plan numbering already puts it earlier).
7. **`reviewing…` is derived from a live reviewer child** in the session list, not a new event type. `SessionInfo` gains `role`.
8. **`review_done` only for REQUIRED findings.** A clean pass (or one with only suggestions) is logged by serve and stored as a `review` note, with no toast or push; the noise budget lists "review findings". Every finished review publishes a `verify` hub event so the TUI can clear `reviewing…` without a notification.
9. **Finding to-dos use `Ref = "finding:<review id>/<finding id>"`** (nl-1 prefix table), distinct from nl-6's `review:<comment id>` for PR review comments.

---

## Research amendments — 2026-10-26

Source: `docs/superpowers/research/2026-10-26-harness-research.md` §What the
research adds (3) and the feed's "writer ≠ judge" / "done is structural" posts
(2026-10-12, 2026-07-16, 2026-08-27). These tasks make a pass mean something
more than "no findings were written".

### Task 13: Evidence record and pass criteria

**Files:**
- Modify: `internal/verify/review.go`, `internal/verify/manager.go`, `internal/verify/brief.go`
- Create: `internal/verify/evidence.go`, `internal/verify/evidence_test.go`
- Modify: `internal/infra/config/config.go` (`VerifyConfig.TestCommand`, `Reproduce`), `cmd/aiman/verify_cmd.go`
- Test: `internal/verify/manager_complete_test.go`, `internal/verify/review_test.go`

**Interfaces:**
```go
type Evidence struct {
    Command  string
    ExitCode int
    Output   string    // last 200 lines, verbatim
    RanAt    time.Time
    TreeHash string
}
// Review adds TreeHash, BaseSHA, Evidence.
func (r Review) Passed() bool // complete && Required == 0 && Evidence.Command != "" && Evidence.ExitCode == 0
```
`verify.test_command` (global) and `repos.<slug>.verify_command` (per repo)
name the command. The manager, not the reviewer, runs it host-side in a second
detached worktree at the same snapshot commit, after the reviewer stops and
before cleanup. The reviewer never supplies the evidence; a reviewer that edits
its worktree cannot fake a pass. If no command is configured, `Passed()` is
false and `--wait` prints `no test command configured` — an unverifiable review
is not a pass.

`--wait` prints `evidence: <command> → exit <n>` plus the last 20 output lines
when it fails. `Review.Passed` (the field) is replaced by `Passed()`; update all
callers, including nl-9's `BeforePR` seam.

- [ ] **Step 1: Write the failing tests**
  - `TestPassedRequiresEvidence`: complete/0 findings/no command → false; complete/0/exit 0 → true; complete/0/exit 1 → false.
  - `TestManagerRunsConfiguredCommand`: a fake command writes `ok` and exits 0 → evidence recorded in the review JSON.
  - `TestManagerRunsInSecondWorktree`: the reviewer's worktree is untouched; the evidence worktree is removed.
  - `TestMissingCommandIsIncomplete`: no config → status `incomplete`, reason names the config key.
- [ ] **Step 2: Run** `go test ./internal/verify/ -run 'Passed|Evidence|ConfiguredCommand'` → FAIL.
- [ ] **Step 3: Implement.** Reuse the snapshot's commit object; `git worktree add --detach <tmp> <tree-ish>`, run the command with a 15-min cap and a 200-line output cap, remove the worktree in a `defer`. A command that cannot start is an `incomplete` review, not a crash.
- [ ] **Step 4: Run** the tests and `make ci` → PASS.
- [ ] **Step 5: Commit** `feat(verify): bind a pass to test evidence`

### Task 14: Structural before-PR gate and reproduce-first

**Files:**
- Create: `internal/agenthook/policy.go`, `internal/agenthook/policy_test.go`
- Modify: `internal/agenthook/merge.go`, `internal/agenthook/install.go`, `internal/agenthook/report.go`
- Modify: `internal/server/handlers_verify.go`, `internal/server/handlers_session.go` (`SessionInfo.Unprotected`), `internal/ui/a1_detail.go`
- Modify: `cmd/aiman/verify_cmd.go` (`--gate`)
- Test: `internal/agenthook/install_test.go`, `cmd/aiman/verify_cmd_test.go`, `internal/ui/a1_detail_test.go`

**Behaviour:**
- `aiman verify --gate` computes the current tree hash (the same temporary-index
  commit the review uses), calls `verify.status {tree_hash}`, and exits 0 when
  the latest review is `Passed()` or `verify` is disabled; otherwise exit 2 with
  one line naming the required findings. This is the hook entry point.
- `agenthook` registers a `PreToolUse` hook for Claude (and Codex only when its
  hook surface supports a pre-tool event) matching the Bash tool whose command
  contains `gh pr create`. Exit 2 blocks the call and Claude feeds stderr back
  to the model. The hook is installed only when `verify.before_pr` is true, and
  is marked with its own marker so an update does not duplicate it.
- Capability matrix: Claude supports `PreToolUse` today. Codex is registered
  only after Task 1 of this plan (or a spike) confirms its hook surface exposes
  a pre-tool event; until then Codex is `unprotected`, not assumed. Agents
  without support get no hook; `SessionInfo.Unprotected` is true for them when
  `before_pr` is on, and the meta line shows `verify advisory`. Never pretend a
  guard exists.
- Reproduce-first: when `verify.reproduce` is true (or the issue type is `Bug`
  and the flag is unset), the manager runs `verify.test_command` on a worktree
  at `BaseSHA` **before** the reviewer starts. If it exits 0, the bug is not
  reproduced; the review ends `incomplete` with one REQUIRED finding
  `not reproducible` and no reviewer is spawned. If the command is missing,
  skip with a logged warning, not a failure.
- `PreToolUse` denial is appended to the ledger as `verify.recorded` with
  `kind=gate` (nl-11 T1), so a blocked PR is auditable.

- [ ] **Step 1: Write the failing tests**
  - `TestGateDeniesWithoutPass`: no review → exit 2, message names `aiman verify --wait`.
  - `TestGateAllowsPassedReview`: `Passed()` → exit 0.
  - `TestGateDisabledIsZero`: `before_pr: false` → exit 0 without a review.
  - `TestPreToolUseInstalledForClaudeAndCodex`: hook file contains the marker and the `gh pr create` matcher; a second install does not duplicate.
  - `TestUnprotectedWhenNoHook`: an agent without PreToolUse and `before_pr: true` → `SessionInfo.Unprotected`, meta line `verify advisory`.
  - `TestReproduceFirstIncompleteWhenBasePasses`: base run exits 0 → incomplete, one REQUIRED `not reproducible`, no reviewer session.
- [ ] **Step 2: Run** `go test ./internal/agenthook/ ./cmd/aiman/ ./internal/ui/ ./internal/verify/ -run 'Gate|PreToolUse|Unprotected|Reproduce'` → FAIL.
- [ ] **Step 3: Implement.** The hook script is generated like the existing hooks (`internal/agenthook/script.go`) and calls `aiman verify --gate`; it must finish under the 30 s socket budget. The gate uses the same tree-hash helper as the manager; do not reimplement the commit logic.
- [ ] **Step 4: Run** the tests and `make ci` → PASS.
- [ ] **Step 5: Commit** `feat(agenthook): gate PR creation on a passing review`
