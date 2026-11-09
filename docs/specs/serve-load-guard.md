# Serve load guard: stay answerable when the host is swamped

**Revision:** 3 (after consult rounds 1 and 2) · **Size:** project · **Status:** approved by the operator on 2026-11-09 ("Approve")
**Task:** operator report on 2026-11-09: "The serve got inundated with rpc requests and completely failed now - we need to guard against that"; "The rpc flood was on the host, and it seemed to lock up the db".

## Problem

Between about 16:40 and 17:14 on 2026-11-09 `aiman serve` on `regent0` stopped answering. The operator saw a pile of `aiman` client processes on the host and the database locked, and restarted the service at 17:14. What the host recorded, and what the code says, ranked by weight:

1. **Serve did not get CPU.** The 15-minute load average reached 120 on 32 cores: three Go CI runs (lint and race tests on every core) inside sessions, 25 agents and the macOS VM, all at the same priority as serve. Serve's 10-second GitHub lookups timed out at 16:48 and 17:08, and when it received SIGQUIT at 17:13 it needed 50 seconds to print six lines of its goroutine dump, which the journal then dropped. Everything below made a starved process do more work than it had to.
2. **Captures stacked.** `runStateSweep` ticks every 2 seconds with no overlap guard, and each `sweepStates` captures and classifies every active session; under load the sweeps pile up. `waitState` (`session.wait`, `session.prompt --wait`) classifies every 500 ms per waiting caller, and `applyMenuOverride` captures on working hooks. With 25 agents, several children waiting and a slow host, serve was capturing dozens of panes a second.
3. **The event stream read the table every 300 ms.** Each `session.events` subscriber (the laptop TUI, the phone) calls `repo.List` every 300 ms, and `loadSessions` runs once or twice per RPC on top.
4. **Writers under the worst journal mode.** The file runs in `journal_mode=delete` with `synchronous=FULL`: a write locks the whole file against readers and fsyncs per commit. The steady writers are the usage tracker every 15 s (a transaction), a `Save` on every hook report, notification `AppendIfDue` transactions, steer notes and the ledger. The pool is unbounded and every connection waits `busy_timeout` (5 s) on contention, so stalls turned into five-second holds in every goroutine. The name backfill inside `loadSessions` is a one-shot and not a load-path writer once rows have names.
5. **Nothing bounds or records requests.** Each connection gets a goroutine and is dispatched at once; there is no concurrency cap, no per-caller budget and no request accounting, so a looping caller is invisible and a pile-up has no ceiling. After the fact, the only diagnostics were the journal and the dropped SIGQUIT dump.

Together: a swamped host turned slow captures and slow writes into long locks, long locks into queued readers, queued readers into client processes piling up on the host, and the pile into more load. The guard has to cut every link, in that order.

## How it works today

```mermaid
flowchart LR
  classDef new fill:#dcfce7,stroke:#16a34a,color:#14532d
  classDef changed fill:#fef9c3,stroke:#ca8a04,color:#713f12
  classDef removed fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray:4 2
  clients[agents' aiman CLI, hooks, laptop SSH calls, phone gateway] -->|one goroutine per connection, no limit| disp[Server.dispatch]
  disp -->|every call| ls[loadSessions: SELECT all]
  events[session.events: repo.List every 300ms per subscriber] --> db
  ls --> db[(sqlite, journal_mode=delete, synchronous=FULL, pool unbounded, busy_timeout 5s; aiman-trigger opens the same file)]
  sweep[state sweep every 2s, no overlap guard: capture + classify every active session] --> db
  wait[waitState: classify every 500ms per waiter] --> cap[pty holders: capture]
  sweep --> cap
  usage[usage tracker 15s, hook Save, notify, steer, ledger] --> db
  holders[pty holders and agents, nice 0] -.->|same cgroup, same priority| cpu[host CPU]
  disp -.-> cpu
```

