# Design-First Coordinator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every operator-started aiman session size its task, publish an explanatory design page to the private `realfi-co/docs` Pages site, have it challenged by a smart consult, and wait for the operator's approval before delegating structured, test-protected pieces to children.

**Architecture:** Serve gains a design gate on the to-do list, a hidden `consult` session role, structured child briefs and reports, acceptance-test protection in verify, a design publisher that commits pages to a docs repository, and a usage rollup by role. Prompts and the bundled skill teach the flow; serve enforces only the gate, the brief and report shapes, the consult tier and test protection.

**Tech Stack:** Go 1.26, aiman serve JSON-over-Unix-socket RPC, sqlite ledger, `git` and `gh` on the host, Mermaid in Markdown rendered by GitHub Pages.

**Spec:** `docs/superpowers/specs/2026-10-29-design-first-coordinator-design.md` (evidence: `docs/superpowers/specs/2026-10-29-agentic-workflows-research.md`).

## Global Constraints

- The spec names `handleSessionCreate`; the handler is `handleCreate` (`internal/server/handlers_session.go:611`). Use the real name.
- New error codes, exact strings, in `internal/server/protocol.go`: `CodeGateNeedsOperator = "gate_needs_operator"`, `CodeDesignNotApproved = "design_not_approved"`, `CodeConsultNeedsSmart = "consult_needs_smart"`.
- New ledger kinds in `internal/domain/events.go`: `EventDesignApproved = "design.approved"`, `EventDesignPublished = "design.published"`, `EventConsultFinished = "consult.finished"`.
- Gate item: text `Design approved`, Ref `gate:design`.
- Design page path: `<design.dir>/<repo name>/<KEY>.md`, KEY = issue key, else branch with `/` replaced by `-`. Commit message: `design(<repo name>): <KEY> rev <n>`. Commits go to `main` of `design.repo`.
- Config keys: `design.repo`, `design.dir` (default `docs/designs`), `design.site` (optional).
- Host checkout: `~/.aiman/design-repo/`; lock file `~/.aiman/design-repo.lock`; consult scratch dirs `~/.aiman/consults/<id>/`.
- At most 4 children run at once (skill guidance, not enforced).
- Prompt suffix, exact copy: `Use the aiman skill's Design first section: size the task after exploring the code and say your call before editing.`
- Source comments describe current code, never history. No em-dashes in prose. `make ci` and `make eval` green before each task's commit; ignore `TestOllamaIntelligenceSummariseSession`.
- Commit trailer on every commit: `Claude-Session: https://claude.ai/code/session_01F6CrCm8V8STTLdz1JfXXzc`.

## Review Focus

1. An agent bypassing the gate by removing, dropping, editing or re-referencing the gate item instead of ticking it: every such mutation from a session is refused (test in Task 1).
2. A trivial task has no gate: `session create --for` from a parent with no gate item must still succeed (test in Task 1).
3. A smart tier with a single entry equal to the coordinator's agent and model: the consult is still created on it, since independence applies only when another entry exists (test in Task 3).
4. Two publishes racing, or `main` moved under the host checkout: the second publish rebases and both commits land (test in Task 6).
5. `## Surprises` written as `None`, `none.`, `- None` or `N/A`: no steer to the parent; anything else steers once (test in Task 4).

---

### Task 1: Design gate in serve

**Files:**
- Create: `internal/todo/gate.go`, `internal/todo/gate_test.go`
- Modify: `internal/server/handlers_todo.go` (`handleTodo` :275, `todoParams` :207), `internal/server/handlers_session.go` (`applyFor` :796), `internal/server/protocol.go`, `internal/domain/events.go`, `cmd/aiman/todo_cmd.go` (`todoParams` :59)
- Test: `internal/server/todo_gate_test.go`, `evals/harness/cases_design.go`

**Interfaces:**
- Produces: `todo.DesignGateRef = "gate:design"`, `todo.DesignGateText = "Design approved"`; `func todo.AddGate(l *domain.TodoList) bool` (adds the open gate item before the Done-when item if absent, reports whether it added); `func todo.GateOpen(l domain.TodoList) bool`; `func todo.GuardGate(prev, next domain.TodoList) error` (returns an error wrapping `todo.ErrInvalid` when an open gate in `prev` is missing, not pending/active, or has a changed Text or Ref in `next`); RPC `todo.gate {session}` and `todo.approve_design {session, quote}`; CLI `aiman todo gate`, `aiman todo approve-design --quote "<text>"`.

