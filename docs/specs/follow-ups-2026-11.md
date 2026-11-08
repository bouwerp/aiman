# Follow-ups, November 2026

Thirteen logged follow-ups built as one batch (to-dos t33–t36, t47–t50, t52, t54, t63, t64, t66). Each
section is one piece: the problem, the contract, and the acceptance tests the piece's child may not
edit. The design is the contract text; there is no page beyond this file. A piece that finds the
contract does not match the code escalates instead of guessing.

Conventions shared by every piece:

- Error codes are the existing ones in `internal/server/protocol.go` (`needs_operator`,
  `session_scope`, `invalid_params`, `not_found`, `forbidden`, `unavailable`, `worktree_dirty`).
- A session caller is what `isSessionCall` says (`internal/server/peer.go`); scope is `sessionScopeOf`.
- Every new or changed RPC is in `api/openapi.yaml` (`internal/server/openapi_spec_test.go` pins it)
  and classified in `internal/gateway/allowlist.go` (its test pins that).
- `AGENTS.md` gains or changes one sentence per piece, in the section that already describes the area.
- Source comments describe the code as it is; the "why we changed it" lives in the PR.

## F1 (t33) Close the attach master on detach

**Problem.** `AttachPTYSession` is the one SSH call that forwards the laptop's agent (`-A`). Its
master uses `ControlPersist=10m`, so for ten minutes after the operator detaches, a process on the
host can still reach the laptop's agent through that socket.

**Contract.**

- `(*ssh.Manager).CloseAttachMaster(ctx) error` runs `ssh -O exit -S <ttyControlPath()> <target>`
  and then removes the socket file (best effort). A missing master is not an error: an exit whose
  output says the control socket is gone (`No such file`, `Control socket connect`) returns nil. Any
  other non-zero exit returns the error with the output.
- The dashboard calls it once the attach command has returned, from the `tea.ExecProcess` callback
  that produces `attachDoneMsg`, and never blocks the UI on it (errors are logged with `m.log`).
- The bulk master is untouched. `-A` stays on the attach itself.

**Acceptance tests** (`internal/infra/ssh/attach_master_acceptance_test.go`, built on
`installFakeSSH`): the recorded call has `-O exit`, `-S` followed by the tty control path, the
target, and no `-A`; a fake that exits 255 with `Control socket connect(...): No such file or
directory` on stderr gives nil; a fake that exits 255 with another message gives an error.

## F2 (t34) GitHub denies the host: say so, once

**Problem.** Bulk SSH calls do not forward the agent (S4), so `git fetch`, `clone` and `pull` on the
host need the host's own GitHub credentials. When they are missing, git prints
`git@github.com: Permission denied (publickey).` and `isRetriableSSHTransportError` reads
`permission denied` as a dropped SSH transport: the call retries twice, falls back to an
unmultiplexed connection, and the operator sees a four-times-slower raw git error.

**Contract.**

- `ssh.IsRemoteGitAuthDenial(text string) bool` is true when the text holds a denial that came from
  the remote command rather than from the SSH client: it contains `Permission denied` and either
  `git@<host>: Permission denied` (any host) or `fatal: Could not read from remote repository`. A
  `fatal: Could not read from remote repository` with no `Permission denied` (a missing repository)
  is not a denial. `isRetriableSSHTransportError` returns false for
  such text, so `Execute` makes exactly one ssh call. The SSH client's own denial
  (`<user>@<host>: Permission denied (publickey,password).` with no `git@` and no git `fatal:`
  line) stays retriable (gotcha 5 in AGENTS.md).
- `git.ErrHostAuthDenied` is a sentinel. `git.WrapRemoteError(err error) error` returns `err`
  unchanged unless its text is a remote git auth denial, in which case the result wraps both
  `ErrHostAuthDenied` and `err` and its message starts with
  `the host cannot authenticate to GitHub: give it its own credentials (a deploy key, or gh auth
  login with the git credential helper); aiman does not forward the laptop's ssh agent to bulk
  calls`. Every remote git call in `internal/infra/git` that fetches, clones, pulls or pushes
  (`EnsureHealthyRepo`, `SetupRemoteWorktree`, `SetupRemoteWorktreeFromBranch`,
  `SetupSharedWorkspace`, `SetupRemotePRWorktree`, `FindExistingWorktree` where it fetches) wraps its
  error with it.
- The TUI shows the wrapped message as it shows any create error; no new view.