- `cmd/aiman/serve.go` opens one `sqlite.Repository` (`internal/infra/sqlite/repository.go`): `database/sql` with unlimited `MaxOpenConns`, `busy_timeout` in the DSN, no `journal_mode` or `synchronous` pragma. `Repository.DB()` is handed to `contextstore.OpenIndex` and `memoryjob.Open`, which write through it. `cmd/aiman-trigger` opens the same file with `NewRepository`: the host file has two writer processes. The laptop's TUI uses the package on its own file.
- `internal/server/server.go` accepts each Unix socket connection in its own goroutine and dispatches with no bound, no accounting and no request log. `callDeadline` bounds only the client side.
- `handlers_session.go` `loadSessions` reads the table on almost every handler, twice for session callers (`sessionScopeOf`, `resolveReq`); `handlers_events.go` reads it every 300 ms per stream.
- `handlers_notify.go` `runStateSweep` ticks every 2 s (`notifySweepEvery`); `sweepStates` has no overlap guard and no capture budget.
- `internal/ptyruntime` starts `aiman pty hold` at the caller's priority; the holder is also the capture and attach path, and its agent and everything the agent runs inherit the priority.
- Diagnostics: the journal, and SIGQUIT, whose dump the journal rate limiter drops when it is long.

## Glossary

- **Caller**: who a request is for accounting: the session bound to the connecting process (`sessionScopeOf`, the holder tree walk), else `operator` for a process outside any holder with no `caller`, else the named caller when it exists. `aiman-gateway` and `aiman-trigger` run outside holders and count as the operator.
- **In flight**: requests dispatched and not yet answered, minus idle waits (see Behaviour 3).
- **WAL**: sqlite's write-ahead log, where readers never block the writer and the writer never blocks readers.
- **Sweep**: one pass of `sweepStates`.

## Proposed design

Legend: **new**, *changed*, ~~removed~~.

### Domain model

- **new** `server.RPCStats{WindowSeconds int; Total, Inflight, Waiting, Rejected, Limited, SweepSkipped int; ByMethod, ByCaller []RPCCount}` with `RPCCount{Name string; Count int; AvgUS int64}`: the last 60 seconds, in `host.stats` under `rpc`.
- **new** error codes `server_busy` and `rate_limited`, both with `data: {retry_after_ms}`.
- **new** config `host.load` (`HostLoadConfig{MaxInflight int; PerCallerPerMinute int; AgentNice *int}`), defaults 64, 300 and 10, pushed by `MarshalServeConfig`.
- *changed* `sqlite.Repository`: WAL, `synchronous=NORMAL`, one writer pool and one bounded reader pool, `OnChange` hook.

### Structure

```mermaid
flowchart LR
  classDef new fill:#dcfce7,stroke:#16a34a,color:#14532d
  classDef changed fill:#fef9c3,stroke:#ca8a04,color:#713f12
  clients[clients] --> gate[admission: inflight semaphore, per-caller budget, accounting]:::new
  gate -->|server_busy / rate_limited| clients
  gate --> disp[dispatch]:::changed
  disp --> cache[session list cache: invalidated on identity changes only, 2s TTL]:::new
  events[session.events: cache, 1s or on hub event]:::changed --> cache
  cache --> db[(sqlite WAL, synchronous NORMAL, 1 writer, 8 readers, trigger as second process)]:::changed
  sweep[state sweep: TryLock, 2s or 5s, 16 per pass, stale hooks first]:::changed --> cache
  wait[waitState: slot released between ticks]:::changed --> gate
  stats[host.stats rpc, aiman host stats]:::new --> gate
  dump[SIGUSR1 and auto dump to ~/.aiman/logs]:::new --> disp
  holders[pty holder at nice 0, its agent child at nice 10]:::changed -.-> cpu[host CPU]
```

### Behaviour

