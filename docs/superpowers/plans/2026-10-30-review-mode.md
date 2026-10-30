# Review Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the operator start a review session from a GitHub PR, have two smart reviewers review it blind and then informed, read a terse brief, answer three questions before seeing the agents' view, and post one operator-approved GitHub review.

**Architecture:** A review session is an interactive session with `ReviewPR` set, created by serve in a detached worktree at the PR head with the description withheld. serve holds the review state (`~/.aiman/reviews/<session>.json`), gates the reveal of the description and the posting of the review, and reuses the consult role for the second reviewer, the design publisher for the brief, and the operator-only gate (generalised to `gate:review`).

**Tech Stack:** Go 1.26, aiman serve JSON RPC, sqlite, `git` and `gh` on the host, Bubble Tea v2 TUI.

**Spec:** `docs/superpowers/specs/2026-10-30-review-mode-design.md` (evidence: `docs/superpowers/specs/2026-10-29-code-review-research.md`).

## Global Constraints

- A review session is interactive with `Session.ReviewPR = "owner/repo#n"`; there is no new `Mode` value. Code that treats it differently checks `ReviewPR != ""`.
- Review sessions skip the design-first prompt suffix, `gate:design` and `verify.before_pr`; the handover child still runs.
- Worktree directory `<repo>@pr-<n>` (detached at the PR head). Brief page key `pr-<n>`.
- Files serve writes in the worktree: `.aiman_review/diff.patch`, `.aiman_review/files.txt`, `.aiman_review/meta.json`; after reveal, `.aiman_pr_description.md`. All match the existing `.aiman_*` git-exclude glob or get added to `info/exclude`.
- Gate: Ref `gate:review`, text `Review approved`; closes like the design gate (operator tick, or `aiman todo approve-review --quote`).
- Error codes (exact): `review_exists`, `review_incomplete`, `review_head_moved`, `review_not_ready`.
- Ledger kinds (exact): `review.started`, `review.revealed`, `review.refreshed`, `review.approved`, `review.posted`, `consult.abandoned`.
- Config: `review.consult_timeout`, default 30 minutes; pushed to serve by `MarshalServeConfig`.
- Posting: event `COMMENT` or `REQUEST_CHANGES`, never `APPROVE`; only findings with `Call: agree` become inline comments; comment bodies start with `blocking:`, `suggestion:`, `question:` or `nit:`.
- Review state file: `~/.aiman/reviews/<session id>.json`.
- After the detached checkout the worktree is soft-reset to the merge base (`git reset --soft <merge-base>`), so the PR's changes are staged and every editor's gutter marks them. Agents never commit, stash or reset there.
- Debug and coverage outputs: `.vscode/launch.json`, `.aiman_review/coverage.md`; config `repos.<slug>.review.coverage` (a command writing Istanbul `coverage-final.json`) for non-Go repositories. Error code `review_no_coverage` when no coverage method applies.
- Source comments describe current code; no em-dashes in prose. `make ci` and `make eval` green before each commit; ignore `TestOllamaIntelligenceSummariseSession`. Every commit ends with `Claude-Session: https://claude.ai/code/session_01F6CrCm8V8STTLdz1JfXXzc`.

### Review file format (Tasks 2, 5, 7 share it)

```
# Review: owner/repo#123

## Answers

### What, why, how
> <operator's words>

### Blast radius
> <operator's words>

### Less code
> <operator's words>

## Findings

### F1 blocking internal/x/y.go:42
Verified: yes (go test ./internal/x/ -run TestY fails)
Source: primary
Call: agree "<operator's words>"

<comment body>

## Summary

<review summary>
```

Labels: `blocking`, `suggestion`, `question`, `nit`. `Verified:` is `yes (<evidence>)` or `no`; an unverified finding must be labelled `question`. `Source:` is `primary`, `secondary` or `both`. `Call:` is `agree` or `disagree`, each followed by a quoted operator answer.

## Review Focus