- [ ] **Step 1: Write failing unit tests** in `internal/todo/gate_test.go`: `TestAddGateOnce` (second call returns false, one item with Ref `gate:design`, placed before Ref `done`); `TestGuardGateRefusesBypass` (table: tick done, drop, remove, edit text, change Ref; each returns an error; reordering the item returns nil; a list with no gate returns nil).
- [ ] **Step 2: Run** `go test ./internal/todo/ -run Gate` and confirm FAIL (undefined).
- [ ] **Step 3: Implement** `internal/todo/gate.go` with the interfaces above.
- [ ] **Step 4: Write failing server tests** in `internal/server/todo_gate_test.go`:
  - `TestGateRefusesSessionCallers`: with Caller = owner and with Caller = another session, `todo.update` done on the gate returns code `gate_needs_operator`; `todo.drop`/`todo.rm` on it likewise.
  - `TestGateOperatorTicks`: Caller empty, `todo.update` done succeeds.
  - `TestApproveDesign`: Caller = owner with quote "go ahead" closes the gate and writes one `design.approved` ledger row whose payload has `quote: "go ahead"`; empty quote returns `invalid_params`; Caller = another session returns `gate_needs_operator`.
  - `TestForBlockedByOpenGate`: `session.create {for, parent}` with an open gate returns `design_not_approved`; after approval it succeeds; with no gate item it succeeds.
- [ ] **Step 5: Run** `go test ./internal/server/ -run 'Gate|ApproveDesign|ForBlocked'`; expect FAIL.
- [ ] **Step 6: Implement.** In `handleTodo`, for every mutating method when `req.Caller != ""`, run the mutation through `todo.GuardGate(prev, next)` inside the `MutateTodos` callback and map its error to `CodeGateNeedsOperator` (also for `todo.mirror`). `todo.gate` calls `AddGate` as `AuthorAiman` without steering. `todo.approve_design` requires `req.Caller == session`, a trimmed non-empty `quote` (`invalid_params` otherwise), sets the gate done as `AuthorAiman`, then `appendEvent(ctx, id, domain.EventActorAgent, domain.EventDesignApproved, map[string]any{"quote": quote})`. In `applyFor`, return `CodeDesignNotApproved` when `todo.GateOpen` on the parent list. CLI: `gate` maps to `todo.gate`; `approve-design` takes `--quote` and maps to `todo.approve_design`.
- [ ] **Step 7: Add eval** `CaseDesignGate()` in `evals/harness/cases_design.go` covering the spec's `design-gate` bullet (owner plain done refused, other session refused, `approve-design` with quote succeeds with ledger row, no-caller done succeeds, `--for` refused while open), registered in `LifecycleCases()` (`evals/harness/cases_lifecycle.go:498`).
- [ ] **Step 8: Run** `go test ./internal/todo/ ./internal/server/ && make eval`; expect PASS.
- [ ] **Step 9: Commit** `feat(todo): gate delegation on operator design approval`.

### Task 2: Model family

**Files:**
- Create: `internal/infra/agent/family.go`, `internal/infra/agent/family_test.go`

**Interfaces:**
- Produces: `func agent.Family(command, model string) string`, returning `anthropic`, `openai`, `google`, `xai`, `deepseek` or `""` (unknown).

- [ ] **Step 1: Write failing test** `TestFamily` (table): `("claude","")`→anthropic; `("codex","gpt-5.4")`→openai; `("grok","")`→xai; `("agy","")`→google; `("pi","anthropic/claude-opus-4-7:high")`→anthropic; `("pi","openai/gpt-5.4")`→openai; `("copilot","gpt-5-mini")`→openai; `("copilot","claude-sonnet-4.5")`→anthropic; `("cursor-agent","gemini-3-pro")`→google; `("deepcode","deepseek-v4")`→deepseek; `("kilo","")`→"".
- [ ] **Step 2: Run** `go test ./internal/infra/agent/ -run TestFamily`; expect FAIL.
- [ ] **Step 3: Implement.** Binary first via `CommandBase` (claude→anthropic, codex→openai, grok→xai, agy→google); otherwise the model, after stripping a `provider/` prefix (which wins when it names a family) and a `:<thinking>` suffix, matched by prefix: `claude|opus|sonnet|haiku`→anthropic, `gpt|o3|o4|codex`→openai, `gemini`→google, `grok`→xai, `deepseek`→deepseek.
- [ ] **Step 4: Run** the test; expect PASS.
- [ ] **Step 5: Commit** `feat(agent): classify agent and model family`.

