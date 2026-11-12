# Session resource limits: spec and implementation plan

## Problem

On regent0 every agent session, `aiman serve` and the phone gateway run as user `code`. They share one cgroup budget, set in `/etc/systemd/system/user-1004.slice.d/10-memory.conf` as `MemoryHigh=24G` and `MemoryMax=32G`. `/tmp` is tmpfs, and files in it are charged to the cgroup that wrote them.

Between 2026-11-09 and 11-12 a single session pushed the whole slice over `MemoryHigh` at least six times:

| Session work | Footprint |
|---|---|
| YOLO DDP training, 24 dataloader workers | ~15G anon + shm |
| 12G venv plus models in a Claude scratchpad under `/tmp` | 12G shmem, held permanently |
| `jest` with default workers | 32 workers, ~18G |
| `bun test evmIdlePages.solo.tsx` | a leaking test, 4–5G per run |
| three sessions running `go test -race ./...` at once | 94 linkers |
| `vitest run` with default workers | 31 workers |

Each time, the kernel throttled every process in the slice: memory stalls of 90%+, load 150–560, and 92% of CPU spent in the kernel. `aiman serve` stopped answering, so the TUI and the phone could not connect. Messages sent to the agents did not stop them reliably; several relaunched the job within seconds.

## Goals

1. A runaway session throttles only itself, never other sessions, serve or the gateway.
2. Agent scratch files do not consume RAM.
3. The operator can see each session's resource use and is warned before the host suffers.
4. Agents know the host rules and can check their own usage.

## Non-goals

- Changing system-wide config that needs root, such as the slice limits or the `tmp.mount` mask. The operator does that separately.
- Limiting GPU use.
- macOS and other hosts without systemd. There, everything below must degrade to a no-op without errors.

## Design

### 1. One cgroup per session

When serve spawns a PTY holder (`internal/ptyhold/client.go: Spawn`), it starts it in a transient systemd user scope:

```
systemd-run --user --scope --unit=aiman-session-<short-id> --slice=aiman-sessions.slice \
  -p MemoryHigh=<high> -p MemoryMax=<max> -p MemorySwapMax=<swap> \
  -p CPUWeight=<w> -p IOWeight=<w> -p TasksMax=<n> \
  -- <holderCmd> --root ... --id ...
```

