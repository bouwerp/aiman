# W8 Coordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every session starts as the coordinator: a small task stays in that session, a larger one is split into child Aiman sessions chosen from an editable preferred-agent list, and a child's merged PR ticks the parent's to-do.

**Architecture:** The judgment of "simple" stays in the bundled skill. Go does not score scope. `agents.preferred` is an ordered list in `~/.aiman/config.yaml`, edited from the admin menu and copied to the remote by `MarshalServeConfig`. `session.create` with an empty `agent` launches the first preferred name installed on that host. `session.create` with `for` (a parent to-do id) cuts a child branch off the parent's branch, sets the parent's `Ref` to `child:<id>`, and returns an error instead of guessing an agent. A serve tracker, resumed from those refs, marks the item done when the child's PR merges into the parent's branch. There is no Spec wizard mode and no `F` key.

**Tech Stack:** Go 1.26, Bubble Tea v2, `gh` on the remote, the existing `session.create` socket method.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (W8).

**Depends on:**
- nl-1: `domain.Session.BaseBranch`, `domain.TodoItem.Ref`, `todo.OpRef` (a Ref-only change never steers), `domain.AuthorAiman`, `(*Server).TodoAccess()`, `session.create` (`internal/server/handlers_session.go` `handleCreate`).
- nl-2: `todoView`, `renderTodoLines`, `rowGlyph`.
- nl-5: `(*Server).NotifyRecorder().Emit`, `domain.NotifyError`, `notify.Label`.
- nl-7: `verify.Manager.LatestFor`, `verify.Review.Passed`. `verify.before_pr` is nl-10's flag. This plan takes a `BeforePR func(id string) bool` that returns false until nl-10 wires it.

Do not add `SessionRolePlanner` or `SessionRoleWorker`. A child is an ordinary session with `ParentID` set. `roles.verifier` stays nl-7 and nl-10. `roles.planner` and `roles.worker` are withdrawn.

## Global Constraints

- Go 1.26. Bubble Tea v2 only (`charm.land/...`). Key tests use `pressKey`/`pressRune`.
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure. Run `graphify update .` after code changes. Do not commit `graphify-out/` or agent skill dirs.
- Serve is the only writer of to-dos. A Ref-only write does not steer.
- No socket method blocks longer than 30 s. Child creation runs inside the existing `session.create` call, which already does.
- Both terminal backends stay supported. Copy `backend` onto the child the way create already copies it.
- Initial prompts stay one line.
- `internal/coordinator` must not import `internal/server`, `internal/ui`, or `internal/gateway`.
- At most one to-do is `active`. A split item stays `pending` until done. Its ◐ comes from the child.
- A shared spec path is `docs/specs/<ISSUE>.md`. One child does not need that file. Children PR into the coordinator's branch. The coordinator opens the final PR to the default branch. That last sentence is skill text, not a Go PR opener.
- Do not encode a line count, file count, or time limit for "simple". The skill states the rule. The agent decides.

## Review Focus

