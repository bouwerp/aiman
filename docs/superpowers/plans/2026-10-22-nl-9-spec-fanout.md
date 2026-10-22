# W8 Spec Mode and Fan-out Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A JIRA ticket can start a planner session that writes `docs/specs/<ISSUE>.md` plus independent to-dos, and `F` fans selected to-dos out to worker child sessions whose merged PRs tick the planner's list.

**Architecture:** A wizard mode "Spec" creates a session with `Role: planner` and a planner prompt. A new `internal/fanout` package, run inside `aiman serve`, validates a fan-out, creates worker children sequentially on branches cut from the planner branch, links each planner to-do to its child through `TodoItem.Ref = "child:<id>"`, and polls children's PRs every 2 minutes to mark items done through nl-1's `Server.TodoAccess()` (author `aiman`, which steers the planner). The TUI opens a picker on `F` and calls `spec.fanout` through nl-1's `aiman rpc` (`usecase.ServeRPC`).

**Tech Stack:** Go 1.26, Bubble Tea v2, `gh` CLI on the remote, newline JSON over `~/.aiman/aiman.sock`.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (W8, Constraints, Noise budget, Shared interfaces).

**Depends on:**
- nl-1 (W0 + W1): `domain.Session.BaseBranch`, `session.register` (a planner created by the laptop's local fallback reaches serve this way), `domain.TodoItem.Ref` and the `child:` prefix, `domain.AuthorAiman`, `todo.OpRef` (a Ref-only change that is stored and never steers; owned by nl-1 Task 7/9/11), `(*Server).TodoAccess()`, `aiman rpc`/`usecase.ServeRPC`.
- nl-2 (A1 dashboard): `m.todos map[string]todoSnapshot`, `rowGlyph(it item)`, `todoView`, `renderTodoLines`.
- nl-5 (W2): `(*Server).NotifyRecorder().Emit`, `domain.NotifyError`, `notify.Label`.
- nl-7 (W5): `domain.SessionRole`, `Session.Role`, `SessionInfo.Role`, `verify.Manager.LatestFor`, `verify.Review.Passed`, `config.VerifyConfig.BeforePR`.

## Global Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only. Key tests use `pressKey`/`pressRune` (`internal/ui/keystrokes_test.go:16,32`).
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- No socket method blocks longer than 30 s. Child creation (`CreateSession` can take over 20 s each, `flow_manager.go:467-470`) runs after `spec.fanout` returns.
- Both terminal backends (tmux, pty) must work.
- Serve is the only writer of to-dos. The TUI and CLI go through the socket.
- Initial prompts are one line (`flow_manager.go:48-52`).
- `internal/fanout` must not import `internal/server` or `internal/gateway`.
- At most one to-do is `active` (W1 store rule). A fanned-out item stays `pending` until done; its ◐ comes from the child's state.
- Spec file path: `docs/specs/<ISSUE>.md`, committed on the planner branch. Children PR into the planner branch; the planner opens the final PR to the default branch.
- No new header or row fields. The fan-out picker is a modal like the other pickers.

## Review Focus

1. **`F` pressed twice, or a second fan-out while children are still being created.** Each to-do must get exactly one child. Test: `TestFanOutRejectsInFlightItem` (Task 5).
2. **Planner branch not pushed, or spec not committed.** Children's PRs need the base on GitHub and the spec in their tree. Fan-out must refuse with a message that says which. Tests: `TestFanOutRequiresPushedBranch`, `TestFanOutRequiresCommittedSpec` (Task 5).
3. **Child PR merged into a different base** (for example straight to `main`). It must not tick the planner item. Test: `TestTrackIgnoresOtherBase` (Task 6).
4. **Serve restarts while children work.** Tracking must resume from the planner lists' `Ref` values with no other state. Test: `TestTrackResumesFromRefs` (Task 6).
5. **Child session forgotten or create failed.** The item must become fan-out-able again instead of pointing at nothing. Tests: `TestTrackClearsRefOfMissingChild` (Task 6), `TestFanOutCreateFailureNotifies` (Task 5).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/domain/session.go` (modify) | `SessionRolePlanner`, `SessionRoleWorker` |
| `internal/usecase/spec_prompts.go` | `SpecPath`, `PlannerPrompt`, `WorkerPrompt`, `ChildBranch` |
| `internal/usecase/flow_manager.go` (modify) | planner prompt injection |
| `internal/usecase/session_create_remote.go`, `internal/ui/create_remote.go` (modify) | `role` across remote create |
| `internal/server/handlers_session.go` (modify) | `createParams`, `createFromParams`, `role` param |
| `internal/ui/dashboard.go` (modify) | mode `[6] Spec`, key wiring, help |
| `internal/fanout/fanout.go` | `Manager.FanOut` validation and child creation |
| `internal/fanout/track.go` | completion tracking |
| `internal/fanout/pr.go` | `gh` PR probe |
| `internal/server/handlers_spec.go` | `spec.fanout`, adapters |
| `cmd/aiman/rpc_cmd.go`, `cmd/aiman/serve.go` (modify) | allowlist `spec.fanout`; wiring |
| `internal/usecase/fanout_remote.go` | laptop-side `FanOutOnRemote` over `ServeRPC` |
| `internal/ui/fanout.go` | `F` picker, child glyph on to-do items |

---

### Task 1: Roles across remote create

**Files:**
- Modify: `internal/domain/session.go` (constants next to nl-7's `SessionRoleReviewer`)
- Modify: `internal/usecase/session_create_remote.go:13-32` (`RemoteCreateSpec`), `:50-73` (params), `:127-177` (result)
- Modify: `internal/ui/create_remote.go:20-40` (`remoteCreateSpecFrom`)
- Modify: `internal/server/handlers_session.go:548-575` (params), `:615-632` (cfg)
- Test: `internal/usecase/session_create_remote_test.go`, `internal/ui/create_remote_test.go`, `internal/server/handlers_session_test.go`

**Interfaces:**
- Produces: `SessionRolePlanner SessionRole = "planner"`, `SessionRoleWorker SessionRole = "worker"`; `RemoteCreateSpec.Role string`; wire param `"role"`; `parseRemoteCreateResult` sets `Session.Role` from `result.session.role`.

- [ ] **Step 1: Write the failing tests**

`TestCreateSessionOnRemoteSendsRole` (params file JSON has `"role":"planner"`), `TestParseRemoteCreateResultRole` (`{"result":{"session":{"id":"x","role":"planner"}}}` → `Role == planner`), `TestRemoteCreateSpecFromCarriesRole`, and in serve `TestCreatePassesRole` (fakeCreator, `internal/server/fake_remote_test.go:62-81`, records `cfg.Role == planner`; saved row has `Role == planner`).

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/usecase/ ./internal/ui/ ./internal/server/ -run Role -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add the constants, the spec field, the `"role"` entry in the param map, `Role string \`json:"role"\`` in the result struct, and `Role string \`json:"role"\`` in `handleCreate`'s params mapped to `cfg.Role` and `sess.Role` before Save.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/usecase/ ./internal/ui/ ./internal/server/ -run Role -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/session.go internal/usecase/session_create_remote.go internal/usecase/session_create_remote_test.go internal/ui/create_remote.go internal/ui/create_remote_test.go internal/server/handlers_session.go internal/server/handlers_session_test.go
git commit -m "feat: carry planner and worker roles through remote create"
```

---

### Task 2: Planner and worker prompts

**Files:**
- Create: `internal/usecase/spec_prompts.go`
- Modify: `internal/usecase/flow_manager.go:357`
- Test: `internal/usecase/spec_prompts_test.go`

**Interfaces:**
- Produces:
  - `func SpecPath(issueKey string) string` → `"docs/specs/" + issueKey + ".md"`
  - `func PlannerPrompt(issueKey string) string`
  - `type WorkerPromptInput struct { SpecPath, PlannerBranch, ItemID, ItemText string }`
  - `func WorkerPrompt(in WorkerPromptInput) string`
  - `func ChildBranch(plannerBranch, itemID string) string` → `domain.SanitizeBranchName(plannerBranch + "-" + itemID)`
  - `func plannerPromptFor(cfg domain.SessionConfig) string` (empty unless `cfg.Role == planner`)

- [ ] **Step 1: Write the failing tests**

```go
func TestPlannerPrompt(t *testing.T) {
	p := PlannerPrompt("PROJ-12")
	// single line (no "\n"); contains "docs/specs/PROJ-12.md", "git push -u origin HEAD", "aiman todo add",
	// "independently buildable", "Do not implement", "git pull", "default branch"
}
func TestWorkerPrompt(t *testing.T) {
	p := WorkerPrompt(WorkerPromptInput{SpecPath: "docs/specs/PROJ-12.md", PlannerBranch: "proj-12-spec", ItemID: "t3", ItemText: "Add rollback\ntest"})
	// single line; contains "t3", "Add rollback test", "docs/specs/PROJ-12.md", "gh pr create --base proj-12-spec"
}
func TestChildBranch(t *testing.T)      // ("proj-12-spec", "t3") → "proj-12-spec-t3"
func TestPlannerPromptForRole(t *testing.T) // planner cfg with IssueKey → PlannerPrompt; worker or empty role → ""
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/usecase/ -run 'PlannerPrompt|WorkerPrompt|ChildBranch' -v`
Expected: FAIL.

- [ ] **Step 3: Implement (exact copy)**

```go
func PlannerPrompt(issueKey string) string {
	spec := SpecPath(issueKey)
	return "You are the planner for " + issueKey + ". Do not implement the ticket yourself. " +
		"1) Read .aiman_task.md and the codebase, then write the spec to " + spec + ". " +
		"2) Commit it on this branch and push with `git push -u origin HEAD`. " +
		"3) Replace your to-do list with one item per independently buildable task using `aiman todo add \"<task>\"`; each item must build, test and review on its own. " +
		"4) Stop and wait. The user fans items out to worker sessions whose PRs target this branch. " +
		"5) When aiman marks an item done, run `git pull`. " +
		"6) When every item is done, open the PR from this branch to the default branch."
}

func WorkerPrompt(in WorkerPromptInput) string {
	text := strings.Join(strings.Fields(in.ItemText), " ")
	return "You are a worker on the spec " + in.SpecPath + ", based on branch " + in.PlannerBranch + ". " +
		"Read " + in.SpecPath + " first. Your task is to-do " + in.ItemID + " of that spec: " + text + ". " +
		"Do only this task and keep your own to-do list with `aiman todo`. " +
		"When it is done, push your branch and open a PR into " + in.PlannerBranch + " with `gh pr create --base " + in.PlannerBranch + "`."
}
```

In `CreateSession`, before `:357`, add `sendKeysPrompt = joinPrompt(sendKeysPrompt, plannerPromptFor(config))`, so the planner text follows the JIRA "Read .aiman_task.md…" trigger and precedes the user's own prompt.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/usecase/ -run 'PlannerPrompt|WorkerPrompt|ChildBranch|JoinPrompt' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/usecase/spec_prompts.go internal/usecase/spec_prompts_test.go internal/usecase/flow_manager.go
git commit -m "feat: add planner and worker prompts for spec mode"
```

---

### Task 3: Wizard mode "Spec"

**Files:**
- Modify: `internal/ui/dashboard.go:3822-3826` (`renderModePicker`), `:5948-5992` (`handleModePickerUpdate`)
- Test: `internal/ui/spec_mode_test.go`

**Interfaces:**
- Consumes: `SessionRolePlanner` (Task 1).
- Produces: mode key `6`; `m.sessionCfg.Role == planner` through the JIRA issue flow.

- [ ] **Step 1: Write the failing tests**

```go
func TestModePickerSpecStartsIssuePicker(t *testing.T)
// state viewStateModePicker; handleModePickerUpdate(pressRune('6')) → state viewStateIssuePicker,
// m.sessionCfg.Role == domain.SessionRolePlanner, cmd != nil (JIRA search)
func TestModePickerRendersSpec(t *testing.T) // renderModePicker() contains "[6]" and "Spec"
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/ui/ -run 'ModePicker' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add the line `[6]  Spec                 — planner writes a spec and to-dos to fan out` after `[5]`. Case `"6"` is case `"1"` with `m.resetSessionCfg(domain.SessionConfig{Role: domain.SessionRolePlanner})`. The rest of the wizard keeps `sessionCfg` fields, so the role reaches `createSession` (`dashboard.go:1954`) and both create paths.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/ui/ -run 'ModePicker|EveryViewState' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/dashboard.go internal/ui/spec_mode_test.go
git commit -m "feat: add Spec mode to the new-session wizard"
```

---

### Task 4: Reusable serve-side create

**Files:**
- Modify: `internal/server/handlers_session.go:547-658`
- Test: `internal/server/handlers_session_test.go` (existing create tests must stay green)

**Interfaces:**
- Produces:

```go
type createParams struct { /* the fields of handleCreate's anonymous params struct, plus */ Role string `json:"role"`; TagIssue string `json:"-"` }
type createError struct{ Code, Message string }
func (e *createError) Error() string
func (s *Server) createFromParams(ctx context.Context, p createParams, caller string) (*domain.Session, error) // *createError on failure
```
`TagIssue` sets `IssueKey` on the saved session without handing the key to `CreateSession`, so no JIRA task file is written.

- [ ] **Step 1: Write the failing test**

`TestCreateFromParamsTagIssue`: `createFromParams(ctx, createParams{Agent: "claude", Repo: "o/r", Branch: "b", TagIssue: "PROJ-1"}, "")` → fakeCreator saw `cfg.IssueKey == ""`; the saved row has `IssueKey == "PROJ-1"`.

- [ ] **Step 2: Run it and see it fail**

Run: `go test ./internal/server/ -run TestCreateFromParamsTagIssue -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Move the body of `handleCreate` after unmarshalling into `createFromParams`, returning `*createError` with the codes it uses today (`invalid_params`, `name_taken`, `create_failed`). `handleCreate` unmarshals into `createParams` and maps the error back. Behaviour is unchanged.

- [ ] **Step 4: Run the server tests**

Run: `go test -race ./internal/server/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/handlers_session.go internal/server/handlers_session_test.go
git commit -m "refactor: extract serve-side session create for internal callers"
```

---

### Task 5: Fan-out validation and child creation

**Files:**
- Create: `internal/fanout/fanout.go`
- Test: `internal/fanout/fanout_test.go`

**Interfaces:**
- Consumes: `usecase.SpecPath`, `usecase.WorkerPrompt`, `usecase.ChildBranch` (Task 2); nl-1's `todo.OpRef` behaviour (a Ref-only `Mutate` is stored, bumps `Rev`, and never steers) and the `Server.TodoAccess()` shape.
- Produces:
  - `func ChildRef(id string) string` (`"child:" + id`) and `func ChildID(ref string) (string, bool)`.

```go
type ChildSpec struct {
	Name, Group, ParentID, Repo, Branch, BaseBranch, Agent, Dir, Prompt, IssueKey string
}
type Sessions interface {
	Get(ctx context.Context, id string) (domain.Session, error)
	List(ctx context.Context) ([]domain.Session, error)
	CreateChild(ctx context.Context, c ChildSpec) (domain.Session, error) // Role worker, persisted
}
type Git interface {
	SpecCommitted(ctx context.Context, worktree, branch, path string) bool // git cat-file -e <branch>:<path>
	BranchPushed(ctx context.Context, worktree, branch string) bool       // git ls-remote --exit-code --heads origin <branch>
}
type Todos interface { // same shape as verify.Todos and ciwatch.Todos; serve passes nl-1's srv.TodoAccess()
	Get(sessionID string) (domain.TodoList, error)
	Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error
}
type Notifier interface { FanOutFailed(ctx context.Context, planner domain.Session, detail string) }
type Deps struct { Sessions Sessions; Git Git; Todos Todos; Notifier Notifier; PRs PRProbe; Reviews Reviews; BeforePR bool; Poll time.Duration }
type Request struct { PlannerID string; ItemIDs []string; Agent string }
type Accepted struct { ItemID, Branch string }
func NewManager(d Deps) *Manager
func (m *Manager) FanOut(ctx context.Context, r Request) ([]Accepted, error)
var (
	ErrNotPlanner      = errors.New("fan-out needs a spec planner session")
	ErrSpecNotCommitted = errors.New("commit the spec on the planner branch first")
	ErrBranchNotPushed = errors.New("push the planner branch first: workers open PRs into it")
	ErrItemNotPending  = errors.New("only pending to-dos without a worker can be fanned out")
)
```
(`PRProbe` and `Reviews` are defined in Task 6.)

- [ ] **Step 1: Write the failing tests**

Fakes for every interface; `Todos` is a fake over a real nl-1 `todo.Files` in `t.TempDir()`. The Ref-only behaviour itself is pinned by nl-1's `TestDiffRefOnly`, `TestFormatSteerNoteSkipsRef` and `TestEnqueueTodoIgnoresRefOnly`.

```go
func TestFanOutRejectsNonPlanner(t *testing.T)       // ErrNotPlanner
func TestFanOutRequiresCommittedSpec(t *testing.T)   // ErrSpecNotCommitted, message names docs/specs/<ISSUE>.md
func TestFanOutRequiresPushedBranch(t *testing.T)    // ErrBranchNotPushed
func TestFanOutRejectsDoneOrLinkedItem(t *testing.T) // item done, or Ref set → ErrItemNotPending (whole request refused, nothing created)
func TestFanOutCreatesChildren(t *testing.T)
// planner {Name "proj-12", Group "PROJ-12", IssueKey "PROJ-12", Branch "proj-12-spec", Repo "o/r", AgentName "Claude Code",
// WorktreePath "/r/r@proj-12-spec", WorkingDirectory "/r/r@proj-12-spec/svc"}; items t3, t5 →
// returns Accepted{t3,"proj-12-spec-t3"},{t5,…}; eventually CreateChild twice, in order, with
// Name "proj-12-t3", Group "PROJ-12", ParentID planner.ID, BaseBranch "proj-12-spec", Agent "claude", Dir "svc",
// IssueKey "PROJ-12", Prompt == WorkerPrompt(...); planner items t3/t5 get Ref = ChildRef(child id) and stay pending
func TestFanOutRejectsInFlightItem(t *testing.T)    // CreateChild blocks; a second FanOut for t3 → ErrItemNotPending
func TestFanOutCreateFailureNotifies(t *testing.T)  // CreateChild error → item keeps empty Ref; Notifier.FanOutFailed(planner, "fan-out of t3 failed: …")
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/fanout/ -run 'TestFanOut' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`FanOut` validates synchronously (role, `IssueKey` present, spec committed, branch pushed, each item `pending` with empty `Ref` and not in the in-flight set), marks the items in flight, returns `Accepted`, and creates children one at a time in a goroutine on the manager's own context. The agent is `r.Agent`, else `agent.FindKnown(planner.AgentName).Command`, else `planner.AgentName`. `Dir` is the planner's `WorkingDirectory` relative to its `WorktreePath`. After each successful create: `Todos.Mutate(ctx, planner.ID, domain.AuthorAiman, set item.Ref = ChildRef(child.ID))`. Always clear the in-flight mark.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/fanout/ -run 'TestFanOut' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fanout/fanout.go internal/fanout/fanout_test.go
git commit -m "feat: fan planner to-dos out to worker sessions"
```

---

### Task 6: Completion tracking

**Files:**
- Create: `internal/fanout/track.go`, `internal/fanout/pr.go`
- Test: `internal/fanout/track_test.go`, `internal/fanout/pr_test.go`

**Interfaces:**
- Consumes: `verify.Review.Passed()`, `verify.Manager.LatestFor` (nl-7).
- Produces:

```go
type PRState struct { Number int; State string } // State: "OPEN" | "MERGED" | "CLOSED" | "" (none)
type PRProbe interface { ForBranch(ctx context.Context, dir, head, base string) (PRState, error) }
type Reviews interface { LatestFor(sessionID string) (verify.Review, bool) }
func NewGHProbe(run func(ctx context.Context, dir string, args ...string) (string, error)) PRProbe
func (m *Manager) TrackOnce(ctx context.Context)
func (m *Manager) Run(ctx context.Context) // TrackOnce now, then every Poll (default 2m)
func childDone(pr PRState, beforePR bool, review verify.Review, hasReview bool) bool
```

- [ ] **Step 1: Write the failing tests**

```go
func TestChildDone(t *testing.T) {
	// table: MERGED → true (any beforePR); OPEN + beforePR + passed review → true;
	// OPEN + beforePR + REQUIRED findings → false; OPEN + !beforePR → false; OPEN + beforePR + no review → false; "" → false
}
func TestGHProbeParses(t *testing.T)
// run fake returns `[{"number":7,"state":"MERGED","baseRefName":"proj-12-spec"}]` for
// args "pr list --head proj-12-spec-t3 --base proj-12-spec --state all --json number,state,baseRefName --limit 5" → {7, MERGED}
func TestTrackIgnoresOtherBase(t *testing.T)   // probe output has baseRefName "main" → PRState{} → not done
func TestTrackMarksDoneAndSteers(t *testing.T) // MERGED → fake Todos saw AuthorAiman and planner t3 state done
func TestTrackResumesFromRefs(t *testing.T)    // fresh Manager, planner list already has t3 Ref="child:<id>" → TrackOnce marks it done; an item with Ref "check:lint" is not probed
func TestTrackClearsRefOfMissingChild(t *testing.T) // Sessions.Get(ref) not found → Ref cleared, state pending
func TestTrackSkipsDoneAndDropped(t *testing.T) // no probe calls for done/dropped items
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/fanout/ -run 'Track|ChildDone|GHProbe' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`TrackOnce` lists sessions with `Role == planner`, reads each list, and for every `pending`/`active` item whose `Ref` parses with `ChildID` (other prefixes such as `check:` are ignored) loads the child, probes its PR in the child's `WorktreePath` against the planner's `Branch`, and on `childDone` calls `Todos.Mutate(ctx, planner.ID, domain.AuthorAiman, set state done)`. That mutation is the steering note to the planner (`✓ t3 done` plus the list). `NewGHProbe` keeps only entries whose `baseRefName` equals `base`, preferring `MERGED` over `OPEN`. The default `run` is `exec.CommandContext("gh", …)` with `Dir` set, stdout only.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/fanout/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fanout/track.go internal/fanout/track_test.go internal/fanout/pr.go internal/fanout/pr_test.go
git commit -m "feat: mark planner to-dos done when worker PRs land"
```

---

### Task 7: `spec.fanout`, CLI and serve wiring

**Files:**
- Create: `internal/server/handlers_spec.go`, `internal/usecase/fanout_remote.go`
- Modify: `internal/server/server.go:24-45`, `:121-188`; `cmd/aiman/rpc_cmd.go` (allowlist `spec.fanout`); `cmd/aiman/serve.go` (after nl-7's verifier wiring)
- Test: `internal/server/handlers_spec_test.go`, `cmd/aiman/rpc_cmd_test.go`, `internal/usecase/fanout_remote_test.go`

**Interfaces:**
- Produces:
  - `func (s *Server) SetFanout(f *fanout.Manager)`
  - `spec.fanout` params `{session?: string, items: []string, agent?: string}` (`session` defaults to `req.Caller`); result `{"type":"fanout","accepted":[{"item":"t3","branch":"…"}]}`. Errors: `invalid_params` with the `fanout.Err*` message.
  - No CLI subcommand: the laptop calls `spec.fanout` through `aiman rpc`.
  - `func usecase.FanOutOnRemote(ctx context.Context, remote TerminalExecutor, plannerID string, items []string) (int, error)`: `ServeRPC(ctx, remote, "spec.fanout", {session, items})`, returns the count accepted.
  - Adapter `serverFanoutSessions.CreateChild` calls `createFromParams` (Task 4) with `Role: "worker"`, `Parent`, `Base`, `Prompt`, `PromptFree: true`, `TagIssue`, and the planner as `caller` so `createGitRoot` (`internal/server/create_root.go:22`) places the worktree beside the planner's.

- [ ] **Step 1: Write the failing tests**

`TestSpecFanoutDefaultsToCaller`, `TestSpecFanoutMapsErrors` (`ErrBranchNotPushed` → `invalid_params` with that text), `TestSpecFanoutNotConfigured`, `TestSpecFanoutRequiresItems` (empty `items` → `invalid_params`), `TestFanOutOnRemoteParams` (fake rpc records `spec.fanout {session: "p1", items: ["t3","t5"]}`), and a `TestRPCParams` row for `spec.fanout`.

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/server/ ./cmd/aiman/ ./internal/usecase/ -run 'Fanout|FanOut|RPCParams' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `runServe`: `fm := fanout.NewManager(deps)` with `BeforePR: cfg.Verify.BeforePR`, `Reviews` = nl-7's verify manager, `Todos` = `srv.TodoAccess()` (nl-1), a `Notifier` whose `FanOutFailed` calls `s.NotifyRecorder().Emit(ctx, p.ID, notify.Label(p.IssueKey, p.Name, p.ID), domain.NotifyError, detail)`, a `Git` implementation over `exec` in the planner worktree; `srv.SetFanout(fm)`; `go fm.Run(ctx)`.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/server/ ./cmd/aiman/ ./internal/usecase/ -v -run 'Fanout|FanOut|RPCParams' && go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/handlers_spec.go internal/server/handlers_spec_test.go internal/server/server.go cmd/aiman/rpc_cmd.go cmd/aiman/rpc_cmd_test.go cmd/aiman/serve.go internal/usecase/fanout_remote.go internal/usecase/fanout_remote_test.go
git commit -m "feat: serve spec.fanout and track workers"
```

---

### Task 8: `F` picker and child state on planner items

**Files:**
- Create: `internal/ui/fanout.go`
- Modify: `internal/ui/dashboard.go:784-808` (help), `:4527,4537` (blocked-key lists), `:5051-5076` (`handleSessionManageKey`), the `viewState` enum (`:145` area) and its update/render switches (`:3067`, `:3483` area)
- Modify: nl-2's `todoView` / `renderTodoLines` (`internal/ui/a1_detail.go`)
- Test: `internal/ui/fanout_test.go`

**Interfaces:**
- Consumes: nl-2's `m.todos[id].List`, `rowGlyph(it item)`, `todoView`, `renderTodoLines`.
- Produces: `viewStateFanoutPicker`; `type fanoutPicker struct { plannerID string; items []domain.TodoItem; cursor int; picked map[string]bool }`; `func (m *Model) openFanoutPicker() (tea.Model, tea.Cmd, bool)`; `func (m *Model) handleFanoutPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd)`; `func (m *Model) renderFanoutPicker() string`; `fanoutDoneMsg{plannerID string; accepted int; err error}`; `func (m *Model) todoRefGlyph(ref string) (string, bool)`; `todoView.RefGlyph func(ref string) (string, bool)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestFanoutKeyOpensPickerForPlanner(t *testing.T)  // planner with pending t1 (no Ref), t2 (Ref set), t3 done → picker lists only t1
func TestFanoutKeyOnNonPlanner(t *testing.T)           // toast "F fans out a spec planner's to-dos", state unchanged
func TestFanoutKeyNoCandidates(t *testing.T)           // toast "No pending to-dos without a worker"
func TestFanoutPickerSelectAndConfirm(t *testing.T)    // space toggles, j/k move, enter with ≥1 picked → cmd != nil, state viewStateMain; enter with none picked does nothing; esc cancels
func TestFanoutKeyIgnoredOnDaemonsTab(t *testing.T)
func TestTodoRefGlyph(t *testing.T)                    // Ref "child:<busy id>" → rowGlyph's "◐"; unknown child or other prefix ("check:lint") → ok false
func TestRenderTodoLinesUsesRefGlyph(t *testing.T)    // pending t3 with Ref and RefGlyph returning "◐" renders "◐" in place of "○"
```

- [ ] **Step 2: Run them and see them fail**

Run: `go test ./internal/ui/ -run 'Fanout|RefGlyph' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`F` (shift+f) is unbound in the main view (only lowercase `f`, the remote filter, at `dashboard.go:4738`) and in the bubbles list keymap. The picker renders as a centred bordered box like `renderModePicker` (`dashboard.go:3810-3833`): one `[ ]`/`[x]` line per item (`t1  text`), then `space: pick · enter: fan out · esc: cancel`. Enter runs `usecase.FanOutOnRemote` over `ssh.NewManager` for the planner's remote (`remoteByHost`, `internal/ui/session_events.go:343`) and toasts `Fanned out <n> to-do(s)` or the error. `todoRefGlyph` parses the `child:` prefix, finds the child's `item` in `m.list.Items()` and returns `rowGlyph(it)`. `renderTodoLines` (nl-2) uses `v.RefGlyph` for a pending item with a `Ref`, in place of `○`. Add `"F"` to both blocked-key lists and a help binding `F  fan out planner to-dos`.

- [ ] **Step 4: Run them and see them pass**

Run: `go test -race ./internal/ui/ -v -run 'Fanout|RefGlyph|EveryViewState'`
Expected: PASS.

- [ ] **Step 5: Run the full gate and commit**

Run: `make ci`
Expected: PASS apart from `TestOllamaIntelligenceSummariseSession`.

```bash
git add internal/ui/fanout.go internal/ui/fanout_test.go internal/ui/dashboard.go
git commit -m "feat: fan out planner to-dos from the dashboard with F"
```

---

## Spec deviations

1. **Fanned-out items stay `pending`.** The spec says the planner item "shows ◐ while the child works". W1 allows one `active` item, so the ◐ is drawn from the child session's state through `Ref`, and the stored state changes only to `done`.
2. **Fan-out refuses an unpushed planner branch or an uncommitted spec.** The spec does not say what happens; workers' PRs cannot target a branch GitHub lacks.
3. **Children are created one at a time**, after `spec.fanout` returns, to respect the 30 s socket limit and avoid parallel clones on one host.
4. **Children get the ticket as a tag only** (`TagIssue`). Passing the issue key to `CreateSession` would write the whole JIRA ticket as their task; their task is the spec plus one to-do, delivered in the prompt.
5. **A forgotten child clears the item's `Ref`** so it can be fanned out again. A failed create raises an `error` notification (nl-5) on the planner.
6. **Planner-side `git pull` and final PR** are instructions in the planner prompt, not serve actions.
7. **Spec mode requires a JIRA issue.** `docs/specs/<ISSUE>.md` needs a key, so mode 6 starts from the issue picker.
8. **`F` keeps the spec's key.** It is free today and does not clash with nl-6's `w` (watch PR) or nl-2's `ctrl+p`/`ctrl+t`.
9. **`OpRef` lives in nl-1.** A Ref-only mutation must be stored (not dropped as a no-op) and must not steer; nl-1's change model owns that (`OpRef`, `Change.SteersAgent`). This plan only uses it.
10. **`Ref = "child:<session id>"`**, not a bare id, so planner lists can also hold `check:`/`review:`/`finding:` refs without the tracker mistaking them for children.
