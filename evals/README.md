# Harness evals

`make eval` runs the deterministic regression suite for aiman's session
harness. It starts an in-process `aiman serve` on a temp unix socket, a temp
SQLite database, a fake remote and a fake creator. It needs no SSH host, no
agent binary and no network.

## Cases

The current suite covers:

- `create-ticket`: the fake creator writes `.aiman_task.md`, the ledger records
  `session.created`, and no to-do list is created yet.
- `prompt-once`: an idle session receives one steer, typed and submitted once.
- `steer-held-dialog` / `steer-held-working`: a note is held until the agent
  reports idle, then delivered once.
- `todo-mirror`: an agent status-only mirror keeps the user's text.
- `todo-user-edit`: a user change steers once, an agent mirror does not, and
  `todo.changed` is recorded.
- `handover-restart` / `handover-precompact`: the fixed handover skeleton and
  the restart or compaction pointer.
- `handover-child`: a `pre_compact` handover starts a handover child (played
  by the case, which writes `handover.md`, `memories.json` and `done` into the
  run's scratch dir). The compaction pointer is held while the child runs;
  then the worker's `.aiman_handover.md` is the child's, the memory job carries
  its candidates, and the pointer is queued.
- `handover-child-fallback`: a child that writes nothing times out. The
  mechanical handover stays, the memory job has no candidates, and the held
  pointer is released.
- `terminate-keeps-pane`: the terminal is stopped, but the row and the last
  capture survive until archive or forget.
- `usage-context`: a two-turn Claude transcript reports the last turn's
  input + cache_read against the explicit context window.
- `usage-budget`: crossing 70% and 100% emits one `usage.budget` event per
  threshold, and `usage.get` reports the same used tokens and state.
- `verify-pass`: a reviewer marker plus a successful `verify.test_command`
  records evidence and reaches `Passed`.
- `verify-missing-evidence`: no test command leaves the review incomplete and
  open.
- `verify-blocked-pr`: a before-PR gate with no passing review of the tree is
  denied and writes `verify.recorded` with `kind=gate`.
- `verify-reproduce-first`: a base that already passes ends the review with a
  `not reproducible` finding and spawns no reviewer.
- `event-sequence`: the session ledger's per-session sequence is contiguous and
  every payload has a hash.
- `child-spawn`: `session.create --for t1` links the parent's item to a child
  branched off the parent's branch. The production `coordinator.Tracker`, fed
  fake PR states, leaves t1 open for a PR merged into another base or still
  open, then marks it done once the PR merges into the parent's branch with a
  report citing a file and a test command.
- `child-no-evidence`: a merged child with no report keeps t1 open, and two
  tracker passes ask the parent for the report once.
- `child-vanished`: a forgotten child clears t1's link, keeps it open, and
  raises one notification.

`restart-preserves-worktree` is added with the restart harness.

## Metrics

`evals/harness/metrics.go` scores the cases that apply to it:

`prompt_submitted_once`, `steer_delivered_after_ready`,
`no_duplicate_steer`, `handover_sections_complete`,
`todo_mirror_preserves_user_text`, `worktree_untouched_on_restart`,
`event_sequence_contiguous`, and `verify_evidence_present` on verifier cases.
A metric is 0 or 1 with a reason; a case passes only when every metric that
applies is 1.

## Oracle

`evals/harness/oracle.go` defines a good and a broken variant. The test asserts
the good variant passes every case and the broken variant fails exactly the
metrics it breaks. A broken harness cannot score like a good one.

## Real-agent track

The real-agent track is opt-in and never runs in `make ci` or `make eval`. It
is compiled only with `-tags evalreal` and requires:

```bash
AIMAN_EVAL_REAL=1
AIMAN_EVAL_AGENT=claude
AIMAN_EVAL_DRIVER=/path/to/a/real-agent-driver
AIMAN_EVAL_BUDGET_USD=5
go test -tags evalreal -v ./evals/harness -run TestRealAgent -count=1
```

The driver receives `AIMAN_EVAL_CASE`, `AIMAN_EVAL_PROMPT`, `AIMAN_EVAL_OUT`
and `AIMAN_EVAL_AGENT` for each of the five cases. It must run in the current
worktree, write its ledger/evidence under `AIMAN_EVAL_OUT`, and may write
`cost.json` (`{"usd": ...}`). The runner writes a summary per run under
`evals/out/<run-id>/`, stops at the budget cap, and marks the run `capped`; a
capped run is never judged. `.github/workflows/eval-nightly.yml` runs the same
command nightly when the repository secrets are configured.

## Scope

This suite grades aiman's harness, not a model. It uses no LLM judge and no
public coding benchmark. `make eval` is fake-agent and spends nothing; only the
opt-in track above uses a real agent and money.
