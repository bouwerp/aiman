# Task 3 report: start a review session

Status: DONE. Commit `aecf268 feat(review): start a review session at a PR head`.

## What was built

- `domain.Session.ReviewPR`, `SessionConfig.ReviewPR`; sqlite column `review_pr` (ALTER, insert, COALESCE upsert, scan); AGENTS.md COALESCE list updated.
- `FlowManager.CreateSession` copies `ReviewPR`; `designFirstPrompt` skips it; `editsGitignore(cfg)` stops create editing `.gitignore` in a review worktree.
- `git.Manager.SetupRemotePRWorktree(ctx, remote, repo, number, base)` (internal/infra/git/pr_worktree.go): ensures the main clone, fetches `pull/<n>/head` and runs `worktree add -q --detach <repo>@pr-<n> FETCH_HEAD`. A healthy existing worktree is reused: it fetches the head itself (FETCH_HEAD is per worktree), then `checkout --force --detach FETCH_HEAD`. It then soft-resets to `merge-base HEAD origin/<base>` (`git.StagePR`) and adds `.aiman_*` to `info/exclude`. A path holding another repo's worktree is an error and is never deleted. Added to `domain.RepositoryManager`.
- internal/review: `State`, `LoadState`, `SaveState` (atomic, validated id), `Prompt` (exact text), `ExecRun`/`ExecRunInput` (stdout only; stderr goes into the error).
- internal/server/review.go: `handleReview` is branched from `handleCreate` before consult and `applyFor`. It:
  - refuses consult, quick and for; refuses `review_exists` while a live session (not ended, not cleanup/error/inactive) has the same `ReviewPR`;
  - names the session `pr-<n>`, picks the smart tier unless an agent is named, and reads the PR from `review.GitHub`;
  - makes the worktree, then writes `.aiman_review/diff.patch` (`git diff --cached`), `files.txt` (`--numstat`) and `meta.json` (ref, number, base, head_sha, title, author; no body) before the agent starts;
  - creates with `PreparedWorktree`, `PromptFree`, `InitialPrompt = review.Prompt`, saves the row with `ReviewPR`, writes the state file, seeds the six to-dos as `AuthorAiman` (refs `review:blind`, `review:informed`), and records `session.created` and `review.started {ref, base, head_sha}`.
- New setters: `SetReviewGitHub`, `SetReviewWorktrees` (`ReviewWorktrees` interface) and `SetReviewDir`. serve.go wires `review.GHCLI{ExecRun, ExecRunInput}`, the git manager and `~/.aiman/reviews`.
- `SessionInfo.ReviewPR` (`review_pr`). `CodeReviewExists`, `EventReviewStarted`. CLI `session create --review owner/repo#n`.
- verify.before_pr: `verifyStatusForTree` returns disabled for a session with `ReviewPR`, and `Unprotected` is false for one.
- Evals: `FakeGitHub`, `Harness.EnableReview` (production git manager over FakeRemote) and `CaseReviewBlind` (create half plus the `review_exists` refusal), registered in `LifecycleCases()`.

## TDD record

- RED: sqlite round trip (no `ReviewPR` field); `TestDesignFirstSkipsReview` (suffix appended); git PR worktree tests (method undefined); review state tests (undefined); server `TestCreateReviewSession`/`TestCreateReviewExists`/`TestCreateReviewRefusals`/`TestBeforePRSkipsReview` (build failed: setters, field, code, event missing); `.aiman_*` showed in `git status` before info/exclude; `TestReviewLeavesGitignoreAlone`; `TestStoreListSkipsForeignRecords` (state file listed as a review); CLI `--review`; `review.ExecRun`.
- GREEN: all of the above. `TestSetupRemotePRWorktreeStagesThePR` uses real git: an origin whose PR exists only as `refs/pull/7/head` (fork case), main moved on after the fork point. It checks the staged files, PR-head content and a clean status for `.aiman_review`, then moves the head and checks that the reused worktree is restaged with the dropped file gone. The eval was checked to fail when a metric is broken. Its RED was not recorded separately, because the implementation already existed when it was written.
- `make ci`: all checks passed, golangci-lint 0 issues. `make eval`: all cases PASS, including review-blind; the oracle-broken FAIL lines are by design.

## Deviations and concerns