0. **Stop the stacking first** (P0, smallest change, ships alone if needed). `sweepStates` takes a `TryLock`; while the previous sweep runs the tick is skipped and `SweepSkipped` counts it. `session.events` reads the session list no more often than once a second (and from the cache in Behaviour 2 once that lands, so a new row reaches the stream through the cache's invalidation, not by polling the table); a hub event is forwarded at once and does not itself trigger a table read.
1. **Database** (`internal/infra/sqlite/repository.go`, P1). `NewRepository` opens two `*sql.DB` on the same file. Both DSNs carry `_pragma=busy_timeout(5000)`, `_pragma=journal_mode(WAL)` (the mode persists in the file; saying it on every connection costs nothing and covers a file created by an older binary) and `_pragma=synchronous(NORMAL)` (per connection, so it has to be in the DSN, as the `busy_timeout` comment already explains). The writer DSN adds `_txlock=immediate` (valid for `modernc.org/sqlite`; it applies to `Begin`/`BeginTx`, which `ApplyUsage` and `AppendIfDue` use; autocommit `Save` is unaffected) and `SetMaxOpenConns(1)`. The reader has `SetMaxOpenConns(8)` and `SetConnMaxIdleTime(1m)`, so idle readers do not pin the WAL for ever. Every read-only method (`List`, `Get`, the `ListUndelivered*`, the usage and notify reads) queries the reader; every mutating method uses the writer. `DB()` returns the writer and **new** `Reader()` returns the reader; `contextstore.OpenIndex` and `memoryjob.Open` take both and run their `SELECT`s (`Search`, `Paths`, `Reindex`'s walk, `Pending`) on the reader, so a context search or a startup reindex never holds the single writer connection while a hook `Save` waits. `aiman-trigger` is a second process on the same file; WAL makes its writes and serve's coexist without the five-second holds, and nothing in this design assumes a single process. A `PRAGMA wal_checkpoint(PASSIVE)` runs on the writer every 5 minutes. `Close` closes the reader pool first, then runs `wal_checkpoint(TRUNCATE)` on the writer, then closes it; when `aiman-trigger` still holds the file the checkpoint answers busy, which is logged and does not fail the close. A copy of `aiman.db` taken after a clean stop is consistent without its `-wal` and `-shm` sidecars (an rsync or backup while serve runs must take all three files; the README says so). `aiman downgrade` swaps the binary only and opens a WAL file fine. The laptop's own database gets the same settings on its own file.
2. **Session list cache** (`handlers_session.go`, P1). `loadSessions` reads the stored list from a cache keyed by nothing (one host, one table): a read-through copy with a 2-second TTL, invalidated through `Repository.OnChange(ChangeIdentity)` on every `INSERT` and `DELETE` (so `session_created`, which the event stream polls from this list, is never late) and on an `UPDATE` whose **old and new values** differ in an identity or lifecycle column: `name`, `group_name`, `parent_id`, `role`, `status`, `backend`, `branch`, `working_directory`, `review_pr`, `fix_pr`, plus `ClearNativeAgentIdentity`. `Save` updates every column with `COALESCE`, so the repository decides by reading the row's identity columns by primary key before the write and comparing, never by which columns the statement names. A hook report's `Save` changes `hook_state`, `hook_state_at`, `agent_title`, `agent_ended`: those are `ChangeOther` and do not invalidate. Every handler that needs a fresh hook field reads the row by id: `classify` and `applyAgentReport` already do (`repo.Get`); *changed* `steerSession` (the delivery gate's lookup) also reads by id (`liveSession`/`repo.Get`) instead of `loadSessions`, because a two-second-old idle row must never let a note into a permission menu. `session.list` and `session.get` keep `decorateState`'s per-row `Get`; the event stream's activity state comes from `sessionStates`; the usage tracker and ciwatch call `repo.List` and are not on this cache. The name and group backfill moves out of `loadSessions` into a startup pass and the two write paths that can create a nameless row (`session.register`, `handleReportAgentSession`). The live merge (`mergeLiveSessions`) stays per call; the pty list is in memory. `session.events` uses the same cache. Net effect: a hook storm costs one `Save` each and zero list reads.
3. **Admission** (`internal/server/admission.go`, P2). Every request passes `admit` before `dispatch`:
   - **Accounting**: a 60-second ring of per-method and per-caller counts and durations in microseconds; `Inflight` and `Waiting` counters. `host.stats` returns it under `rpc`; `aiman host stats` prints a `rpc` block (`total/min`, `inflight`, `waiting`, `rejected`, `limited`, `sweep_skipped`, top 10 methods, top 10 callers by count).
   - **Concurrency cap**: a semaphore of `host.load.max_inflight` (default 64). A request waits for a slot at most 50 ms, then answers `server_busy` with `retry_after_ms: 1000`: failing fast is what keeps client processes from piling up on the host, which was the operator's symptom, and the CLI retries once. Long-lived calls hold a slot only while they work, not while they wait: `pty.attach` and `session.events` release it after setup; `waitState` (`session.wait`, `session.prompt --wait`, the peer-note wait) releases the slot between classify ticks and takes it back for each classify, counted in `Waiting` meanwhile, so 25 waiting agents cost 25 slots for a few milliseconds a second, not continuously. `session.create`, `design.publish`, `review.run`, `review.coverage`, `session.restart` and `session.terminate` do real work for their whole duration and keep their slot.
   - **Per-caller budget**: a token bucket per caller, `host.load.per_caller_per_minute` (default 300) with a burst of 60; over budget is `rate_limited` with `retry_after_ms`. The bucket key is the pty holder the connecting process descends from, looked up in the holder map serve already keeps in memory (the pty list's PIDs and `inHolderTree`'s walk), never `req.Caller` and never a `loadSessions`: a peer inside a holder shares that session's bucket whatever `AIMAN_ID` says, including when it is unset. Only a process outside every holder is the operator and exempt, which covers the laptop TUI, `aiman-gateway` and `aiman-trigger`; a process outside any holder that names a `caller` is accounted under that caller but not limited (it is the laptop or a tool acting for the operator). `session.report_agent_session` is exempt from the bucket (still counted): hooks are how steering and notifications stay live, and a tool loop must not silence them. `todo.mirror` is exempt for the same reason. A `pty.wait-output` loop at one poll a second fits (60/min); a coordinator polling four children at once fits; a session calling `session.read` in a tight loop is the case this stops.
   - **Log**: one line per caller per minute when it is limited or rejected (`rpc: caller <id> limited: 300/min, method session.read`), and one line when `Inflight` first exceeds half the cap (`rpc: 33 in flight, host load 87.2`). Never the request body.
   - **Capture budget**: a global semaphore of 4 around every pane capture serve makes for itself (`captureLive` in `sampleState`, `applyMenuOverride`, the sweep), so dozens of waiters and hook reports cannot turn into dozens of concurrent captures. *changed* `waitState` no longer captures: it reads the session's last state from `sessionStates` (the sweep's and the report path's cache) every 500 ms and only falls back to one capture of its own when no state has been sampled yet; a hook report updates `sessionStates` through `observeState` and `Kick`s, so a waiter sees it within a tick.
   - The CLI client (`client.go`) retries `server_busy` and `rate_limited` once after `retry_after_ms`, then returns the error with its message.
4. **Agents yield to serve** (`internal/ptyhold`, P3). The holder stays at nice 0: it is the capture and attach path the TUI and the phone depend on, so it must stay as responsive as serve. The holder starts the agent through `bash -l -c <childShell>`, which execs the agent before any later line of the holder runs, so renicing the shell's pid after `StartWithSize` would miss the agent and its whole tree. Instead `childShell` wraps the command in `nice -n <n>` (with `n` from `host.load.agent_nice`, default 10, 0 omits the wrapper), so the agent is born at nice 10 and everything it spawns inherits it; the holder's own copier and the pty stay at 0. Documented consequence: `git`, `ssh`, test binaries and anything else started inside a session run at nice 10 and can miss short timeouts under load; a user-level mutagen daemon runs outside sessions and is unaffected. Existing agents keep their priority until their session restarts; nothing renices running processes. The nice value reaches the holder through its request file (`ptyruntime` passes it from serve's config).
5. **Load-aware sweep** (`handlers_notify.go`, P4). On top of Behaviour 0: the ticker runs every 2 seconds while the 1-minute load average is below `runtime.NumCPU()` and every 5 seconds above it (never longer); the load average comes from `/proc/loadavg` each tick, and elsewhere than Linux the period stays 2 seconds. Each sweep captures at most 16 sessions, chosen in this order: sessions that just transitioned (a hook reported `ended` or `waiting_input` since the last sweep) always, then sessions whose hook report is stale or missing, then sessions with a fresh `working` report (they still need `applyMenuOverride`'s capture, because a permission menu shows only on screen), round robin within each group; only a fresh `idle` or `ended` report needs no capture. Every capture counts toward the 16 and passes the capture semaphore. The ceiling for a hook-less session at the back of the queue is `ceil(n/16)` passes times the period: 15 seconds for 40 sessions at 5 seconds. `session.wait` and `steerReady` classify on their own and are not delayed by the sweep.
6. **Diagnostics** (`cmd/aiman/serve.go`, `internal/server/dump.go`, P5). SIGUSR1 writes `~/.aiman/logs/serve-dump-<unix>.txt` from a dedicated goroutine: `RPCStats`, the load average, the in-flight list (method, caller, age), then `runtime.Stack` into a buffer capped at 64 MiB (a truncated dump says so on its last line). One dump at a time; a signal during a dump is ignored. The same file is written automatically, at most once per 10 minutes, when `Inflight` has sat at the cap for 30 seconds. `aiman host dump` sends SIGUSR1 to `systemctl --user show -p MainPID --value aiman-serve`; when that is empty or 0 (serve not running as a user unit) it reads `~/.aiman/serve.pid`, and with neither it says so and exits 1.

### CLI

- `aiman host stats` gains the `rpc` block (and `--json` the `rpc` object).
- `aiman host dump` writes the dump file and prints its path.
- Config: `host.load.max_inflight`, `host.load.per_caller_per_minute`, `host.load.agent_nice`.

## Scenario

Three children run `make ci` at once; load climbs past 100. Their shells run at nice 10, so serve, the holders, sshd and the laptop's `aiman pty capture` calls keep getting CPU. The sweep skips the ticks it cannot keep up with, drops to every 5 seconds, and captures the 16 sessions whose hooks are stale, so the two Codex sessions without hooks still show their state within three passes. Twenty agents sit in `session.wait`; they hold a slot only during each half-second classify, so `inflight` reads 6. A reviewer child polls `aiman session read` in a tight loop: after 300 calls in a minute it gets `rate_limited`, the CLI retries once after a second, then prints the error; serve logs the caller once. Hook reports from 25 agents commit in WAL without blocking the TUI's reads, and none of them invalidates the session list cache. The operator runs `aiman host stats` and sees `inflight 6`, `waiting 20`, `limited 1 (sess-c, session.read)`, `sweep_skipped 14`. Nothing piles up and nothing locks. Had it still locked up, `aiman host dump` would have written what serve was doing to a file the journal cannot drop.

## Alternatives

- **Move off sqlite**: lost; the contention is the journal mode, the unbounded pool and the read patterns, all fixable in place.
- **Shed load by refusing all session callers when busy**: lost; a budget per caller punishes the one looping agent, not every agent.
- **Renice serve upward**: lost; a negative nice needs root or `CAP_SYS_NICE`, and the unit runs as the user.
- **Nice the whole holder** (rev 1): lost in round 1; the holder is the capture and attach path, so a slow holder is exactly what the operator feels. The agent's shell child is the right process.
- **cgroup CPU weights per holder**: better isolation than nice; lost for the first cut because holders are started by serve, not systemd, and stay in serve's cgroup. A follow-up if nice proves too weak.
- **Invalidate the cache on every `Save`** (rev 1): lost in round 1; hook reports are the hottest write on a busy host and would make the cache miss constantly.
- **Request log to disk**: lost; the 60-second window answers "who is hammering now" and the dump answers "what is stuck".

## Open questions

1. Should `rate_limited` also apply to the operator's own processes (the laptop TUI's SSH calls)? Default: no; the TUI already serialises its captures (`previewBusy`).
2. Default `agent_nice` 10 or 5? Default: 10; agents are interactive but their builds are not, and the TUI's responsiveness is what the operator feels.

## Decisions

Consult round 1 (Grok, report `~/.aiman/consults/a67838f4-8fb1-40d1-a3af-fb90cce9f108.report.md`): verdict revise, 4 high, 2 medium, 2 low. All accepted in rev 2.

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| The diagnosis named the journal mode as the outage; the continuous load was CPU starvation, stacked sweeps and wait/classify captures, `session.events` listing every 300 ms, and hook, usage and notify writers; the write-on-read is a one-shot | high | accept | Problem rewritten as a ranked list; Behaviour 0 ships the sweep guard and the event-stream read first |
| A cache invalidated on every `Save` misses on every hook report; `session.events` bypasses `loadSessions` | high | accept | Behaviour 2: invalidate on identity and lifecycle columns only, hook fields read by id; the event stream uses the cache |
| Nicing the holder slows the capture and attach path the operator needs | high | accept | Behaviour 4: holder stays at nice 0, the agent's shell child is reniced; inherited effects documented |
| Two pools underspecified: `synchronous` per connection, `DB()` handed to contextstore and memoryjob, `aiman-trigger` is a second writer process, `TRUNCATE` checkpoints never finish under idle readers, sidecar files on copy | high | accept | Behaviour 1: pragmas in both DSNs, `_txlock=immediate` on the writer only, `DB()` is the writer, reader idle time, `PASSIVE` on a timer and `TRUNCATE` on `Close`, trigger named, laptop claim removed |
| Waits hold a slot for minutes; 300/min breaks a coordinator's hooks in a tool loop; caller identity must follow the holder binding; trigger is exempt by the wording | medium | accept | Behaviour 3: waits release the slot between classify ticks (`Waiting`), hooks and `todo.mirror` exempt from the bucket, identity through `sessionScopeOf`, operator exemption spelled out |
| A 10-second sweep with 16 per pass delays TUI state and notifications by 20 to 30 seconds | medium | accept | Behaviour 5: 2 s or 5 s only, transitions first, stale hooks next, fresh hooks need no capture |
| The `RPCStats` struct tags do not compile; integer milliseconds hide cheap calls | low | accept | Contracts: one field per tag, `AvgUS` in microseconds |
| A full goroutine dump can be tens of MB and stall serve; `aiman host dump` needs a non-unit path | low | accept | Behaviour 6: dedicated goroutine, 64 MiB cap, one at a time, `serve.pid` fallback |

Consult round 2 (Grok, report `~/.aiman/consults/54610ced-697e-4c17-a41e-0c3d6efd1f85.report.md`, against rev 2): verdict revise, 3 high, 3 medium, 1 low. All accepted in rev 3.

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| `steerReady` reads hook fields through `loadSessions`, so a 2-second-old idle row could type a note into a menu; inserts and deletes must invalidate; `review_pr`/`fix_pr` are identity; `Save` writes every column so "which column the statement names" cannot drive invalidation | high | accept | Behaviour 2: `steerSession` reads by id; invalidate on every insert and delete and on old-vs-new identity differences read before the write |
| Renicing the shell's pid after start misses the agent, which has already been exec'd | high | accept | Behaviour 4: `nice -n` inside `childShell`, so the agent is born niced |
| The bucket keyed on `sessionScopeOf` trusts `req.Caller`, leaves an in-holder empty caller as operator, and costs a list read plus a /proc walk per request | high | accept | Behaviour 3: key is the holder the peer pid descends from, from the in-memory holder map; operator only outside every holder |
| `DB()` as the sole writer hands contextstore and memoryjob reads that pin the one writer connection; `Close` cannot truncate while readers hold the WAL | medium | accept | Behaviour 1: `Reader()` for their selects; close readers, checkpoint, close writer; busy logged |
| A fresh `working` report still needs a capture for menus; the 16-per-pass budget and the "10 seconds" claim were wrong | medium | accept | Behaviour 5: working sessions captured and counted, ceiling stated as `ceil(n/16)` passes |
| Releasing the wait slot does not cut the capture storm: 25 waiters still classify every 500 ms and every hook report captures | medium | accept | Behaviour 3: global capture semaphore of 4; `waitState` reads `sessionStates` instead of capturing |
| A 2-second admission wait keeps client processes piled up | low | accept | Behaviour 3: 50 ms, then `server_busy`; the CLI retries once |

P1 consult (report `~/.aiman/consults/08f9af65-877d-44e1-8640-231a5353d94c.report.md`): verdict revise, 2 high, 2 medium, 2 low. Behaviour 2 assumed only `steerSession` reads a hook field through `loadSessions`; `liveReview` (review create) and `checkFixRounds` (`review.fix`) also branch on `AgentEnded`.

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| Do not add `agent_ended` to the identity columns: every SessionEnd and `ClearAgentEnded` would miss the cache, and title, path and hook state would stay stale | high | accept | `OnChange` kinds unchanged |
| Do not `repo.Get` on every `resolveReq`-style lookup: it misses the list scans and taxes identity-only calls | high | accept | Only the gates re-read, through `freshRows` |
| The gates are `liveReview` and `checkFixRounds`; terminate's `children_left` is warning copy | medium | accept | `freshRows` narrows by identity (`review_pr`; fixer role and `parent_id`) and re-reads those rows' hook fields by id; `children_left` stays on the cache |
| `ClearAgentEnded` is `ChangeOther`, so the gates must re-read even when the cached row says ended | medium | accept | `freshRows` re-reads every matching row, ended or not |
| A one-row re-read on the peer target and the handover lookup is fine; not every resolve | low | accept in part | `admitPeer` re-reads the target's hook fields (a restart's false refusal); the handover render keeps the cache, its title and state are copy |
| Keep `TestCreateReviewExists` as the test of the gate re-read | low | accept | Unchanged; it passes through `freshRows` |

## Breakdown

| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |
|---|---|---|---|---|---|---|
| P0 stop the stacking | `internal/server/handlers_notify.go` (`TryLock`, `SweepSkipped`), `internal/server/handlers_events.go` (1 s or on hub event) | 0 | none | S | worker | `internal/server/sweep_overlap_acceptance_test.go`; `go test ./internal/server/` |
| P1 database and cache | `internal/infra/sqlite/repository.go` (pools, pragmas, checkpoint, `OnChange`, `Reader`, read/write split), `internal/contextstore`, `internal/memoryjob` (take the reader), `internal/server/handlers_session.go` (`loadSessions` cache, backfill move, `steerSession` by id), `handlers_events.go` (cache), `cmd/aiman/serve.go` (startup backfill), `README.md` (WAL sidecars) | 1 | P0 | M | smart | `internal/infra/sqlite/wal_acceptance_test.go`, `internal/server/session_cache_acceptance_test.go`; `go test ./internal/infra/sqlite/ ./internal/server/` |
| P2 admission and stats | `internal/server/admission.go` (new), `server.go`, `protocol.go`, `handlers_host.go`, `handlers_session.go` (`waitState` slot release and `sessionStates` read, capture semaphore), `client.go`, `cmd/aiman/host_cmd.go`, `internal/infra/config/config.go` (`HostLoadConfig`), `api/openapi.yaml`, `AGENTS.md` | 1 | none | M | smart | `internal/server/admission_acceptance_test.go`, `cmd/aiman/host_stats_rpc_acceptance_test.go`; `go test ./internal/server/ ./cmd/aiman/` |
| P3 agent priority | `internal/ptyhold/holder.go` (`childShell` nice wrapper), `internal/ptyruntime` (pass the value), config push | 1 | none | S | worker | `internal/ptyhold/nice_acceptance_test.go`; `go test ./internal/ptyhold/ ./internal/ptyruntime/` |
| P4 load-aware sweep | `internal/server/handlers_notify.go`, `internal/server/loadavg.go` (new) | 2 | P0, P1 | S | worker | `internal/server/sweep_load_acceptance_test.go`; `go test ./internal/server/` |
| P5 diagnostics | `internal/server/dump.go` (new), `cmd/aiman/serve.go` (SIGUSR1), `cmd/aiman/host_cmd.go` (`host dump`) | 2 | P2 | S | worker | `internal/server/dump_acceptance_test.go`; `go test ./internal/server/ ./cmd/aiman/` |

## Contracts

```go
// internal/server
type RPCCount struct {
    Name  string `json:"name"`
    Count int    `json:"count"`
    AvgUS int64  `json:"avg_us"`
}
type RPCStats struct {
    WindowSeconds int        `json:"window_seconds"` // 60
    Total         int        `json:"total"`
    Inflight      int        `json:"inflight"`
    Waiting       int        `json:"waiting"`
    Rejected      int        `json:"rejected"`
    Limited       int        `json:"limited"`
    SweepSkipped  int        `json:"sweep_skipped"`
    ByMethod      []RPCCount `json:"by_method"`
    ByCaller      []RPCCount `json:"by_caller"`
}
const (
    CodeServerBusy  = "server_busy"
    CodeRateLimited = "rate_limited"
)
func (s *Server) RPCStats() RPCStats
func (s *Server) SetHostLoad(c config.HostLoadConfig) // zero fields keep defaults (64, 300, 4 captures); serve calls it from the pushed host.load
// Admission runs inside dispatch, after the request is decoded, so a direct dispatch call is counted, capped and limited.
// The bucket and by_caller key come from the holder the peer pid descends from (sessionOfPID, else the holder map), never from req.Caller;
// a process outside every holder with no caller is keyed "operator" (never limited); one outside every holder naming a caller is counted under it and not limited.
// The capture budget (default 4) wraps every CaptureScreen made through captureLive. The CLI retries server_busy and rate_limited exactly once;
// a second rate_limited fails with an error whose text contains "rate limited". `aiman host stats --json` prints the rpc object.
func (s *Server) SweepSkipped() int // P0: sweeps skipped because the previous one was still running; RPCStats.SweepSkipped reads it
// P0: EventPollInterval becomes 1s. P1: var sessionListTTL = 2 * time.Second (the cache TTL; tests shorten it); serve subscribes to OnChange in New through an interface assertion on its repository, never a concrete type.

// internal/ptyhold (P3)
func childShell(command string, nice int) string // nice clamped to [0,19]; 0 emits no wrapper; the command runs as `exec nice -n <n> bash -c <quoted command>` after the TERM/PATH exports
// ptyhold.Spec gains AgentNice int `json:"agent_nice,omitempty"`; Run passes it to childShell; a request without it runs at the holder's niceness
// ptyruntime.Spec gains AgentNice int, copied by Manager.Create into the holder's Spec; serve sets it from host.load.agent_nice (nil: 10)

// internal/ptyruntime/ptyfake (test infrastructure the acceptance tests use)
func (f *FakePTY) SetCaptureDelay(d time.Duration)
func (f *FakePTY) Captures() int
func (f *FakePTY) MaxConcurrentCaptures() int

// internal/infra/config
type HostLoadConfig struct {
    MaxInflight        int  `yaml:"max_inflight,omitempty"`          // 0: 64
    PerCallerPerMinute int  `yaml:"per_caller_per_minute,omitempty"` // 0: 300
    AgentNice          *int `yaml:"agent_nice,omitempty"`            // nil: 10
}
// HostConfig gains Load HostLoadConfig `yaml:"load,omitempty"`.

// internal/infra/sqlite
func NewRepository(dbPath string) (*Repository, error) // WAL, synchronous NORMAL, 1 writer (_txlock=immediate), 8 readers
type ChangeKind int // ChangeIdentity (insert, delete, or an identity column changed) or ChangeOther
func (r *Repository) OnChange(fn func(ChangeKind))     // after every mutating method commits
func (r *Repository) DB() *sql.DB                      // the writer
func (r *Repository) Reader() *sql.DB                  // the bounded reader pool
func (r *Repository) Close() error                     // wal_checkpoint(TRUNCATE), then both pools
```

## What the reader should be able to explain

1. The five links of the failure chain, in order, and which piece cuts each.
2. Why hook reports must not invalidate the session list cache.
3. Why the agent is born at nice 10 inside its shell, and its holder is not.
4. What the admission layer refuses, to whom, why waits and hooks are treated specially, and why the operator's own calls are exempt.
5. How to read `aiman host stats` and where the dump lands next time.
