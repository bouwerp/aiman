# nl-12: Session handover by a child agent (spec + plan)

User decisions (2026-10-28): the handover is rewritten by a child agent session, which also proposes the memory candidates. The child runs on every trigger, including pre_compact. The TUI writes the forget handover at teardown step 0. A successful restart/agent_switch child queues a "handover rewritten" note.

## 1. Spec

### Problem
- `writeHandover` (`internal/server/handover.go:206-235`) builds the handover mechanically from pane tail, to-dos, undelivered notes and diffstat. It cannot see what the agent decided or why.
- Memory candidates are proposed on the laptop by `intel.ProposeMemories` (Ollama) (`internal/ui/memory_candidates.go:100-106, 358-368`). With AI off, `NoopIntelligence` returns `ErrIntelligenceUnavailable`, so the feature produces nothing.

### Facts from the code that shape the design
- Restart and agent_switch never queue a pointer note. The TUI calls only `handover.write` (`internal/ui/dashboard.go:7736`) and puts `HandoverPrompt` into the new agent's initial prompt (`internal/ui/restart_handover.go:22-31`).
- `handover.point` is called only by `ReviveIfNeeded` (`internal/usecase/session_terminal.go:762`) and `notePreCompactHandover` (`handover.go:188`).
- The steerer delivers a session's whole pending batch as one message (`internal/server/steer.go:358-403`), so a hold in the delivery gate would block unrelated notes. The hold therefore happens at enqueue time.
- TUI teardown removes the worktree before `session.forget` (`dashboard.go:2405-2416`, `2717`), so serve's forget handover fails today. Archive (`persistSnapshotCmd`, `dashboard.go:1395-1424`) deletes nothing.