**Acceptance tests.** `internal/infra/ssh/git_denial_acceptance_test.go`: the two text cases above
for `IsRemoteGitAuthDenial` and `isRetriableSSHTransportError`; with a fake ssh that exits 128 and
prints the git denial, `Execute` records exactly one call and returns an error holding the output.
`internal/infra/git/host_auth_acceptance_test.go`: `WrapRemoteError` on a plain error returns it
unchanged; on the git denial text returns an error that `errors.Is` `ErrHostAuthDenied` and whose
message contains `give it its own credentials`; `SetupRemoteWorktree` over a fake remote whose
`git fetch` answers that denial returns an error that `errors.Is(err, ErrHostAuthDenied)`.

## F3 (t35) ciwatch trusted logins

**Problem.** S5 steers a review comment only from an `OWNER`, `MEMBER` or `COLLABORATOR`. Review
bots are `NONE` and members of a private organisation show as `CONTRIBUTOR`, so their comments are
recorded as ignored.

**Contract.**

- Config: `ci.trusted_logins` (a list of GitHub logins) on both `config.Config` and
  `config.ServeConfig` as `CI CIConfig `yaml:"ci,omitempty"`` with
  `TrustedLogins []string `yaml:"trusted_logins,omitempty"``. `MarshalServeConfig` copies it, so
  serve reads the laptop's list.
- `ciwatch.Deps.TrustedLogins []string`. A comment is trusted when `trustedAssociation` holds or
  its author login equals a trusted login ignoring case. Everything else about ignored comments is
  unchanged (`review.comment_ignored` once, seen, no to-do).
- `cmd/aiman/serve.go` passes `cfg.CI.TrustedLogins` into `ciwatch.Deps`.

**Acceptance tests.** `internal/ciwatch/trusted_logins_acceptance_test.go`: with
`Deps.TrustedLogins = ["review-bot"]`, a comment by `Review-Bot` with association `NONE` becomes a
`review:<id>` to-do and no `review.comment_ignored` row; a comment by `stranger` with `NONE` is
ignored as before; with no trusted logins the bot's comment is ignored.
`internal/infra/config/ci_config_acceptance_test.go`: a config file holding
`ci:\n  trusted_logins: [review-bot, alice]` loads into `cfg.CI.TrustedLogins`; the serve config
marshalled for a host contains `trusted_logins` with both; a config without the block marshals no
`ci:` key.

## F4 (t36) The gateway refuses to serve from inside a session

**Problem.** The gateway calls serve with no caller, so serve treats it as the operator. S8 phase 1
made it clear `AIMAN_ID`; but a gateway started inside a session's terminal is still a process in a
holder tree, and serve's peer check would read its calls as session calls or, worse, its device
calls as operator calls depending on the path. The clean rule is that it does not run there at all.

**Contract.**

- New package `internal/procwalk` holds the `/proc` readers now private to `internal/server/peer.go`:
  `ParentPID(pid int) (int, bool)`, `Cmdline(pid int) ([]byte, bool)`, `IsHolderCmdline(raw []byte)
  bool`, and `InHolderTree(pid int, parent func(int) (int, bool), cmdline func(int) ([]byte, bool))
  bool`, true when `pid` or one of its ancestors (at most 64 levels, stopping at pid 1 or an
  unreadable parent) is a holder process. `Self() bool` is `InHolderTree(os.Getpid(), ParentPID,
  Cmdline)`. `peer.go` uses the package; its behaviour and tests do not change.
- `gateway.ErrInsideSession` and `gateway.RefuseInsideSession(inHolderTree func() bool) error`,
  which returns `ErrInsideSession` when the function says true, else nil. Its message:
  `aiman-gateway refuses to serve from inside a session's terminal: start it from the operator's
  shell or its systemd unit`.
- `cmd/aiman-gateway` `run` calls `gateway.RefuseInsideSession(procwalk.Self)` before taking the
  instance lock, on the serving path only (CLI subcommands such as `pair` and `devices` still work
  from anywhere).
- `AGENTS.md`: the sentence "counts as the operator unless it was itself started inside a session's
  terminal" becomes "refuses to start inside a session's terminal (`gateway.RefuseInsideSession`)".

**Acceptance tests.** `internal/procwalk/procwalk_acceptance_test.go`: with fake readers, a chain
`300 → 200 → 100 → 1` where 100's cmdline is `aiman\x00pty\x00hold\x00--dir\x00/x\x00--id\x00s1` is
in the tree from 300, 200 and 100; the same chain with 100 a plain `bash` is not; a self-parent
loop ends false; `IsHolderCmdline` accepts a renamed binary and rejects `aiman pty list` and a
cmdline without `--id`. `internal/gateway/inside_session_acceptance_test.go`:
`RefuseInsideSession(func() bool { return true })` is `ErrInsideSession` by `errors.Is` and its
message contains `inside a session's terminal`; `false` gives nil.