1. A PR from a fork: `pull/<n>/head` must still fetch; the worktree lands at the PR head SHA (test in Task 3).
2. The agent asks for reveal before any consult was started and the second reviewer is unavailable: reveal refuses with `review_not_ready` unless `single: true` is passed, which records single-reviewer mode (test in Task 4).
3. The PR head moves between brief and post: post refuses with `review_head_moved`; refresh resets and reopens the gate (test in Task 5).
4. A finding labelled `blocking` with `Verified: no`: the parser rejects it as incomplete so it cannot be posted as a defect (test in Task 2).
5. The operator starts a second review of the same PR while the first session lives: create refuses with `review_exists` (test in Task 3).
6. Refresh after the soft reset: the worktree ends staged against the new merge base with no leftovers from the old head (test in Task 4).

---

### Task 1: Generalise the gate to a second ref

**Files:**
- Modify: `internal/todo/gate.go`, `internal/todo/gate_test.go`, `internal/server/handlers_todo.go` (`handleTodoGate` :635, `handleApproveDesign` :650, routing :305), `internal/server/server.go` (:396), `internal/server/handlers_session.go` (`applyFor` :824), `internal/domain/events.go`, `cmd/aiman/todo_cmd.go`, `evals/harness/cases_design.go`

**Interfaces:**
- Produces: `todo.ReviewGateRef = "gate:review"`, `todo.ReviewGateText = "Review approved"`; `func todo.AddGate(l *domain.TodoList, ref string) bool` (text from the ref); `func todo.GateOpen(l domain.TodoList, ref string) bool`; `GuardGate`/`ClampGate` protect both refs; `ErrGateLocked` message names no specific gate; RPC `todo.approve_review {session, quote}` writing `review.approved`; `todo.gate {session, ref}` (ref defaults to `gate:design`); CLI `aiman todo approve-review --quote`, `aiman todo gate [--review]`.

- [ ] **Step 1: Failing tests:** `TestGuardGateCoversReviewGate` (tick, drop, rename of an open `gate:review` refused; reorder allowed); `TestAddGateReview` (text `Review approved`); server `TestApproveReview` (owner with quote closes `gate:review` and writes `review.approved` with the quote; other session refused `gate_needs_operator`); `TestReviewGateDoesNotBlockFor` (open `gate:review` does not refuse `session create --for`).
- [ ] **Step 2: Run** `go test ./internal/todo/ ./internal/server/ -run 'Gate|Approve'`; expect FAIL.
- [ ] **Step 3: Implement.** `handleApproveDesign` becomes `approveGate(ctx, req, p, sess, ref string, kind domain.EventKind)`. `applyFor` keeps checking `DesignGateRef` only. Update the eval helpers for the new signatures.
- [ ] **Step 4: Run** `go test ./internal/... && make eval`; expect PASS.
- [ ] **Step 5: Commit** `feat(todo): a review gate beside the design gate`.

### Task 2: Review package: PR reference, GitHub client, review file

**Files:**
- Create: `internal/review/ref.go`, `internal/review/github.go`, `internal/review/file.go` with tests `ref_test.go`, `github_test.go`, `file_test.go`

**Interfaces:**
- Produces:
  - `type review.Ref struct{ Owner, Repo string; Number int }`; `func review.ParseRef(s string) (Ref, error)` for `owner/repo#n`; `func (r Ref) String() string`; `func (r Ref) Slug() string` (`owner/repo`).
  - `type review.PR struct{ Number int; Title, Author, Body, BaseRef, HeadRef, HeadSHA string; Additions, Deletions int }`.
  - `type review.GitHub interface { ListPRs(ctx context.Context, slug string) ([]PR, error); PR(ctx context.Context, ref Ref) (PR, error); PostReview(ctx context.Context, ref Ref, post Post) error }`.
  - `type review.Post struct{ Event, Body, CommitID string; Comments []Comment }`, `type review.Comment struct{ Path string; Line int; Body string }`.
  - `type review.GHCLI struct{ Run func(ctx context.Context, name string, args ...string) (string, error) }` implementing `GitHub` with `gh pr list --repo <slug> --state open --json number,title,author,headRefName,additions,deletions`, `gh pr view <n> --repo <slug> --json number,title,author,body,baseRefName,headRefName,headRefOid,additions,deletions`, and `gh api repos/<slug>/pulls/<n>/reviews --input -` (JSON body on stdin).
  - `type review.File struct{ Answers map[string]string; Findings []Finding; Summary string }`, `type review.Finding struct{ ID, Label, Path string; Line int; Verified bool; Evidence, Source, Call, Quote, Body string }`; `func review.ParseFile(md string) (File, error)`; `func (f File) Missing() []string` (missing answers, findings without a call, unverified non-question findings); `func (f File) Post(event, commit string, single bool) (Post, error)` (agreed findings only, `<label>: ` prefixes, `APPROVE` refused, single-reviewer note in the body).