1. `SetupRemotePRWorktree` takes an extra `base string`. The brief's signature had no base, but the soft reset needs `origin/<base>`.
2. The merge base is `merge-base HEAD origin/<base>`, run in the worktree, not `FETCH_HEAD` in the worktree. FETCH_HEAD is per worktree, so after a main-clone fetch it does not exist there. The result is the same.
3. `~/.aiman/reviews` is also the verify store's directory. `verify.Store.Get` now treats a record whose `id` differs from its file name as not found, so review state files never list as verifier reviews.
4. `meta.json` and the state record the head the soft reset staged (`ORIG_HEAD`), falling back to GitHub's `headRefOid`, so a push between the PR read and the fetch is recorded truthfully.
5. The review repo is passed with no clone URL, like other serve creates, so the repo must already be cloned on the host (or recoverable from its origin).
6. `handleCreate`'s agent/tier pick was extracted into `launchFor`, shared with `handleReview`. Behaviour is unchanged and the existing tier tests pass.
7. The bootstrap (`repos.<slug>` copy/symlink/setup) does not run for review worktrees, because PreparedWorktree skips it.

## Fix round 1

Commit: `fix(review): keep PR config out of the review agent and its own state dir`.

### Changes

1. **The agent never runs in the PR worktree (critical).**
   - serve picks the session id first (new `SessionConfig.SessionID`, honoured by `FlowManager.CreateSession` and both fake creators).
   - It makes `~/.aiman/pr-reviews/<id>/work/`, runs `git init` there, and writes `.aiman_review/{diff.patch,files.txt,meta.json}` into it. The diff and numstat still come from the PR worktree, and `meta.json` gains `worktree`.
   - The agent launches with that scratch directory as `PreparedWorktree`, so create's trust and the boot-wait dialog acceptance see only the scratch directory. The PR worktree is never handed to the creator.
   - The saved row has `WorktreePath` = PR worktree and `WorkingDirectory` = scratch.
   - `review.Prompt` gains the sentence `The pull request is checked out at the path in .aiman_review/meta.json (worktree); read it there.` after the first.
   - Two more paths would have touched the PR worktree and are closed:
     - a TUI restart now trusts only the working directory for a review session and points the `.gitignore` fix-up at the scratch directory (`restartRoot`, plus a guard in `restartTerminal`);
     - serve start no longer copies the skill into a review's PR worktree (`skillRoots`).