## F5 (t47) Scope `pty.attach` and `pty.resize`

**Problem.** S1 scoped the typing and killing methods to the caller's tree. `pty.attach` (reads and
types) and `pty.resize` were left out.

**Contract.** Both call `refuseOutOfScope` on the target id before anything else. For `pty.attach`
the refusal is the first and only response line on the connection (written before `Subscribe`).
Operator calls are unchanged.

**Acceptance tests** (`internal/server/pty_scope_acceptance_test.go`, on `scopeServer`): for
`pty.resize`, every case of `scopeTargetCases(true)` answers `session_scope` exactly when the case
is not allowed, and an operator call applies; for `pty.attach` through `handlePTYAttachConn` with
an in-memory connection, caller `sess-b` attaching to `sess-a` reads one `session_scope` error line
and nothing else, caller `sess-a` attaching to `sess-c` reads `pty_attached`.

## F6 (t48) `session.register` refuses session callers

**Problem.** `session.register` writes identity fields, including `parent_id`. A session that
registers a row naming itself as the parent of any other session defeats the S1 scope check.

**Contract.** `session.register` answers `needs_operator` to a session caller (`isSessionCall`) and
saves nothing. The laptop (over SSH, outside any holder) and the gateway (which cannot be inside
one after F4) are unaffected.

**Acceptance tests** (`internal/server/register_scope_acceptance_test.go`, on `scopeServer`): a
call with caller `sess-a` naming `sess-b` with `parent_id` `sess-a` is `needs_operator` and
`sess-b`'s row keeps no parent; the same from the peer pid with no caller is refused too; an
operator call (no caller, no pid) still returns `sessions_registered`.

## F7 (t49) Skill: out-of-tree children

Docs only; the coordinator makes the change. `internal/aimanskill/SKILL.md`, after the `--orphan`
sentence: a child made with `--orphan`, or with `--parent` naming another session, is outside the
creator's tree, so the creator cannot prompt, steer, read or forget it afterwards (`session_scope`);
helpers the creator will drive stay its own children.

## F8 (t50) Push host credentials with the splice

**Problem.** `ApplyDelegatedCredentials` (the laptop's STS push) writes `~/.aws/credentials` on the
host through `MergeCredentialsIntoConfig`, which folds case (writing `phone` deletes `[Phone]`),
normalises line endings, drops a comment above the next section and adds blank lines on every
rewrite. `WriteHostCredentials` already splices for the same file.

**Contract.**

- `ApplyDelegatedCredentials` composes the new file with `spliceCredentialsSection(existing,
  profileName, FormatCredentialsSection(profileName, creds))`.
- `RemoveSessionProfile`'s credentials cleanup uses a new `removeCredentialsSection(existing,
  profile string) string` that drops only the sections whose header is exactly `[profile]` (case
  preserved) and copies every other byte; when the file has no such section it returns the input
  unchanged.
- `~/.aws/config` (`MergeProfileIntoConfig`) is out of scope.

**Acceptance tests** (`internal/infra/awsdelegation/push_splice_acceptance_test.go`, with a fake
`RemoteRunner` that answers the `cat` with a fixture and records `WriteFile`): applying `phone` to
`[Phone]\naws_access_key_id = A\naws_secret_access_key = B\n\n# keep me\n[other]\nx = 1\n` keeps the
`[Phone]` block, `# keep me` and `[other]` byte for byte and adds a `[phone]` block with the new
keys; applying the same credentials twice writes the same bytes the second time; `RemoveSessionProfile`
for `phone` removes `[phone]` and keeps `[Phone]`; CRLF input stays CRLF.

## F9 (t52) `session.file`: read a tapped path on the phone

**Problem.** The phone transcript shows the paths an agent read or edited; a tap copies the path.
The operator wants to read the file there.

**Serve contract.**

- `session.file {session, path, offset?, limit?}` (`internal/server/handlers_file.go`) answers
  `{"type":"file","session":<id>,"path":<absolute path>,"size":<bytes>,"offset":<bytes>,
  "data":<string>,"eof":<bool>,"binary":<bool>}`. `limit` defaults to 65536 and is capped at
  262144 (`FileMaxReplyBytes`); `offset` defaults to 0 and past the end gives empty data with `eof`
  true. The reply's `offset` is the offset the page starts at, so the next page starts at `offset`
  plus the byte length of `data`. `data` is cut on a UTF-8 boundary, so a client appends pages.
