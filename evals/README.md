# Harness evals

`make eval` runs the deterministic regression suite for aiman's session
harness. It starts an in-process `aiman serve` on a temp unix socket, a temp
SQLite database, a fake remote and a fake creator. It needs no SSH host, no
agent binary and no network.

## Cases

The current suite covers:

- `prompt-once`: an idle session receives one steer, typed and submitted once.
- `steer-held-dialog` / `steer-held-working`: a note is held until the agent
  reports idle, then delivered once.
- `todo-mirror`: an agent status-only mirror keeps the user's text.
- `handover-restart` / `handover-precompact`: the fixed handover skeleton and
  the restart or compaction pointer.
- `event-sequence`: the session ledger's per-session sequence is contiguous and
  every payload has a hash.

Cases that need features still landing are added with those features:
`restart-preserves-worktree`, `terminate-keeps-pane` and `child-spawn`.

## Metrics

`evals/harness/metrics.go` scores the cases that apply to it:

`prompt_submitted_once`, `steer_delivered_after_ready`,
`no_duplicate_steer`, `handover_sections_complete`,
`todo_mirror_preserves_user_text`, `worktree_untouched_on_restart`,
`event_sequence_contiguous`. `verify_evidence_present` joins after the verifier
lands. A metric is 0 or 1 with a reason; a case passes only when every metric
that applies is 1.

## Oracle

`evals/harness/oracle.go` defines a good and a broken variant. The test asserts
the good variant passes every case and the broken variant fails exactly the
metrics it breaks. A broken harness cannot score like a good one.

## Scope

This suite grades aiman's harness, not a model. It uses no LLM judge and no
public coding benchmark. The real-agent track is opt-in, cost-capped and
nightly; a capped run is never judged.