- The holder and everything the agent runs inherit the scope. A per-session `MemoryHigh` throttles only that session.
- `--scope` keeps the current fork/detach semantics: the holder is still the caller's child until it detaches. Verify that `detachAttr()`/`Setsid` behaves the same inside a scope. If `--scope` blocks `Spawn`, fall back to `systemd-run --user --unit ... --property=...` as a transient service, with `KillMode=process` and `Type=exec`.
- Holders then no longer live in `aiman-serve.service`'s cgroup. That strengthens the "holders outlive serve" contract documented in `internal/infra/remotesvc/remotesvc.go` (`killMode`). Keep `KillMode=process` on serve for holders started before this change.
- Detection: use systemd only if `systemd-run` is on PATH, `systemctl --user` reaches the bus (set `XDG_RUNTIME_DIR` when it's missing), and the host is Linux. Otherwise spawn exactly as today and log once.
- A parent slice, `aiman-sessions.slice`, holds every session scope. Give it an aggregate `MemoryHigh` (by default, slice budget minus headroom for serve) so the sum of sessions can't starve serve either.

### 2. Protect serve and the gateway

In `remotesvc` unit generation, give `aiman-serve` and `aiman-gateway` these settings: `MemoryLow=512M` (serve) and `256M` (gateway), `CPUWeight=200`, `IOWeight=200`. They are cheap to set and keep the control plane responsive under pressure. The existing install and update path rewrites the unit files.

### 3. Scratch and tmp on disk

For each session, serve sets `TMPDIR=~/.aiman/tmp/<session-id>` (create it with mode 0700) in the PTY's environment.

- First verify that Claude Code derives its scratchpad from `TMPDIR` (`os.tmpdir()`). If it doesn't, find the variable that controls it, such as `CLAUDE_CODE_TMPDIR`, and set that too. Check the other agents (codex, agy, pi, opencode) and set whatever each one honours.
- Delete the directory when the session is terminated or forgotten, not on restart.
- Since `/tmp` will move to disk once `tmp.mount` is masked, this matters most for hosts that keep tmpfs. Still do it everywhere, because it also keeps per-session scratch together.

### 4. Defaults and config

Add to `~/.aiman/config.yaml`:

```yaml
resources:
  enabled: true                # false = spawn as today
  session:
    memory_high: 6G
    memory_max: 10G
    memory_swap_max: 1G
    cpu_weight: 100
    io_weight: 100
    tasks_max: 4096
  sessions_slice:
    memory_high: 20G           # aggregate across all sessions
  tmpdir: true
```

Allow per-session overrides at create time (`session create --memory-high 12G`) for known heavy work. The override is stored on the session and reapplied on restart. The override does not raise the aggregate limit.

`systemctl --user set-property aiman-session-<id>.scope MemoryHigh=...` applies live, so add `aiman session limits <target> --memory-high X` for adjustments at runtime.

### 5. Monitoring

Serve samples each session scope every 10s:
- from `/sys/fs/cgroup/.../aiman-session-<id>.scope/`: `memory.current`, `memory.pressure` (`some avg10`), `memory.stat` (anon, shmem), `cpu.stat` (`usage_usec` delta) and `pids.current`
- the host's `/proc/pressure/{memory,cpu,io}` and the user slice's `memory.current` and `memory.high`

Expose it:
- An RPC `resources.get`, returning per-session usage and host and slice totals, plus a CLI `aiman resources [--session X] [--json]`.
- The TUI dashboard: a mem/CPU column per session, and a header gauge for the slice versus `MemoryHigh`.
- The phone: the same fields over the existing gateway RPCs. A separate session in the aiman-phone repo can pick up the UI work.
- Alerts: when a session holds `memory.current >= 90%` of its `memory_high` for 30s, or its pressure `avg10 > 50`, raise a notification to the operator (existing notify path) and inject a one-line note into the session's pane: "aiman: this session is at X/Y memory and is being throttled; reduce parallelism." Rate-limit to one note per session per 10 minutes. Raise a separate host-level alert when the slice reaches 90% of `MemoryHigh`.

### 6. Agent guidance (skill)

Add a "Host resources" section to the aiman skill (`skills/aiman/SKILL.md` and the embedded copy at `internal/aimanskill/SKILL.md`; keep both in sync):

- Each session has a memory budget (show the value). Exceeding it slows only your session.
- Parallelism defaults: `jest`/`vitest --maxWorkers=2`, `go test -p 2` (never `-race ./...` across the whole module, and never alongside another heavy job), PyTorch/Ultralytics `workers<=2`, one heavy job at a time.
- Put venvs, clones and datasets on disk under the session's `TMPDIR` or worktree, never under `/tmp` directly.
- Ask the operator before GPU, training or multi-GB jobs.
- Check your usage with `aiman resources --session $AIMAN_ID`. If it's near the limit, stop and reduce parallelism rather than retrying.
- Don't relaunch a job that was killed by the operator or OOM-killed without asking.

## Implementation plan

Use TDD wherever there's pure logic. Run `make ci` before each commit. Land each step as its own commit on main, per the repo's push-to-main rule.

1. **Config.** Add the `resources` block to the config types, with defaults and parsing (e.g. `6G`). Tests: defaults, overrides and invalid sizes.
2. **Systemd capability probe.** Write `internal/infra/cgroup` (or similar) with `Available() bool`, `ScopeName(id)`, and a builder that returns the `systemd-run` argv from limits. Unit-test the argv builder; make the probe injectable.
3. **Spawn in a scope.** In `ptyhold.Spawn`, wrap `holderCmd` with the builder's argv when available. Integration test, skipped without a user systemd: spawn a holder, assert that `/proc/<pid>/cgroup` contains `aiman-session-<id>.scope` and that `memory.high` matches. Confirm that a serve restart leaves holders alive. Mind the `testing.Testing()` guard in `ptyruntime`.
4. **Sessions slice.** Create or update the `aiman-sessions.slice` drop-in, with its aggregate limit, at serve startup.
5. **Serve and gateway unit hardening.** Add the settings in `remotesvc` unit generation and update the tests for the unit text.
6. **TMPDIR per session.** Set the PTY env at create and restart, and add cleanup on terminate and forget. Verify by hand how Claude Code chooses its scratchpad and record the finding in a code comment.
7. **Sampler.** Add a goroutine in serve that reads the cgroup files: parse `memory.pressure` and `cpu.stat`, and compute the CPU % delta. Unit-test the parsers on fixture files.
8. **RPC and CLI.** Add `resources.get` and `aiman resources`. Add `session create --memory-high/--memory-max` and `aiman session limits`.
9. **Alerts.** Add threshold and hysteresis logic (unit-tested), the operator notification, and pane injection, rate-limited.
10. **TUI.** Add the per-session column and the header gauge.
11. **Skill.** Write the host-resources section in both SKILL.md copies.
12. **Docs.** Update AGENTS.md (a gotcha entry and a recent-history row) and the README config section.
13. **Rollout.** Release with a tag once main is green. Existing sessions stay unscoped until they restart; note that in the release notes. Optionally add `aiman session adopt-scope <id>`, which moves a running holder's PIDs into a new scope via `systemd-run --scope --pid` or `busctl`, if feasible.

## Risks and open questions

- **Does `systemd-run --scope` from a process inside `aiman-serve.service` work on the user manager?** It should, since the user manager has delegation, but verify this early: it's step 3's first test.
- **Shmem charging.** tmpfs pages stay charged to the scope that wrote them even after the session ends, until they are deleted. TMPDIR cleanup on terminate covers this.
- **The global CLAUDE.md** (`~/.claude/CLAUDE.md`) is per-user and outside this repo. The skill is the in-repo channel; suggest the operator add a one-line pointer there.
- **macOS laptop TUI**: the sampler and RPC must return "unsupported" cleanly, and the UI hides the column.