1. **A second `session.create --for t3` while t3 already has `Ref` `child:<id>`.** It must not create another child. Test: `TestCreateForRefusesLinkedItem` (Task 4).
2. **Preferred list is `codex, claude` and only `claude` is installed.** The child and a session with no explicit agent must be `claude`, not `codex` and not a hardcoded default. Test: `TestPickAgentSkipsMissing` (Task 2).
3. **The child PRs into `main` while the parent branch is `feat/x`.** The parent item must stay open. Test: `TestTrackIgnoresOtherBase` (Task 5).
4. **Serve restarts while the child works.** Tracking resumes from `Ref` alone. Test: `TestTrackResumesFromRefs` (Task 5).
5. **The user picked `grok` in the wizard, and `agents.preferred` starts with `claude`.** The new session stays `grok`. Its later children still use the list. Test: `TestExplicitAgentBeatsPreferred` (Task 2).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/infra/config/config.go` | `AgentsConfig`, on `Config` and `ServeConfig` |
| `internal/ui/agent_preferred.go` | admin editor for the ordered list |
| `internal/coordinator/pick.go` | `PickAgent` |
| `internal/coordinator/branch.go` | `ChildBranch`, `ChildRef`, `ChildID` |
| `internal/coordinator/track.go` | PR completion |
| `internal/server/handlers_session.go` | empty agent, `for` param |
| `cmd/aiman/session_cmd.go` | `--for` |
| `internal/aimanskill/SKILL.md` | coordinator rules the agent reads at start |
| `internal/ui/a1_detail.go` | ◐ on a pending item whose child is working |

---

### Task 1: Preferred-agent list in config and the admin menu

**Files:**
- Modify: `internal/infra/config/config.go` (`Config`, `ServeConfig`, `MarshalServeConfig`)
- Create: `internal/ui/agent_preferred.go`
- Modify: `internal/ui/dashboard.go` (admin menu item directly after "Agent defaults", `viewStateAgentPreferred`, the same Update/View switch arms as `viewStateAgentDefaults`)
- Test: `internal/infra/config/config_test.go`, `internal/ui/agent_preferred_test.go`

**Interfaces:**
- Produces:

```go
type AgentsConfig struct {
    Preferred []string `yaml:"preferred,omitempty"`
}
// Config.Agents AgentsConfig `yaml:"agents,omitempty"`
// ServeConfig.Agents AgentsConfig `yaml:"agents,omitempty"`
```

`MarshalServeConfig` copies `Agents` the same way it copies `AgentDefaults`.

- [ ] **Step 1: Write the failing tests**
  - `TestMarshalServeConfigCarriesPreferred`: config with `agents.preferred: [claude, grok]` round-trips through `MarshalServeConfig` and the laptop `Config` load. Order is preserved. An omitted key loads as nil.
  - `TestPreferredEditorReordersAndSaves`: model seeded with `claude`, `grok`. `pressKey("down")` then `J` swaps them. `a`, type `codex`, `enter` appends. `d` on the cursor removes that row. Save writes `agents.preferred` as the visible order and leaves `agent_defaults` untouched.
- [ ] **Step 2:** `go test ./internal/infra/config ./internal/ui -run 'TestMarshalServeConfigCarriesPreferred|TestPreferredEditor'` → FAIL.
- [ ] **Step 3:** Implement the struct, the marshal field, and a list editor. Keys: `j`/`k` or arrows move, `J`/`K` reorder, `a` append, `d` delete, `enter` save, `esc` cancel. Empty list is allowed and saves as an empty slice, not a missing key, so a user can clear a previous list. Menu title `Preferred agents`, description `Order of agents a coordinator may start`.
- [ ] **Step 4:** the same test command → PASS.
- [ ] **Step 5: Commit** `feat(config): editable preferred-agent list`

---

### Task 2: Choose the installed preferred agent

**Files:**
- Create: `internal/coordinator/pick.go`
- Modify: `internal/server/handlers_session.go` `handleCreate` (empty `Agent`)
- Modify: the wizard agent step that confirms a session when the user did not pick one (the agent picker in `internal/ui`; find it by the existing "no agent" path rather than a stale line number)
- Test: `internal/coordinator/pick_test.go`, `internal/server/handlers_session_test.go`

**Interfaces:**
- Consumes: `AgentsConfig.Preferred`
- Produces:

```go
func PickAgent(preferred, installed []string) (string, error)
// error is ErrNoPreferredAgent, and Error() lists the preferred names tried, in order.
```

- [ ] **Step 1: Write the failing tests**
  - `TestPickAgentSkipsMissing`: preferred `codex, claude`, installed `claude`, `grok` → `claude`.
  - `TestPickAgentNoneInstalled`: preferred `codex, claude`, installed `agy` → error text contains `codex` and `claude`, in that order.
  - `TestCreateUsesPreferredWhenAgentEmpty`: `session.create` with `agent` omitted and preferred `claude` (installed) stores `AgentName`/`agent` command `claude`.
  - `TestExplicitAgentBeatsPreferred`: create with `agent: grok` while preferred starts with `claude` stores `grok`.
  - `TestCreateReportsNoPreferredAgent`: empty agent, none installed → response code for a failed create, message contains the tried names, and no session row is saved.
- [ ] **Step 2:** `go test ./internal/coordinator ./internal/server -run 'TestPickAgent|TestCreateUsesPreferred|TestExplicitAgent|TestCreateReportsNoPreferred'` → FAIL.
- [ ] **Step 3:** `PickAgent` trims blanks, compares install names case-sensitively to the binary names the host already scans, and returns the first hit. `handleCreate` calls it only when `params.Agent` is empty. The wizard, when the user confirms without a selection, sends that empty agent and shows the server error. It does not substitute `claude`.
- [ ] **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(session): launch the first installed preferred agent`