- Roots: the session's `WorktreePath` and `WorkingDirectory` after `EvalSymlinks`. A relative path
  resolves against `WorkingDirectory`. The resolved path (after `EvalSymlinks`) must lie inside a
  root, else `forbidden`. Also `forbidden` regardless of root: any path with a `.git` element, and a
  base name matching `.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa*`, `id_ed25519*` or `credentials`.
  A session with neither root is `unavailable`. A missing file is `not_found`; a directory is
  `invalid_params`.
- `binary` is true when the first 8 KiB holds a NUL byte; then `data` is empty and `eof` true.
- Session callers pass `refuseOutOfScope`. On the gateway it is a device read method.
  `api/openapi.yaml` gets the method, its params and the `FileResult` schema.

**Phone contract** (`bouwerp/aiman-phone`, after the serve piece lands and the phone copies
`api/openapi.yaml` byte for byte and regenerates `api/types.generated.ts`).

- A tap on a path in the transcript opens `app/session/[id]/file.tsx` with the path as a query
  parameter instead of copying. The screen shows the path in a header with a copy button (the
  existing "Copied" toast), then the file in a monospace, horizontally scrollable view with line
  numbers; "Load more" appends the next page while `eof` is false; `binary` shows one line saying
  so.
- An old serve (`invalid_params` with `unknown method`), `unavailable`, `forbidden` and `not_found`
  each show one line (`lib/fileView.ts`, `describeFileError(code: string, message: string)`), and
  the copy button
  still works.
- `lib/fileView.ts` also holds `fileHref(sessionId, path)` (the route with the path encoded) and
  `appendPage(prev, reply)` (concatenates `data` and tracks `offset` and `eof`).

**Acceptance tests.** Serve: `internal/server/session_file_acceptance_test.go` on a rig with a
temp worktree (`ptyServer` with a row whose `WorktreePath` and `WorkingDirectory` are the temp
dir): a relative and an absolute read of `notes.md` return its bytes with `eof` true and `size`;
`offset`+`limit` page through a 200 KiB file and the pages concatenate to the file; a symlink to a
file outside the root is `forbidden`; `.env`, `sub/.git/config` and `key.pem` are `forbidden`;
`missing.txt` is `not_found`; a file holding a NUL is `binary` with empty data; a session caller
outside the tree is `session_scope`; a session with no roots is `unavailable`;
`limit` 1_000_000 is capped at 262144. Phone: `tests/unit/fileView.acceptance.test.ts` for
`fileHref`, `appendPage` and `describeFileError` (old serve, forbidden, not_found, unavailable).

## F10 (t54) `--for` into another repository

**Problem.** `applyFor` defaults the child's base to the parent's branch. With `--repo` naming
another repository, that branch does not exist there and `git worktree add` exits 128.

**Contract.** `applyFor` defaults `Base` to the parent's branch only when the child's repository is
the parent's (`params.Repo` empty, or equal to `parent.RepoName` ignoring case). Otherwise `Base`
stays as the caller gave it, and empty means `SetupRemoteWorktree` detects that repository's
default branch. The child's branch name is still `ChildBranch(parent.Branch, item.ID)` and
everything else `applyFor` defaults is unchanged.

**Acceptance tests** (`internal/server/create_for_repo_acceptance_test.go`, on the `fakeCreator`
rig of `session_create_branch_test.go` plus a parent row with branch `feat` and repo `o/a` and a
to-do `t1`): `--for t1` reaches the creator with `Base` `feat`, `Repo` `o/a`, `Branch` `feat--t1`;
`--for t1 --repo o/b` reaches it with `Base` empty, `Repo` `o/b`, `Branch` `feat--t1`;
`--for t1 --repo O/A` keeps `Base` `feat`.

## F11 (t63) Stop the AWS login watchers before the engine closes

**Problem.** `watchAWSLogin` polls the engine every `watchEvery` until the login ends. Serve's
shutdown closes the engine while a watcher may still run, so a watcher can observe a closed engine
and write a `finished` row during shutdown.

**Contract.** `awsState` gains a stop channel and a wait group. `(*Server).CloseAWSLogin()` closes
the channel once (a second call returns at once) and waits for every watcher to return. A watcher
returns on the stop channel without writing. `cmd/aiman/serve.go` calls `srv.CloseAWSLogin()`
before `engine.Close()` in the closer that `wireAWSLogin` returns.

**Acceptance tests** (`internal/server/aws_login_stop_acceptance_test.go`, on the `scriptedLogin`
rig of `handlers_aws_test.go` with `watchEvery` one millisecond): start a login that stays
`pending`; `CloseAWSLogin` returns within a second; then make the scripted status `done` and wait
50 ms: the ledger holds `aws.login_started` and no `aws.login_finished`; calling `CloseAWSLogin`
again returns at once.