### Task 3: Consult role

**Files:**
- Create: `internal/coordinator/consult.go`, `internal/coordinator/consult_test.go`, `internal/server/consult.go`, `internal/server/consult_test.go`
- Modify: `internal/domain/session.go` (:115-128), `internal/coordinator/brief.go`, `internal/coordinator/track.go` (`TrackOnce` :85), `internal/server/handlers_session.go` (`createParams` :569, `handleCreate`, `readBrief` :843), `cmd/aiman/session_cmd.go` (`boolSessionFlags`, `sessionCreateParams` :215), `cmd/aiman/serve.go` (:233), `internal/domain/events.go`
- Test: `evals/harness/cases_design.go`

**Interfaces:**
- Consumes: `agent.Family` (Task 2).
- Produces: `domain.SessionRoleConsult SessionRole = "consult"` (`Hidden()` true); `type coordinator.ConsultBrief struct{ Question, Scope, Report string }` and `func coordinator.ParseConsultBrief(md string) (ConsultBrief, error)` (today's three-heading rules, moved from `ParseBrief`); `func coordinator.PickConsult(smart []config.PreferredAgent, coordAgent, coordModel string, installed func(string) bool) (config.PreferredAgent, error)`; `type coordinator.ConsultReport struct{ High, Medium, Low int; Verdict string }` and `func coordinator.ParseConsultReport(md string) (ConsultReport, error)`; `createParams.Consult bool` (`consult`), CLI `--consult`; `Tracker.Retire func(ctx context.Context, child domain.Session)`.
- Consult report format (for the skill): `## Objections` with bullets starting `[high]`, `[medium]` or `[low]`; `## Verdict` whose first word is `approve`, `revise` or `reject`.

- [ ] **Step 1: Write failing tests** in `internal/coordinator/consult_test.go`: `TestPickConsultPrefersOtherFamily` (coordinator claude/opus; smart = [claude/opus, codex/gpt-5.4] → codex); `TestPickConsultOtherModelSameFamily` (smart = [claude/opus, claude/sonnet], coordinator claude/opus → claude/sonnet); `TestPickConsultSingleSameEntry` (smart = [claude/opus] → claude/opus, nil error); `TestPickConsultEmptySmart` (error `ErrConsultNeedsSmart`); `TestParseConsultReport` (two `[high]`, one `[low]`, verdict `revise` → {2,0,1,"revise"}; missing `## Verdict` → error).
- [ ] **Step 2: Run** `go test ./internal/coordinator/ -run Consult`; expect FAIL.
- [ ] **Step 3: Implement** `internal/coordinator/consult.go`. `PickConsult` walks only `smart` (installed entries) and ranks: different family, then the `verify.PickReviewer` independence rule (different binary, or same binary with a different non-empty model), then the first installed entry.
- [ ] **Step 4: Run**; expect PASS.
- [ ] **Step 5: Write failing server tests** in `internal/server/consult_test.go`: `TestCreateConsult` (`session.create {for, parent, consult:true, brief}` makes a hidden child with Role `consult`, ParentID set, Tier `smart`, WorkingDirectory under `<home>/.aiman/consults/`, no branch, and links the item with `coordinator.ChildRef`); `TestCreateConsultNeedsSmart` (smart tier empty, worker installed → `consult_needs_smart`); `TestCreateConsultAllowedBeforeApproval` (open gate does not block `consult:true`); `TestCreateConsultUsesConsultBrief` (a three-heading brief is accepted for a consult).
- [ ] **Step 6: Run**; expect FAIL.
- [ ] **Step 7: Implement** `internal/server/consult.go` with `func (s *Server) createConsult(ctx context.Context, req Request, p createParams, parent domain.Session, item domain.TodoItem) Response`, following `handoverchild.(*Manager).prepare` (`internal/handoverchild/manager.go:387`): `MkdirAll` + `git init` of `~/.aiman/consults/<id>/`, write the brief with `usecase.WriteTaskBrief`, then `CreateChild` with `SessionConfig{Role: domain.SessionRoleConsult, AdHoc: true, PreparedWorktree: dir, ParentID: parent.ID, Tier: "smart", InitialPrompt: "Read .aiman_task.md and follow your brief. The parent worktree is <path>; read it, do not edit it."}` and the agent from `PickConsult`. `handleCreate` branches to it when `p.Consult` before `applyFor`'s branch defaults and before the gate check.
- [ ] **Step 8: Tracker.** In `TrackOnce`, for a child with Role `consult`, skip `landed`; when its report parses with `ParseConsultReport`, settle the item, append `consult.finished` with `{high, medium, low, verdict}` on the parent, and call `Retire` (wired in `serve.go` to kill, forget and remove the scratch dir). An unparsable report keeps the item open and asks once through the existing `askForReport`. Add `track_test.go` case `TestConsultSettlesOnReport`.
- [ ] **Step 9: Add eval** `CaseConsultClosesOnReport()` (consult created, report with objections and verdict written to its dir, `TrackOnce`, item done with no PR set, `consult.finished` row present), registered in `LifecycleCases()`. The harness `EnableCoordinator` must also install a smart entry: set `Smart: config.PreferredFromCommands("claude")` in its `SetAgentChoice` call.
- [ ] **Step 10: Run** `go test ./internal/... && make eval`; expect PASS.
- [ ] **Step 11: Commit** `feat(coordinator): consult sessions as smart sounding boards`.

### Task 4: Structured child brief and report

**Files:**
- Modify: `internal/coordinator/brief.go`, `internal/coordinator/brief_test.go`, `internal/coordinator/report.go`, `internal/coordinator/report_test.go`, `internal/coordinator/track.go` (`evidence` :144, `askForReport` :157), `internal/usecase/flow_manager.go` (`WriteTaskBrief` :208), `evals/harness/cases_coordinator.go`
- Test: `evals/harness/cases_design.go`

**Interfaces:**
- Produces: `type coordinator.Brief struct{ Objective, Contract, Owns, OutOfScope, AcceptanceTests, Report, EscalateWhen string }` with headings, in order, `Objective`, `Contract`, `Owns`, `Out of scope`, `Acceptance tests`, `Report must contain`, `Escalate when`; `func (b Brief) AcceptanceFiles() []string` (tokens in AcceptanceTests that pass `isRepoPath`); `func (b Brief) AcceptanceCommands() []string` (backtick spans in AcceptanceTests that are not repo paths); `func coordinator.BriefFromTaskFile(task string) (Brief, bool)`; `func coordinator.ReportSurprises(md string) (text string, any bool)`; brief markers `<!-- aiman:brief -->` / `<!-- /aiman:brief -->` written by `WriteTaskBrief`; `const coordinator.ChildGuidelines` (exact copy below).

`ChildGuidelines`:
```
You are a child session. Your brief is your design: skip the Design first section of the aiman skill and do not wait for confirmation. Do not edit the files listed under Acceptance tests. If an Escalate when condition holds, ask a smart consult (aiman session create --consult) before guessing. Write .aiman_report.md with ## Evidence (each command and its exit code) and ## Surprises (anything that affects other pieces, or "none").
```

- [ ] **Step 1: Update failing tests.** `brief_test.go`: `TestBriefRequiresSevenHeadings`; `TestBriefRejectsOldShape` (a Question/Scope/Report brief errors with a message containing `## Objective`); `TestBriefAcceptance` (`` `go test ./internal/parse/...` covers internal/parse/parse_test.go `` → commands `[go test ./internal/parse/...]`, files `[internal/parse/parse_test.go]`); `TestBriefFromTaskFile` round trip through the markers. `report_test.go`: `TestReportNeedsEvidenceAndSurprises` (missing either heading → error; evidence must sit under `## Evidence`); `TestReportSurprises` (`None`, `none.`, `- None`, `N/A` → any=false; other text → any=true, text returned).
- [ ] **Step 2: Run** `go test ./internal/coordinator/`; expect FAIL.
- [ ] **Step 3: Implement** in `brief.go` and `report.go`. `WriteTaskBrief` wraps the brief in the markers and appends `ChildGuidelines` inside them.
- [ ] **Step 4: Tracker.** When a landed child's report has surprises, steer the parent once per child (key in `asked`): `[aiman] child <id> reported surprises that may affect other pieces; re-read the design before starting dependent pieces:\n<text>`. Test `TestTrackerSteersSurprisesOnce` in `track_test.go`.
- [ ] **Step 5: Update evals.** `CaseChildSpawn`'s report gains `## Evidence` (with the existing command and path) and `## Surprises\n\nnone`. Add `CaseBriefStructure()` (spec bullet `brief-structure`) and register it.
- [ ] **Step 6: Run** `go test ./internal/... && make eval`; expect PASS.
- [ ] **Step 7: Commit** `feat(coordinator): structured child briefs and reports`.

### Task 5: Protected acceptance tests in verify

**Files:**
- Modify: `internal/verify/manager.go` (`prepare` :294, `finish` :478), `internal/verify/evidence.go`
- Create: `internal/verify/acceptance.go`, `internal/verify/acceptance_test.go`
- Test: `evals/harness/cases_design.go`

**Interfaces:**
- Consumes: `coordinator.BriefFromTaskFile`, `Brief.AcceptanceFiles`, `Brief.AcceptanceCommands` (Task 4).
- Produces: `func verify.TouchedProtected(patch string, files []string) []string`.

- [ ] **Step 1: Write failing tests** `TestTouchedProtected` (a patch with `diff --git a/internal/parse/parse_test.go b/...` and protected `[internal/parse/parse_test.go]` → that file; a rename away from it also counts; unrelated → empty) and `TestFinishDeniesProtectedEdit` (manager test: worker task file with a brief protecting a file the diff touches → review incomplete with one REQUIRED finding titled `Acceptance test edited: <file>`).
- [ ] **Step 2: Run** `go test ./internal/verify/`; expect FAIL.
- [ ] **Step 3: Implement.** `prepare` keeps the parsed brief on the run. `finish` adds the REQUIRED finding per touched file (pattern of `notReproduced`, :407) and runs each acceptance command with `runAt` after the configured test command; a non-zero exit adds a REQUIRED finding `Acceptance test failed: <command>`.
- [ ] **Step 4: Add eval** `CaseProtectedAcceptanceTests()` and register it in the verify case list (`cases_verify_test.go`).
- [ ] **Step 5: Run** `go test ./internal/verify/ && make eval`; expect PASS.
- [ ] **Step 6: Commit** `feat(verify): protect and run a brief's acceptance tests`.

### Task 6: Design publisher

**Files:**
- Create: `internal/design/publish.go`, `internal/design/publish_test.go`, `internal/design/triage.go`, `internal/design/triage_test.go`, `internal/server/handlers_design.go`, `cmd/aiman/design_cmd.go`
- Modify: `internal/infra/config/config.go` (`Config` :38, `ServeConfig` :73, `MarshalServeConfig` :228), `cmd/aiman/main.go` (:86), `cmd/aiman/serve.go`

**Interfaces:**
- Produces: `type config.DesignConfig struct{ Repo, Dir, Site string }` (yaml `repo`, `dir`, `site`; `func (d DesignConfig) DirOrDefault() string` → `docs/designs`); `type design.Publisher struct{ Cfg config.DesignConfig; Home string; Run func(ctx context.Context, dir string, name string, args ...string) (string, error) }`; `func (p *Publisher) Publish(ctx context.Context, src, repoName, key string) (design.Result, error)`; `type design.Result struct{ Path, URL, Build string; Revision int; Local bool }`; `func design.Key(issueKey, branch string) string`; `func design.CountTriage(md string) (accepted, rejected, deferred int)` (rows of the table under `## Decisions` whose `Triage` column is `accept`, `reject` or `defer`); RPC `design.publish {session, file, key}`; CLI `aiman design publish <file> [--key KEY]`.

- [ ] **Step 1: Write failing tests** with a local bare repository standing in for `design.repo` (`Run` wired to real `git`; clone step uses `git clone <bare path>` when `Cfg.Repo` is a filesystem path): `TestPublishFirstRevision` (file lands at `docs/designs/r/PROJ-1.md`, commit `design(r): PROJ-1 rev 1`, `Revision` 1, URL `<site>/designs/r/PROJ-1.html`); `TestPublishSecondRevision` (rev 2); `TestPublishRebasesOnMovedMain` (another clone pushes first; publish still succeeds and both commits are on main); `TestPublishLocalWhenUnset` (empty Repo → file under the session's git dir `aiman-design/`, `Local` true, no git calls); `TestKey` (`("PROJ-1","x")`→`PROJ-1`, `("","feat/a-b")`→`feat-a-b`); `TestCountTriage`.
- [ ] **Step 2: Run** `go test ./internal/design/`; expect FAIL.
- [ ] **Step 3: Implement.** Under an exclusive `flock` on `~/.aiman/design-repo.lock`: clone if `~/.aiman/design-repo/.git` is missing (`gh repo clone <repo> <dir>` for an `owner/name`), `git pull --rebase`, copy, `git add`, commit, `git push`; on a rejected push, `pull --rebase` and retry up to 3 times. Revision = commits touching the path + 1. URL: `Cfg.Site`, else `gh api repos/<repo>/pages -q .html_url`; path with the leading `docs/` dropped and `.md`→`.html`. Build: `gh api repos/<repo>/pages/builds/latest -q .status`, empty on error.
- [ ] **Step 4: Run**; expect PASS.
- [ ] **Step 5: Wire serve and CLI.** `handlers_design.go` resolves the session, reads `file` relative to its working directory, calls `Publish` with `RepoName`'s name part and `design.Key`, appends `design.published` `{revision, url, accepted, rejected, deferred}` and returns `{"type":"design","design":Result}`. `config.go` adds `Design DesignConfig` to `Config` and `ServeConfig` and copies it in `MarshalServeConfig`. Test `TestMarshalServeConfigCopiesDesign` in `internal/infra/config`.
- [ ] **Step 6: Run** `go test ./internal/... ./cmd/...`; expect PASS.
- [ ] **Step 7: Commit** `feat(design): publish design pages to a docs repository`.

### Task 7: Usage by role

**Files:**
- Modify: `internal/domain/usage.go` (:81), `internal/usage/tracker.go` (`scanSession` :194), `internal/usage/report.go` (`rollup` :285, `bucketKey` :306), `internal/server/handlers_usage.go` (`validUsageBy` :144), `cmd/aiman/usage_cmd.go` (:23)

**Interfaces:**
- Produces: `UsageSessionMeta.Role`, `UsageSessionMeta.ParentID`; `by: "role"` keys: the session's Role when set, else `child` when ParentID is set, else `coordinator`.

- [ ] **Step 1: Write failing test** `TestRollupByRole` in `internal/usage/report_test.go` (sessions coordinator, child, consult, reviewer → four rows with those keys) and extend the handler test for `by: role`.
- [ ] **Step 2: Run** `go test ./internal/usage/ ./internal/server/ -run Role`; expect FAIL.
- [ ] **Step 3: Implement** the fields, fill them in `scanSession`, add `role` to both whitelists and `bucketKey`, and update the CLI help.
- [ ] **Step 4: Run**; expect PASS.
- [ ] **Step 5: Commit** `feat(usage): roll up cost by session role`.

### Task 8: Prompts

**Files:**
- Modify: `internal/infra/skills/engine.go` (`initialPrompt` :162, `buildSessionPrompt` :181, KitPrompt swap :301, Working Guidelines step 3 :454), `internal/usecase/flow_manager.go` (`joinPrompt` call sites :427-435), `internal/usecase/daemon.go` (:170-176)
- Test: `internal/infra/skills/engine_test.go`, `internal/usecase/flow_manager_test.go`, `internal/usecase/daemon_test.go`, `evals/harness/cases_design.go`

**Interfaces:**
- Produces: `skills.DesignFirstSuffix` (exact copy from Global Constraints).

- [ ] **Step 1: Write failing tests:** `TestInitialPromptsCarryDesignSuffix` (JIRA, multi-file, kit-only prompts end with the suffix); `TestHiddenRolesGetNoSuffix` (reviewer, handover, consult); `TestAdHocTextGetsSuffix` (operator text present → suffix appended; quick with no text → empty prompt); `TestBriefedChildGetsNoSuffix`; `TestWorkingGuidelinesDesignFirst` (step 3 reads `**Design first.** Follow the aiman skill's Design first section: size the task, publish the design page, run a consult, and wait for the operator's approval before delegating. A trivial task needs only a short plan.` and the file contains no `Wait for confirmation`); `TestAutonomousPromptDesign` (daemon prompt contains the line below).
- [ ] **Step 2: Run** `go test ./internal/infra/skills/ ./internal/usecase/`; expect FAIL.
- [ ] **Step 3: Implement.** Daemon prompt gains, before the "When you are finished" list: `Before implementing, size the task. For anything beyond a trivial fix, write the design page, publish it with aiman design publish, and run one consult with aiman session create --consult, on another model family when one is installed. If an objection of severity high is still unresolved, stop and wait for input; do not implement. Link the design in your issue comment.` Item 3's "wait for a reply" polling text stays.
- [ ] **Step 4: Add eval** `CaseChildPromptNoWait()` (spec bullet `child-prompt-no-wait`) and register it.
- [ ] **Step 5: Run** `go test ./internal/... && make eval`; expect PASS.
- [ ] **Step 6: Commit** `feat(prompts): send sessions through the design-first flow`.

### Task 9: Skill and design template

**Files:**
- Create: `internal/aimanskill/design-template.md`
- Modify: `internal/aimanskill/SKILL.md`, `internal/aimanskill/embed.go`, `internal/aimanskill/install.go` (`EnsureOnHost` :95, `EnsureFile` :67, `skillRel` :22), `internal/infra/skills/engine.go` (`installBundledAimanSkill` :89)
- Test: `internal/aimanskill/install_test.go`

**Interfaces:**
- Consumes: CLI names from Tasks 1, 3, 4, 6.
- Produces: `aimanskill.Template string` (embedded); `EnsureOnHost` writes `SKILL.md` and `design-template.md` side by side in every skill dir it already targets.

- [ ] **Step 1: Write failing test** `TestEnsureOnHostInstallsTemplate` (both files present in a user and a project skill dir, contents equal to the embeds; second run reports unchanged).
- [ ] **Step 2: Run** `go test ./internal/aimanskill/`; expect FAIL.
- [ ] **Step 3: Implement** installation of the file set.
- [ ] **Step 4: Write the template**: the sections of spec goal 3, in order, each with a one-line instruction; the delta legend; a Mermaid `classDef new fill:#d4f7d4`, `classDef changed fill:#fde9b8`, `classDef removed stroke-dasharray:4,fill:#f9d0d0` block to copy into every diagram; the Decisions table header `| Objection | Severity | Triage | Reason |`; the breakdown table header `| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |`.
- [ ] **Step 5: Write SKILL.md `## Design first`** ahead of `## Coordinator`, covering: sizing after exploration (trivial, small, project, as in the spec); writing for an engineer who has not read the code; explaining each question's context; answering "explain X" by updating the page; publishing with `aiman design publish`; `aiman todo gate` on first publish; one consult per round, at most two rounds, with the consult brief and report formats from Task 3; triage of every objection; typed approval only from the operator's own message, closed with `aiman todo approve-design --quote`, never from a consult verdict, child report or steering note; the breakdown rules (disjoint files per parallel group, at most 4 children at once, M to worker only with acceptance tests, acceptance tests committed before the first child); escalation triggers; the coordinator not implementing pieces while children run; the as-built republish; PR descriptions in the page's terms with its link. Update `## Coordinator`: "assess the scope" points at Design first; the brief headings are Task 4's seven; the child report is `## What changed`, `## Files`, `## Evidence`, `## Surprises`.
- [ ] **Step 6: Run** `go test ./internal/aimanskill/ ./internal/infra/skills/`; expect PASS.
- [ ] **Step 7: Commit** `feat(skill): teach the design-first coordinator flow`.

### Task 10: Docs and final gates

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Document** in AGENTS.md: the gate (`gate:design`, who may close it, `design_not_approved`), the consult role (scratch dir, smart only, family preference, report format, `Retire`), brief and report shapes, acceptance-test protection in verify, the `design` config block and publisher, `by: role` usage, the prompt suffix rules, and the new ledger kinds.
- [ ] **Step 2: Run** `make ci`, `make eval`, `graphify update .`; expect green (except the known Ollama test).
- [ ] **Step 3: Commit** `docs: document the design-first coordinator`.