- [ ] **Step 1: Failing tests:** `TestParseRef` (valid, missing `#`, non-numeric, extra slashes); `TestGHCLIArgs` (fake `Run` records args for each call; PostReview sends event, commit_id, body and comments with path/line/body); `TestParseFileFormat` (the Global Constraints example round-trips); `TestMissing` (missing `### Less code`, a finding without `Call:`, `blocking` with `Verified: no`, each named); `TestPostFiltersAndLabels` (two findings, one disagreed: one comment `blocking: ...`; event `APPROVE` errors; `single` adds `Reviewed by one agent; the second reviewer was unavailable.` to the body).
- [ ] **Step 2: Run** `go test ./internal/review/`; expect FAIL.
- [ ] **Step 3: Implement** the three files.
- [ ] **Step 4: Run**; expect PASS.
- [ ] **Step 5: Commit** `feat(review): PR references, a gh client and the review file`.

### Task 3: Start a review session

**Files:**
- Create: `internal/server/review.go`, `internal/server/review_test.go`, `internal/review/state.go` (+ test), `internal/review/prompt.go`
- Modify: `internal/domain/session.go` (`Session`, `SessionConfig`), `internal/infra/sqlite/repository.go` + `repository_scan.go` (column `review_pr`, COALESCE upsert), `internal/server/handlers_session.go` (`createParams` :570, `handleCreate` :616, `SessionInfo` :27), `internal/usecase/flow_manager.go` (`designFirstPrompt` :233, copy `ReviewPR` :366), `internal/infra/git/manager.go` (new detached PR worktree), `internal/domain/interfaces.go` (`RepositoryManager`), `internal/server/protocol.go`, `internal/domain/events.go`, `cmd/aiman/session_cmd.go`, `cmd/aiman/serve.go`, `evals/harness/` (fake GitHub, `SetReviewGitHub`)
- Test: `evals/harness/cases_review.go` (`CaseReviewBlind` create half)

**Interfaces:**
- Consumes: `review.Ref`, `review.GitHub`, `review.PR` (Task 2).
- Produces: after `worktree add`, `git reset --soft $(git merge-base FETCH_HEAD origin/<base>)` so the PR is staged; `diff.patch` is `git diff --cached`; `files.txt` is `git diff --cached --numstat`; `Session.ReviewPR`, `SessionConfig.ReviewPR`, `SessionInfo.ReviewPR` (json `review_pr`); `createParams.Review string` (json `review`), CLI `--review owner/repo#n`; `func (m *Manager) SetupRemotePRWorktree(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, number int) (domain.Worktree, error)` (fetch `pull/<n>/head` into the main clone, `git worktree add --detach <repo>@pr-<n> FETCH_HEAD`; reuse the directory when it exists by checking out FETCH_HEAD detached); `type review.State struct{ Ref string; HeadSHA string; Revealed, Single bool }` with `review.LoadState(dir, id string) (State, error)` / `review.SaveState(dir, id string, st State) error`; `review.Prompt` (exact copy below); `func (s *Server) SetReviewGitHub(gh review.GitHub)`; to-dos seeded at create as `AuthorAiman`: `Blind review` (Ref `review:blind`), `Informed pass` (`review:informed`), `Verify findings`, `Publish the brief`, `Ask the three questions`, `Post the review`.