---

### Task 3: Coordinator section in the bundled skill

**Files:**
- Modify: `internal/aimanskill/SKILL.md` (a new section before "Your to-do list")
- Test: the existing skill test file under `internal/aimanskill/`

**Interfaces:**
- Consumes: nothing from earlier tasks. The text names the CLI the agent already has.

- [ ] **Step 1: Write the failing test** `TestSkillStatesCoordinator`: the skill body contains all of these substrings: `coordinator`, `simple, short-lived`, `child Aiman session`, `agents.preferred`, `docs/specs/`, `do not spawn a child`, `vendor subagent`. It must not contain a fan-out key or a Spec wizard mode.
- [ ] **Step 2:** `go test ./internal/aimanskill -run TestSkillStatesCoordinator` → FAIL.
- [ ] **Step 3:** Add the section. Exact rules, in this order:
  1. You are the coordinator of this session. Before editing, read the task and assess the scope.
  2. If the task is simple and short-lived, implement it here. Do not spawn a child for work you can finish in one sitting.
  3. Otherwise, write one to-do per independently buildable piece with `aiman todo add`, then start each with `aiman session create` and `--for` that id. Do not pass `--agent`. The server picks from `agents.preferred`. A vendor subagent inside this process does not count as that child.
  4. When more than one child needs a shared contract, commit `docs/specs/<ISSUE>.md` on this branch before the first `session create`. One child can start from its to-do text alone.
  5. Each child opens its PR into this session's branch. You open the final PR to the default branch.
  6. If create returns `no_preferred_agent`, leave the to-dos unassigned and tell the user the names in the error. Do not pick a different agent.
- [ ] **Step 4:** PASS. The serve install path already copies this file. Do not add a second copy.
- [ ] **Step 5: Commit** `feat(skill): every session starts as the coordinator`

---

### Task 4: `session.create --for` links the parent's to-do

**Files:**
- Create: `internal/coordinator/branch.go`
- Modify: `internal/server/handlers_session.go` `handleCreate`
- Modify: `cmd/aiman/session_cmd.go` (the `create` flag list, next to `parent`)
- Test: `internal/coordinator/branch_test.go`, `internal/server/handlers_session_test.go`, `cmd/aiman` create flag test if one exists, else `session_cmd` test beside it

**Interfaces:**
- Consumes: `PickAgent`, `todo.OpRef`, parent `Session.Branch` and `BaseBranch`
- Produces:

```go
func ChildBranch(parentBranch, todoID string) string // parentBranch + "--" + todoID
func ChildRef(id string) string                      // "child:" + id
func ChildID(ref string) (string, bool)              // only the child: prefix
```

