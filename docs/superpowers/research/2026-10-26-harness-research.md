# Harness research: what to take from the coding-agent course and @alindnbrg

Date: 2026-10-26
Status: input to the Next Level plans (see `docs/superpowers/plans/2026-10-22-nl-*.md`)

## Sources

- `github.com/decodingai-magazine/building-a-coding-agent-from-scratch-course`
  (main, 2026-10-26): README, `AGENTS.md`, ADRs 0002–0023, `src/decode/`,
  `evals/`, `tasks/`, lessons 4, 7 and 8.
- André Lindenberg (`@alindnbrg`): his public archive at
  `andrelindenberg.de` and its RSS. 1,000 most recent LinkedIn posts
  (2025-06-27 → 2026-10-25), 364 read in depth. LinkedIn itself is not
  scrapable; the archive stores the post text and links the original.
- The aiman tree at `research-for-better-harness`, including the half-built
  `internal/usage` package and the unused `detailMetaExtras` placeholder.

## Thesis

Both sources converge: the model is a dependency, the harness is the product.
The course opens with LangChain's Terminal-Bench result — same model, harness
changes only, ~30th to top 5. The feed's recurring framing is "the harness is
the product, the model is a dependency" and "the unit that fails in production
is the model-harness-environment loop."

Aiman is a harness for other people's harnesses. Its product is the cross-agent
control plane. It already has embryonic versions of five of the six primitives a
production harness needs:

1. execution loop — vendor agent in tmux/PTY plus a steering queue
2. tool registry — the `aiman` CLI exposed through the bundled skill
3. context manager — `contextstore`, `handover`, skill injection
4. state store — SQLite plus per-session JSON
5. lifecycle hooks — `agenthook` (SessionStart/End, Stop, UserPromptSubmit,
   Notification/idle_prompt, PreCompact, PostToolUse for to-dos)
6. evaluation interface — missing

The missing primitive is the highest-leverage one. Aiman has extensive unit
tests but no behavioural eval, benchmark, replay, mining or quality gate; the
v0.19.23–v0.19.28 regressions were all in a layer the unit tests did not cover.

## How this maps to the Next Level program

| Research item | Already planned | Action |
|---|---|---|
| Handover | nl-3a, shipped | amend: skeleton, evidence, compaction pointer |
| Usage and cost | nl-4, T1–T4 shipped | amend T5–T11: context gauge, health, budgets |
| Memory | nl-3b | amend: provenance, verification, facts vs habits |
| Verifier | nl-7 | amend: tree-hash evidence, structural gate, reproduce-first |
| CI reaction | nl-6 | later: post-merge test/bisect is a separate slice |
| Notifications | nl-5 | no change |
| Faster starts | nl-8 | no change; bootstrap/cache work already matches |
| Coordinator | nl-9 | amend: child brief contract, report validation |
| Quality kit | nl-10 | amend: only evidence ticks `Done-when met` |
| Evaluation interface | none | **new plan: nl-11 (W10)** |
| Session event ledger | none | nl-11, task 1 |
| Hook enforcement | none | nl-7 task 14 (Claude/Codex first) |
| Skill lifecycle | none | backlog; not this program |
| Agent conformance | README phase 2 only | backlog; not this program |
| Workflow-as-code | rejected by `d6a6a83` | do not reopen |
| Model routing | explicit non-goal | do not add |

## What the research adds, in one line each

1. **Evals + ledger.** Behavioural regression cases grown from real failures,
   driven by a deterministic fake agent and a durable `session_events` stream.
2. **Context telemetry.** Occupancy from the last assistant turn, a 60/80%
   gauge, and read:edit / edits-without-read / first-test health signals.
3. **Evidence-backed verification.** A pass is bound to a tree hash and a test
   command; a PR cannot open without it; a bug fix must reproduce first.
4. **Handover as compaction.** Fixed skeleton, verbatim error/test evidence, and
   a pointer queued after `PreCompact`, not only after a restart.
5. **Memory governance.** Author, verified, source, confidence; facts supersede,
   habits need evidence; verified notes rank first.
6. **Coordinator contracts.** Each child gets a question/scope/report brief and
   must return evidence before its parent's to-do is marked done.
7. **Structural Done-when.** The agent cannot tick the final item on its own
   word; only a verification record (or the user) can.

## What was deliberately rejected

- **Workflow-as-code / graph engine.** The repo replaced spec-mode fan-out with
  the coordinator in `d6a6a83`. Reopening it would undo a reviewed design
  decision.
- **A fan-out key or spec wizard.** Same reason. Harden child briefs instead.
- **Model routing.** Explicit Next Level non-goal.
- **Board/kanban or inbox layouts.** Explicit Next Level non-goals.
- **Automatic code-link memory staleness.** The context store has no repo or
  worktree identity; `context verify` plus a source hash is the honest first
  step.
- **Opik/Kitaru in the eval path.** Wrong stack for this Go repo. The fake agent
  and a Go test driver cover the deterministic track; a real-agent track stays
  opt-in and cost-capped.

## Evidence highlights

- Course ADR-0006 / lesson 4: compaction at ~60% and ~80%, fixed summary
  skeleton, recent tail, boundary snap, memory cap, context gauge.
- Course ADR-0017 / 0022 and lessons 7–8: benchmark plus regression cases,
  deterministic metrics first, oracle sanity both directions, never judge a
  capped run, grow cases from real failures.
- Course ADR-0017 (fan-out): input contract, output validation, labelled
  aggregate, per-child limits.
- `@alindnbrg` 2026-05-03: six harness primitives (execution loop, tool
  registry, context manager, state store, lifecycle hooks, evaluation).
- 2026-05-02: Pi's SWE-bench jump from 6.7% to 68.3% by changing the edit-tool
  format; Stripe's 1,300 PRs/week through harness design.
- 2026-07-16: "done is structural" — a hook blocks a pass until evidence exists;
  a separate evaluator grades from a context that never saw the build.
- 2026-10-12 / 2026-08-27: the writer must not judge; the verifier must
  reproduce the failure before believing the fix.
- 2026-08-27 / 2026-04-13 / 2026-04-07: checkpoint around compaction, audit
  stale memory and unused skills, read:edit ratio as an early regression signal.
- 2026-09-22 / 2026-10-25 / 2026-09-13: claim symbols before editing, test after
  merge and bisect/revert, share build caches across worktrees.
- 2026-09-01 / 2026-09-07 / 2026-10-17: facts supersede, habits need evidence,
  verified vs generated notes, memory pinned to the code it describes.
- 2026-10-01 / 2026-05-04 / 2026-09-26: skill eval suites with pinned graders,
  skill linting, and load/opportunity telemetry.

## Decision log

- **One new plan (nl-11), not five.** The other changes are additive tasks in
  the plans that already own the code. This keeps the roadmap's "each ships on
  its own" property.
- **The ledger is a table, not an event-sourcing rewrite.** `session_events`
  with `(session_id, seq)` and a payload hash; append from the handlers that
  already mutate state. No new database or bus.
- **Evals are deterministic first.** The fake-agent track is free and runs in
  CI; the real-agent track is opt-in, cost-capped, and never judges a capped
  run.
- **Hook enforcement is per-agent capability.** Claude and Codex get PreToolUse
  first; an agent without it is marked `unprotected`, never faked.
- **No abstraction for one caller.** `Evidence` is a struct on `Review` and
  `handover.Input`, not a service.