`review.Prompt`:
```
You are reviewing a pull request in this worktree. Follow the aiman skill's Review mode section. Start with .aiman_review/meta.json, .aiman_review/files.txt and .aiman_review/diff.patch. Do not read the PR description, commit messages or gh pr view until aiman review reveal succeeds. The PR's changes are staged on purpose so editors mark them; never commit, stash or reset in this worktree. Never approve the PR and never push to it.
```

- [ ] **Step 1: Failing tests:** `TestCreateReviewSession` (fake GitHub returns PR 7 head `abc123`; create with `review: "o/r#7"` saves a session with `ReviewPR`, smart tier, PromptFree with `review.Prompt` as the initial prompt and no design-first suffix; `.aiman_review/meta.json` holds number, base, head SHA, title, author; `diff.patch` and `files.txt` exist; no `.aiman_pr_description.md`; state file `HeadSHA abc123`; the six to-dos seeded; ledger `review.started`); `TestCreateReviewExists` (second create for the same PR while the first is live: `review_exists`); `TestSetupRemotePRWorktreeFork` (fake remote records `git fetch origin pull/7/head`, `worktree add --detach .../r@pr-7 FETCH_HEAD` and the `reset --soft` to the merge base); `TestDesignFirstSkipsReview` in usecase; sqlite round trip of `review_pr`.
- [ ] **Step 2: Run** `go test ./internal/server/ ./internal/usecase/ ./internal/infra/... ./internal/review/ -run 'Review|PRWorktree'`; expect FAIL.
- [ ] **Step 3: Implement.** `handleCreate` branches to `s.handleReview(ctx, req, params)` when `Review` is set, before `applyFor`. Diff: `git diff <base merge-base>...HEAD` in the worktree; files: `git diff --numstat`. Wire `review.GHCLI` in `serve.go`. The `verify.before_pr` check skips sessions with `ReviewPR` set (test `TestBeforePRSkipsReview`).
- [ ] **Step 4: Eval** `CaseReviewBlind` (create half: files present, description absent) with a harness fake GitHub; register in `LifecycleCases()`.
- [ ] **Step 5: Run** `go test ./... && make eval`; expect PASS.
- [ ] **Step 6: Commit** `feat(review): start a review session at a PR head`.

### Task 4: Reveal, refresh and review consults

**Files:**
- Modify: `internal/server/review.go`, `internal/coordinator/track.go` + `track_consult.go` (per-parent timeout, `consult.abandoned` ledger row), `internal/infra/config/config.go` (`ReviewConfig{ConsultTimeout}` in `Config`, `ServeConfig`, `MarshalServeConfig`), `cmd/aiman/serve.go`, `internal/server/server.go`, `internal/server/protocol.go`
- Create: `cmd/aiman/review_cmd.go`
- Test: `internal/server/review_test.go`, `internal/coordinator/track_test.go`, `evals/harness/cases_review.go`

**Interfaces:**
- Consumes: `review.State`, `review.GitHub` (Tasks 2, 3).
- Produces: RPC `review.reveal {session, single}`, `review.refresh {session}`; CLI `aiman review reveal [--single]`, `aiman review refresh`; `Tracker.ConsultTimeoutFor func(parent domain.Session) time.Duration` (review parents get `review.consult_timeout`, others the existing timeout); ledger `consult.abandoned {child, todo}` written by `abandonConsult`.

Reveal rules: refuse `review_not_ready` unless the `review:blind` to-do is done and every consult child of the session created before reveal has ended (`consult.finished` or `consult.abandoned` in the ledger). If no consult was ever created, or every one was abandoned, reveal also needs `single: true`, which sets `State.Single`. On success write `.aiman_pr_description.md` (title, body, base, linked issues from the body), set `Revealed`, record `review.revealed {single}`.