2. **State directory.** State is now `~/.aiman/pr-reviews/<id>.json`. `SetReviewDir`, the default, the docs, the `State` doc, serve.go and the harness `ReviewDir` all use `pr-reviews`. The `verify.Store.Get` change and `TestStoreListSkipsForeignRecords` are reverted.
3. **Cleanup on failure.** A failed Save, state write or to-do seed after `CreateSession` stops and forgets the session (`abandonReview`) and removes its scratch directory and state file. A failure before create removes the scratch directory.
4. **Parameter refusals.** `repo`, `branch`, `dir`, `prompt`, `issue`, `brief` and `brief_file` with `review` are refused as `invalid_params`, as are consult, quick and for.
5. **Per-PR lock.** A per-PR lock (`reviewLocks`) is held from the live-review check, which re-reads the sessions under the lock, through the save.
6. **Origin check.** `checkOrigin` runs before `ensureHealthyRepo` and before any fetch. It reads `git remote get-url origin` and requires it to name owner/repo (https, ssh:// or scp form, any case, optional `.git` or trailing slash). Otherwise it returns `domain.ErrOriginMismatch` naming both, and serve maps that to `invalid_params`.

### Tests

- RED, then GREEN:
  - server: `TestCreateReviewSession` (scratch cwd, row paths, meta `worktree`, nothing in the PR worktree), `TestReviewAgentNeverRunsInThePRWorktree`, `TestCreateReviewConcurrent`, `TestCreateReviewRefusals` (extended), `TestCreateReviewOriginMismatch`, `TestCreateReviewCleansUpOnFailure`; these failed to build until `SessionID` existed;
  - git: `TestOriginNamesRepo`, `TestSetupRemotePRWorktreeRefusesForeignOrigin` (no fetch is run); the real-git test's origin path now ends in `o/r`;
  - review: `TestPromptPointsAtTheWorktree`;
  - ui: `TestRestartReviewNeverTrustsPRWorktree`.
- `TestSkillRootsSkipReviewWorktree` was written with its implementation; no separate RED run.
- The eval `review-blind` gained `agent_runs_in_scratch`, `meta_names_worktree` and `pr_worktree_untouched`. The harness `FakeRemote.Reply` answers the origin query.
- `go test -race ./internal/server/ -run Review -count=2`: ok.

### Commands and output

- `make ci`: exit 0, "0 issues.", "All CI checks passed!"
- `make eval`: exit 0. `review-blind PASS`; the oracle-broken `prompt-duplicate` and `handover-missing-evidence` print FAIL by design; `ok github.com/bouwerp/aiman/evals/harness 65.498s`.

## Fix round 2

Commit: `fix(review): no path launches an agent in the PR worktree`.

### Changes

1. **Change directory (`c`).** `c` on a session with `ReviewPR` is refused with the toast `review sessions run in their own directory; c does not apply`. The change-dir confirm step refuses as well, whatever led to it.
2. **PR checkouts are never offered or launched in.** New `domain.InPRWorktree(path)` (any path component matching `<repo>@pr-<n>`) and `domain.CheckLaunchDir` / `ErrPRWorktreeLaunch` are enforced at every layer:
   - Discovery step 2 and `OrphanWorktreeSessions` (the revive-worktree screen) skip PR checkouts (`offerableWorktree`), live review or not. No phantom row is created, and nothing is listed.
   - `TrustWorkspacePaths` drops any PR-checkout path, so no caller can trust one.
   - `CreatePTYSession` and `RecreatePTYSession` refuse a `Dir` inside a PR checkout, which covers create, restart and revive. `RecreatePTYSession` refuses before the old process is stopped.
   - `restartTerminal` refuses before trusting or running anything.
   - `FlowManager.CreateSession` refuses a worktree or working directory inside a PR checkout before any write.

   Judgment call on (a): I did not add the PR worktree to the PTY holder meta. Discovery now never treats a `<repo>@pr-<n>` checkout as uncovered, while its review lives or after it ends, so the phantom cannot arise. The only path to such a checkout is `SetupRemotePRWorktree`, whose directory name the rule matches. The rule also catches a stored row's path.

   Side effect: an ordinary branch literally named `pr-<n>` (worktree `<repo>@pr-<n>`) can no longer host an agent.
3. **Unreadable origin.** The origin is read with `git config --get remote.origin.url`. If it cannot be read before `ensureHealthyRepo`, the clone is left to that recovery. The origin is then read strictly after recovery: a read failure is a create failure, never `ErrOriginMismatch`, and nothing is fetched.
4. **github.com only.** The origin must be github.com/<owner>/<repo> over https, http or ssh:// (any user or port), or scp-style `user@github.com:owner/repo`. Host and path are case-insensitive, with optional `.git` or trailing slash. Other hosts, look-alike hosts, local paths and `file://` are refused.

   I use the raw configured URL, not `remote get-url` (which applies `insteadOf` rewrites). The check judges the repository the operator configured. The real-git test uses `insteadOf` to point the github.com origin at a local upstream.
5. **Letter case.** The per-PR lock key is lower-cased (`reviewLockKey`) and `liveReview` compares with `EqualFold`, so `O/R#7` and `o/r#7` are one review.
6. **Caller-chosen session id.** `FlowManager.CreateSession` rejects a caller `SessionID` that is not a canonical UUID, or that a stored session already has. The store is attached with `SetSessionStore`, wired to the sqlite repository in serve.go.

### Tests (each was RED first, then GREEN)

- ui: `TestChangeDirRefusedForReview`, `TestChangeDirConfirmRefusedForReview`, `TestRestartRefusesPRWorktreeDir` (runs nothing)
- usecase: `TestPRWorktreesAreNeverOrphans` (Discover and OrphanWorktreeSessions, with and without aiman-id), `TestTrustSkipsPRWorktree`, `TestCreatePTYSessionRefusesPRWorktree`, `TestCreateSessionRefusesPRWorktreeDir`, `TestCreateSessionChecksCallerSessionID`
- domain: `TestPRWorktreePaths`, `TestCheckLaunchDir`
- git: `TestOriginNamesRepo` (host cases), `TestSetupRemotePRWorktreeUnreadableOrigin`
- server: `TestCreateReviewExistsIgnoresCase`

The eval harness answers `config --get remote.origin.url` with `git@github.com:o/r.git`.

### Commands and output

- `make ci`: exit 0, "0 issues.", "All CI checks passed!"
- `make eval`: exit 0. `review-blind PASS`; the oracle-broken `prompt-duplicate` and `handover-missing-evidence` print FAIL by design; `ok github.com/bouwerp/aiman/evals/harness 65.143s`.