### Decisions
- **D1 Mechanical floor.** The synchronous mechanical write stays on every trigger. The run store keeps the sha256 of the last mechanical render per session. When a new render matches it and the file on disk came from a child, leave the file alone, return `written:false`, start no child.
- **D2 Pointer hold.** `queueHandoverPointer` (`handover.go:141`) first calls `handovers.HoldPointer(sessionID, text)`. While a run for that session is starting/running, the text is stored on the run and not enqueued. When the run ends (done, failed, timed out) the held text is enqueued. Delivery gate unchanged. For restart/agent_switch a successful child enqueues `HandoverRefreshedNote` ("[aiman] Your handover in .aiman_handover.md was rewritten from the full transcript; re-read it."), skipped when a held pointer is being released.
- **D3 Scratch dir.** The child runs in `~/.aiman/handovers/<run-id>/` (git init'ed), created with `AdHoc: true`, `PreparedWorktree: <scratch>` (`flow_manager.go:396-397`, `:521`). The dir holds `brief.md`, `mechanical.md` (copy of the mechanical render), and outputs `handover.md`, `memories.json`, `done` (written last). The brief lists the transcript path (`Session.AgentSessionPath`, captured at start), the worker worktree path (read-only, may be gone), reason, the eight required headings, the JSON schema, and the rules: no code edits, commits, pushes, aiman to-dos, steering or child sessions. The prompt and all outputs use absolute paths (the eval FakeCreator ignores `PreparedWorktree`). The child is prompt-free (`--dangerously-skip-permissions`, `internal/infra/skills/engine.go:573`); the scratch cwd is the mitigation, the read-only rule is not enforceable.
- **D4 Jobs carry candidates.** While a child runs, `writeHandover` does not enqueue the memory job. The run enqueues it at the end: success gives candidates plus the child's handover as `Source`; failure gives no candidates plus mechanical `NoteBody`. The TUI shows candidates directly and calls Ollama only when a job has none and `cfg.AI.Enabled`.
- **D5 Agent choice.** `roles.handover` (agent/model/effort). Candidates: `roles.handover.agent`, then `agents.preferred`, then the worker's own agent. `RolesConfig.For` and `ResolveLaunchDefaults` (`internal/infra/config/roles.go`) generalise beyond reviewer. `LaunchCatalog` gains `Cheap` (claude `haiku`, codex `gpt-5.4-mini`, copilot `gpt-5-mini`, agy `gemini-3.7-flash-low`, others empty). Model precedence: `roles.handover.model`, then `Cheap`, then `agent_defaults`/repo overrides.
- **D6 Concurrency and hiding.** One run per session; a trigger while one runs only does the mechanical write and records the reason. `writeHandover` returns early when `sess.Role.Hidden()` (reviewer and handover roles). `SessionRole.Hidden()` replaces reviewer-only checks at `dashboard.go:725`, `restart_agent_switch.go:75`, `a1_prompt.go:403`.
- **D7 Ledger and Resume.** Events `handover.child_started` and `handover.child_finished` (actor aiman; payload run, agent, reason, status, candidates count, error). `Resume` mirrors `verify.Manager.Resume`: running → watch; starting → failed + fallback + cleanup; finished but not cleaned → cleanup.

### Data flow
Every trigger: mechanical write (sync), `Start` (fast; launch in background), return.

| Reason | Trigger | Pointer | On success |
|---|---|---|---|
| restart, agent_switch | TUI `PrepareRestartHandoff` | none queued | overwrite worker file, store context note, enqueue job with candidates, queue `HandoverRefreshedNote` |
| pre_compact | hook | `HandoverCompactionNote` held until the run ends | overwrite file, store note, enqueue job, release pointer |
| archive | TUI snapshot save | none | overwrite file, store note, enqueue job |
| forget | TUI teardown step 0 (new), then serve `handleForget` | none | worker dir usually gone: skip the file, still store context note `handover-<id>` and enqueue the job |
| revive | `handover.point` only | held only if a run is active | n/a |

### Failure modes (all keep the mechanical file)
No installed agent or `CreateChild` fails → `failed`. Exit without `done` → `incomplete`. Timeout (`handover.timeout`, default 10m) → `timed_out`, stop child. `done` present but `handover.md` fails `MissingSections` or exceeds 64 KiB → keep mechanical, still use valid candidates. Bad `memories.json` → no candidates. Worker row gone at finish → no file, no steer; context note and job still go out. Crash between releasing the pointer and saving the run may deliver the pointer twice (accepted).

### Config
```yaml
handover:
  child: true      # *bool, default on
  timeout: 10m
roles:
  handover: {agent: "", model: "", effort: ""}
```
Add to `Config`, `ServeConfig`, `MarshalServeConfig` (`internal/infra/config/config.go`).

## 2. Plan

Gate per task: `make ci` (T7 also `make eval`). Order: T1, T2, T5 in parallel; T3 needs T1+T2+T5; T4 needs T3; T6 needs T1 (`Hidden`) and T5; T7 needs T4; T8 last.

- **T1 Foundation.** `internal/domain/session.go`: `SessionRoleHandover`, `SessionRole.Hidden()`. `internal/domain/events.go`: the two event kinds. `internal/infra/config/config.go`: `HandoverConfig{Child *bool; Timeout time.Duration}` + `ChildEnabled()`, wired into Config/ServeConfig/MarshalServeConfig. `roles.go`: `RolesConfig.Handover`, `For("handover")`, `ResolveLaunchDefaults` for any role, `HandoverCandidates(workerAgent) []string`. `internal/infra/agent/launch_catalog.go`: `Cheap`. `internal/infra/skills/launch_flags.go`: handover role with no role model uses `Cheap`. Tests: YAML round-trip and default on; candidate order/dedupe; launch-flag table (claude gets `--model haiku`, role model beats Cheap, Cheap beats agent_defaults, no Cheap means no flag); `Hidden()`.
- **T2 Pure helpers.** `internal/domain/intelligence.go`: `FilterMemoryCandidates` moved from `internal/infra/ai/ollama.go:453-477` (Ollama calls it). New `internal/handover/child.go`: `ChildBrief(BriefInput) string`, `ParseChildHandover(raw) (string, error)` (MissingSections + 64 KiB), `ParseChildCandidates(raw) []domain.MemoryCandidate`. Tests: brief names every absolute path, all eight headings and the no-edit/commit/push rules; missing section rejected; candidates filtered and capped; bad JSON gives none.
- **T3 `internal/handoverchild` (new).** `run.go` (Run record, JSON file store like `verify.Store`), `manager.go`, tests + fakes. Deps reuse the `verify.Sessions`/`verify.Terminal` method sets so `serverSessions`/`serverTerminal` (`internal/server/handlers_verify.go`) satisfy them; plus Installed, Steer, Ledger, Context, Jobs, Now, Poll. API: `Start`, `Active`, `HoldPointer`, `LastMechanicalSHA`, `Resume`, `Close`. Launch shaped like `verify/manager.go:289-348`; watch like `check` (`:438-465`); finish per failure modes, then stop, wait, forget child, remove scratch dir. Tests: success, timeout, exit without marker, invalid handover with valid candidates, second Start absorbed, hold/release, worker gone at finish, Resume for three states, ledger start then finish.
- **T4 Serve wiring.** `internal/server/handover.go`: hidden-role early return, fingerprint skip, start child when enabled (else today's direct enqueue), `HoldPointer` in `queueHandoverPointer`, `HandoverRefreshedNote`, `"child": <run id>` in the `handover.write` response. `server.go`: `handovers` field + `SetHandovers`. `handlers_verify.go`: Forget event reason parameter (`child_cleanup`). `cmd/aiman/serve.go`: build manager with `HandoverCandidates`, `Resume`, defer `Close`. Tests: no job while child starts; pre_compact pointer held then released; hidden role writes nothing; fingerprint skip; `child: false` behaves as today.
- **T5 Memory job candidates.** `internal/memoryjob/queue.go`: `Job.Candidates` (`json:"candidates,omitempty"`), `candidates TEXT` column via tolerant `ALTER TABLE` (pattern `internal/infra/sqlite/repository_usage.go:76-89`), JSON in Enqueue/Pending. Tests: round-trip; old table migrates; empty omitted.
- **T6 TUI.** `internal/ui/memory_candidates.go`: jobs with candidates become proposals directly; else Ollama only when `cfg.AI.Enabled`; else mark done. `Hidden()` at the three call sites. `terminateSaveContext` (`dashboard.go:2433`): `WriteHandoverOnRemote(..., "forget")` first, best effort, 20s cap. Tests for each.
- **T7 Evals.** `evals/harness/handover_child.go` + `CaseHandoverChild` and `CaseHandoverChildFallback` in `cases_lifecycle.go` (pointer held, outputs applied, job has candidates, pointer released; timeout keeps mechanical).
- **T8 Docs.** AGENTS.md (handover paragraph, config keys, memory candidates, teardown-order gotcha), README.md (config reference near `roles.verifier`, Ollama now a fallback).