Refresh: fetch the new head into the same worktree, `git reset --hard FETCH_HEAD` then `git reset --soft` to the new merge base, rewrite `.aiman_review/`, delete `.aiman_pr_description.md`, set `Revealed=false`, `Single=false`, `HeadSHA` new, set `review:blind`, `review:informed` and `gate:review` back to pending as `AuthorAiman`, record `review.refreshed {from, to}`.

- [ ] **Step 1: Failing tests:** `TestRevealRefusesBeforeBlind`; `TestRevealWaitsForBlindConsult` (a consult linked and still running: refused; after `consult.finished`: allowed); `TestRevealSingle` (no consult: refused without `single`, allowed with it, state `Single`); `TestRevealWritesDescription`; `TestRefreshResets` (new head SHA, worktree staged against the new merge base with no files left from the old head, description removed, to-dos and gate pending, ledger row); `TestConsultTimeoutForReview` (review parent uses 30m default, other parent the handover timeout); `TestAbandonRecordsLedger`.
- [ ] **Step 2: Run** `go test ./internal/server/ ./internal/coordinator/ ./internal/infra/config/ -run 'Reveal|Refresh|ConsultTimeout|Abandon|Review'`; expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Eval** `CaseReviewBlind` (reveal half) and `CaseReviewSingle` (reveal with `single` and the state flag); register both.
- [ ] **Step 5: Run** `go test ./... && make eval`; expect PASS.
- [ ] **Step 6: Commit** `feat(review): reveal the description after the blind passes`.

### Task 5: Brief gate and posting

**Files:**
- Modify: `internal/server/handlers_design.go` (add `gate:review` on the first publish when `ReviewPR` is set), `internal/server/review.go`, `cmd/aiman/review_cmd.go`, `internal/server/server.go`, `internal/server/client.go` (post deadline 3 minutes)
- Test: `internal/server/review_test.go`, `evals/harness/cases_review.go`

**Interfaces:**
- Consumes: `review.ParseFile`, `File.Missing`, `File.Post`, `review.GitHub.PostReview`, `review.State` (Tasks 2-4); `todo.AddGate`, `todo.GateOpen` (Task 1).
- Produces: RPC `review.post {session, file, event}`; CLI `aiman review post <file> [--request-changes]` (default event `COMMENT`).

Post order: file confined to the worktree (reuse `readConfined`); `gate:review` open → `gate_needs_operator`; `File.Missing()` non-empty → `review_incomplete` naming the gaps; current PR head (from `GitHub.PR`) differs from `State.HeadSHA` → `review_head_moved`; event `APPROVE` → `invalid_params`; post with `commit_id = HeadSHA`; tick `Post the review`; ledger `review.posted {event, blocking, suggestion, question, nit, single}`.

- [ ] **Step 1: Failing tests:** `TestPublishAddsReviewGate` (first `design.publish` from a review session adds `gate:review`, a design session does not); `TestPostRefusals` (table: gate open, incomplete file, head moved, APPROVE, file outside the worktree); `TestPostPostsAgreedOnly` (fake GitHub records one call with event COMMENT, commit_id, and only agreed comments; ledger counts).
- [ ] **Step 2: Run** `go test ./internal/server/ -run 'Post|ReviewGate'`; expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Evals** `CaseReviewGate`, `CaseReviewHeadMoved` (including refresh reopening the gate), `CaseReviewIncomplete`; register them.
- [ ] **Step 5: Run** `go test ./... && make eval`; expect PASS.
- [ ] **Step 6: Commit** `feat(review): post one operator-approved GitHub review`.

### Task 6: Debug setup

**Files:**
- Create: `internal/review/debug.go`, `internal/review/debug_test.go`
- Modify: `internal/server/review.go`, `cmd/aiman/review_cmd.go`, `internal/server/server.go`

**Interfaces:**
- Consumes: `review.State`; `.aiman_review/reading-order.txt` (one `path:line` per line, written by the agent with the brief; Task 9 tells it to).
- Produces: RPC `review.debug {session, lang, target}`; CLI `aiman review debug [--lang go|node|react] [--target <package, script or dev command>]`; `func review.LaunchConfig(lang string, ports DebugPorts, worktree string, existing []byte) ([]byte, error)`; `type review.DebugPorts struct{ Debug, App, DevTools int }`; the session's `Tunnels` gain the debug ports (plus the app port and 8097 for react).