## F12 (t64) `aws.log`

**Problem.** The `aws.login_*` rows live under the fixed ledger id `aws-login`, which no session
owns, so `session.log` cannot read them and nothing shows the operator their sign-in history.

**Contract.**

- `aws.log {since_seq?, limit?}` answers `{"type":"aws_log","events":[SessionEvent…]}` from the
  ledger id `domain.AWSLoginLedger`, oldest first, through the same `ListSessionEvents` as
  `session.log` (`limit` 0 means the lister's default). A session caller gets `needs_operator`.
  On the gateway it is a device read method. `api/openapi.yaml` lists it.
- `aiman aws log [--since <seq>] [--limit <n>] [--json]` (`cmd/aiman/aws_cmd.go`) calls it and
  prints one line per row: `<at RFC3339> <kind> profile=<p> state=<s>` plus ` failure=<f>` when set;
  `--json` prints the result.

**Acceptance tests** (`internal/server/aws_log_acceptance_test.go`, on the rig of
`TestAWSLoginLedger`): after a login starts and finishes, `aws.log` returns the two rows in order
with their `seq`; `since_seq` equal to the first row's seq returns only the second; a session
caller is `needs_operator`. The CLI line format is pinned by a unit test the child writes against
a function `formatAWSLogLine(ev domain.SessionEvent) string`.

## F13 (t66) A parent terminates its own children

**Problem.** A session caller can only `session.forget` its children; the worktrees stay behind.

**Contract.** `session.terminate` accepts a session caller when the target is a strict descendant
of the caller under `sessionScope.covers` (helper ptys excluded: they have no row to terminate).
The caller itself is `needs_operator` (`a session cannot terminate itself`); a target outside the
tree is `session_scope`. Everything after the check is the operator path: `worktree_dirty` unless
`discard`, the forget handover, helpers, stop, `git worktree remove`, drop, `session.ended`. The
target's own children are left as `children_left`, as today. The gateway allowlist is unchanged
(`session.terminate` already needs `allow_destructive`). The skill's Coordinator section says a
coordinator may `aiman session terminate <child>` once a child's work has landed.

**Acceptance tests** (`internal/server/terminate_scope_acceptance_test.go`, on `newLifecycleRig`
with rows `a` (parent), `c` (child of `a`, a linked worktree), `d` (child of `c`) and `b`
(unrelated), and `procInSession` admitting pid 4242): caller `a` terminating `c` removes `c`'s
clean worktree and drops the row; caller `a` terminating `d` succeeds; caller `b` terminating `c`
is `session_scope` and touches nothing; caller `a` terminating `a` is `needs_operator`; caller `a`
terminating a dirty `c` without `discard` is `worktree_dirty` and with `discard` removes it.

## Breakdown

| Piece | Owns files | Parallel group | Depends on | Complexity | Tier |
|---|---|---|---|---|---|
| F1+F2 ssh/git | `internal/infra/ssh`, `internal/infra/git`, dashboard attach site | 1 | | M | smart |
| F3+F8 ciwatch/creds | `internal/ciwatch`, `internal/infra/config` (CI block), `cmd/aiman/serve.go` ciwatch deps, `internal/infra/awsdelegation/push.go` | 1 | | M | smart |
| F11+F12 aws log | `internal/server/handlers_aws.go`, `cmd/aiman/aws_cmd.go`, `cmd/aiman/serve.go` closer, dispatch, allowlist, openapi | 1 | | M | smart |
| F5+F6+F10+F13 scope | `internal/server/handlers_pty.go`, `handlers_session.go` (register, applyFor), `handlers_lifecycle.go`, skill | 1 | | M | smart |
| F4 gateway guard | `internal/procwalk` (new), `internal/server/peer.go`, `internal/gateway`, `cmd/aiman-gateway` | 2 | group 1 merged | M | smart |
| F9 serve file | `internal/server/handlers_file.go` (new), dispatch, allowlist, openapi | 2 | group 1 merged | M | smart |
| F9 phone viewer | aiman-phone `app/session/[id]/file.tsx`, `lib/fileView.ts`, `lib/links.ts` tap, `api/*` | 3 | F9 serve merged | M | smart |
| F7 docs | `internal/aimanskill/SKILL.md`, `AGENTS.md` | coordinator | | S | |

Pieces in one group own disjoint files. Group 2 waits for group 1 because both touch `server.go`'s
dispatch, the allowlist and `api/openapi.yaml`.