`handleCreate` accepts `for` (JSON `for`, CLI `--for`). It requires `parent` (default the caller's session when `AIMAN_ID` is set, which create already supports). The child branch is `ChildBranch` when `branch` is empty. The child's base branch is the parent's branch. The one-line prompt is the to-do text. After the child session is saved, set the parent's item `Ref` to `ChildRef(child.ID)` with author `aiman` through `TodoAccess`.

- [ ] **Step 1: Write the failing tests**
  - `TestChildBranch`: `feat/proj-1`, `t3` → `feat/proj-1--t3`.
  - `TestCreateForLinksParent`: parent to-do `t3` text `Add the parser`, create `--for t3` with parent set → child branch `feat/proj-1--t3`, child base `feat/proj-1`, prompt `Add the parser`, parent item ref `child:<child id>`, and that write does not enqueue a steer note.
  - `TestCreateForRefusesLinkedItem`: `t3` already `child:old` → error, no second session.
  - `TestCreateForUnknownItem`: `--for t9` → error, no session.
  - CLI: `--for t3` is sent as params `for: t3`.
- [ ] **Step 2:** `go test ./internal/coordinator ./internal/server ./cmd/aiman -run 'TestChildBranch|TestCreateFor|TestSessionCreateForFlag'` → FAIL.
- [ ] **Step 3:** Implement. If the child is saved and the Ref write then fails, forget the child session before returning the error, so a retry is not blocked by `TestCreateForRefusesLinkedItem`.
- [ ] **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(session): link a child session to the parent's to-do`

---

### Task 5: Mark the parent's to-do done when the child's PR lands

**Files:**
- Create: `internal/coordinator/track.go`
- Modify: `cmd/aiman/serve.go` (start the tracker next to the other serve loops, after todos are enabled)
- Test: `internal/coordinator/track_test.go`

**Interfaces:**
- Consumes: `ChildID`, `TodoAccess`, `NotifyRecorder`
- Produces:

```go
type PRState struct { Base string; Open, Merged bool }
type Tracker struct {
    Todos    interface { Get(id string) (domain.TodoList, error); Mutate(id string, op todo.Op, by domain.TodoAuthor) error }
    Sessions func() ([]domain.Session, error)
    PR       func(ctx context.Context, child domain.Session) (PRState, error)
    Passed   func(childID string) bool // nil means false; nl-7's LatestFor.Passed
    BeforePR func(parentID string) bool // nil means false; nl-10 sets this from verify.before_pr
}
func (t *Tracker) TrackOnce(ctx context.Context)
```

- [ ] **Step 1: Write the failing tests**
  - `TestTrackMarksMergedChild`: parent item ref `child:c1`, `c1`'s PR `Merged` with `Base` equal to the parent's branch → item `done`, author `aiman`.
  - `TestTrackIgnoresOtherBase`: same, but `Base` is `main` and the parent branch is `feat/x` → item unchanged.
  - `TestTrackOpenPRWhenBeforePR`: `BeforePR` true, PR `Open` on the parent branch, `Passed` true → `done`. `Passed` false → unchanged. `BeforePR` false and PR only open → unchanged.
  - `TestTrackResumesFromRefs`: a new `Tracker` and an existing ref, no other state, `TrackOnce` marks it done.
  - `TestTrackClearsRefOfMissingChild`: ref points at an id `Sessions` does not return → ref cleared, item not done, one `NotifyError` whose detail contains the id.
  - `TestTrackIgnoresOtherRefs`: ref `check:lint` is not probed.
- [ ] **Step 2:** `go test ./internal/coordinator -run 'TestTrack'` → FAIL.
- [ ] **Step 3:** `TrackOnce` walks every session's list. Interval for the serve loop is 2 minutes. A `gh` error on one child is logged and skipped. Wiring in `serve.go` builds `PR` from `gh pr view --json state,mergedAt,baseRefName` in the child's worktree. `Passed` reads nl-7's latest review.
- [ ] **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(coordinator): tick a to-do when its child's PR merges`

---

### Task 6: Show the child working on the parent's to-do row

**Files:**
- Modify: `internal/ui/a1_detail.go` (`renderTodoLines` / `todoView`)
- Test: `internal/ui/a1_detail_test.go`

**Interfaces:**
- Consumes: `ChildID`, `rowGlyph`
- Produces: `todoView.RefGlyph func(ref string) (glyph string, ok bool)`. A pending item with `ok` draws `glyph` instead of `○`.

- [ ] **Step 1: Write the failing test** `TestTodoRefGlyph`: ref `child:<id>` whose session glyph is `◐` → the stripped to-do line starts with `◐`, not `○`. Ref `check:lint`, or a child id that is not in the list, keeps `○`.
- [ ] **Step 2:** `go test ./internal/ui -run TestTodoRefGlyph` → FAIL.
- [ ] **Step 3:** The dashboard sets `RefGlyph` from `m.list` items. Do not add a key, a picker, or a help entry for fan-out.
- [ ] **Step 4:** `go test ./internal/ui` → PASS.
- [ ] **Step 5: Commit** `feat(ui): a parent's to-do shows its child's glyph`

---

## Self-review

- W8 "simple stays here" and "larger splits": Task 3, skill only. No Go threshold.
- Child is a real Aiman session: Task 4 uses `session.create`.
- User can still edit and steer, and can stop a child from the dashboard: existing to-do edits and session terminate. No new task.
- Branch, `Ref`, ◐, done on merge, `verify.before_pr` seam: Tasks 4, 5, 6.
- Shared spec only when more than one child needs it: Task 3.
- Preferred list, order, settings, empty agent, explicit wizard agent, no invented agent: Tasks 1 and 2.
- `roles.verifier` untouched. `roles.planner` / `roles.worker` not introduced.
- nl-10, when it reaches `ResolveLaunchDefaults`, must not add a planner or worker role agent. Child agent selection stays `PickAgent`.