Behaviour: `go` runs `dlv dap --listen=127.0.0.1:<port>` in the worktree with an `--init` script of breakpoints from `reading-order.txt`; `node` runs `node --inspect=127.0.0.1:<port> <target>`; `react` runs the target dev command, `npx react-devtools` on 8097, and `node --inspect` when the dev server is Node. The processes run in a pty helper (`pty create` with the review session as parent) so they live and die with the session. Ports come from a free-port probe. `.vscode/launch.json` gets attach configurations named `aiman: attach <lang>`; an existing file is merged, never overwritten.

- [ ] **Step 1: Spike** (no commit): attach VS Code or `dlv connect` to a `dlv dap` started with `--init` breakpoints and record whether the client keeps the server-side breakpoints. If not, `LaunchConfig` lists the reading order in each configuration's `name`/description and the brief carries `path:line` links instead; record the result in the report.
- [ ] **Step 2: Failing tests:** `TestLaunchConfigGo` and `TestLaunchConfigNode` (attach request, port, worktree path mapping); `TestLaunchConfigMerges` (existing configurations kept); `TestDebugRegistersTunnels` (server test with a fake pty: ports appear in the session's `Tunnels`); `TestDebugInitFromReadingOrder` (one `break` per line, in order).
- [ ] **Step 3: Run** `go test ./internal/review/ ./internal/server/ -run 'Launch|Debug'`; expect FAIL.
- [ ] **Step 4: Implement.**
- [ ] **Step 5: Run** `make ci`; expect PASS.
- [ ] **Step 6: Commit** `feat(review): debug a PR from the laptop`.

### Task 7: Changed-line coverage

**Files:**
- Create: `internal/review/coverage.go`, `internal/review/coverage_test.go`, `internal/review/testdata/` (a cover profile, an Istanbul `coverage-final.json`, a diff)
- Modify: `internal/server/review.go`, `cmd/aiman/review_cmd.go`, `internal/infra/config/config.go` (`RepoConfig.Review.Coverage`), `internal/server/protocol.go`, `internal/domain/events.go` (`review.coverage`)

**Interfaces:**
- Produces: `type review.Hunk struct{ Path string; Lines []int }`; `func review.ChangedLines(diff string) []Hunk`; `func review.GoCoverage(profile string) (map[string]map[int]bool, error)`; `func review.IstanbulCoverage(data []byte, root string) (map[string]map[int]bool, error)`; `func review.CoverageReport(h []Hunk, cov map[string]map[int]bool) (md string, ran, missed int)`; RPC `review.coverage {session}`; CLI `aiman review coverage`.

Behaviour: a worktree with `go.mod` runs `go test -coverprofile=.aiman_review/cover.out` over the packages holding changed `.go` files; otherwise the repository's `review.coverage` command runs and `coverage/coverage-final.json` is read; neither gives `review_no_coverage`. `coverage.md` lists each hunk with ran and missed lines, then totals. Ledger `review.coverage {ran, missed}`. The run has a 10-minute context.

- [ ] **Step 1: Failing tests:** `TestChangedLines` (added and modified lines only, deletions ignored, renames use the new path); `TestGoCoverage` and `TestIstanbulCoverage` against testdata; `TestCoverageReport` (a hunk with one missed line reports it; totals); server `TestCoverageNoMethod` (`review_no_coverage`).
- [ ] **Step 2: Run** `go test ./internal/review/ ./internal/server/ -run Coverage`; expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make ci`; expect PASS.
- [ ] **Step 5: Commit** `feat(review): report which changed lines the tests ran`.

### Task 8: TUI

**Files:**
- Create: `internal/ui/pr_picker.go`, `internal/ui/pr_picker_test.go`
- Modify: `internal/ui/dashboard.go` (`renderModePicker` :3977, `handleModePickerUpdate` :6151, `handleRepoPickerUpdate` :6381, `handleLoadingUpdate` :6857, view-state enum :130, update/view dispatch), `internal/ui/create_remote.go` (:20), `internal/usecase/session_create_remote.go` (`RemoteCreateSpec`), `internal/ui/a1_detail.go` (`renderPRActions` :271, `gitRightParts` :212), `internal/ui/session_events.go` (`sessionFromInfo` :332), `internal/ui/session_merge.go` (:122)

**Interfaces:**
- Consumes: `SessionInfo.ReviewPR`, `createParams.Review` (Task 3); `review.PR` (Task 2) for the list rows.
- Produces: mode `[6] Review a PR`; `viewStatePRPicker`; `prsMsg{prs []review.PR, err error}`; `RemoteCreateSpec.Review string`.

The PR list is fetched over SSH with `gh pr list` (same JSON fields as Task 2) through a usecase helper `usecase.ListOpenPRs(ctx, remote, slug) ([]review.PR, error)`. Rows: `#<n> <title> · <author> · +<add> -<del>`, newest first, type-to-filter like the branch picker. After a PR is picked: agent picker, then create with `Review = "<slug>#<n>"`. The detail pane meta line shows `review #<n>` for a review session; `renderPRActions` and its call are deleted.

- [ ] **Step 1: Failing tests** (keys via `pressKey`/`pressRune`): `TestModePickerOffersReview`; `TestRepoPickThenPRPicker` (review mode after repo selection loads PRs into `viewStatePRPicker`); `TestPRPickerSetsReview` (enter on `#7` sets `sessionCfg.ReviewPR = "o/r#7"` and moves to the agent picker); `TestRemoteCreateSpecCarriesReview`; `TestDetailNoStaticPRActions`.
- [ ] **Step 2: Run** `go test ./internal/ui/ ./internal/usecase/ -run 'Review|PRPicker|PRActions'`; expect FAIL.
- [ ] **Step 3: Implement**, following `internal/ui/branch_picker.go`.
- [ ] **Step 4: Run** `make ci`; expect PASS.
- [ ] **Step 5: Commit** `feat(ui): start a review from a PR`.

### Task 9: Skill and docs

**Files:**
- Modify: `internal/aimanskill/SKILL.md`, `internal/aimanskill/install_test.go`, `AGENTS.md`

**Interfaces:**
- Consumes: every command, code and format above (Tasks 1-8).

- [ ] **Step 1: Failing test** `TestSkillDescribesReviewMode` (SKILL.md contains `## Review mode`, `aiman review reveal`, `aiman review post`, `approve-review`, and the four labels).
- [ ] **Step 2: Write SKILL.md `## Review mode`:** the phases (blind primary pass and blind consult, reveal or `--single`, informed pass and informed consult, merge and dedupe, verify each finding or relabel it `question`, chunking above 400 changed lines with an explicit reading order); the consult briefs for both passes (the blind one says "do not read the PR description"); the brief page via `aiman design publish --key pr-<n>` with its five sections; the three questions asked one at a time, before the agents' answers and findings are added in a second revision; recording the operator's words as quotes in the review file; the review file format exactly as in this plan; the gate and `approve-review`; `aiman review post` and its refusals; `aiman review refresh`; `aiman review coverage` (run it before the brief; changed lines no test ran become `question` findings; the brief shows the totals); writing `.aiman_review/reading-order.txt` with the brief and offering `aiman review debug` to the operator; the staged-changes rule; never approve, never push. Children-style escalation does not apply.
- [ ] **Step 3: AGENTS.md:** `ReviewPR` in the session table and COALESCE list, the review RPCs and refusals, the review gate, `review.consult_timeout`, the ledger kinds, the TUI mode and PR picker.
- [ ] **Step 4: Run** `make ci`, `make eval`, `graphify update .`; expect PASS.
- [ ] **Step 5: Commit** `docs: review mode in the skill and AGENTS.md`.
