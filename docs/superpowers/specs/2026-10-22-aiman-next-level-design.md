# Aiman Next Level: Design

Date: 2026-10-22
Status: revised after review (builder check, Grok 4.7, Muse spark-1.3)
Plans: `docs/superpowers/plans/2026-10-22-nl-*.md` (one per workstream; index and cross-plan contracts in `2026-10-22-nl-0-roadmap.md`)

## Intent

The user runs several agent sessions at once across remotes and vendors. Today the only way to know what a session is doing, or to change its course, is to attach, read the pane, and type. Nothing shows a session's plan. In the user's words: "having to go in to multiple sessions and see what was happening, or to steer and re prompt, and rarely do sessions have a visible to do list, which I think must be standard and editable."

The next level makes aiman the place where you watch and steer every session without entering any of them. On that foundation it adds the research-backed improvements: handovers and better memory, cost visibility, notifications, CI and review reaction, cross-vendor verification, cheaper starts, and spec-driven fan-out.

Success: on an ordinary day the user attaches to a session only when they choose to, not because it is the only way to find out what is going on.

## Goals

1. Every session has a visible, editable to-do list, shared by the user and the agent.
2. The user steers any session from the dashboard (edit its to-dos or type a prompt) and the agent receives it as soon as it is safe to type into it.
3. aiman tells the user when a session needs them.
4. Token spend and cache efficiency are visible per session and per JIRA ticket.
5. Agents react to CI failures and review comments without the user relaying them.
6. A different-vendor reviewer checks a session's work before a PR goes out.
7. Restarts and agent switches carry a real handover; shared memory gets typed notes, ranked search and expiry.
8. Sessions start cheaper and faster (worktree bootstrap, RTK, graphify, cache hygiene).
9. A ticket can be split into a spec plus to-dos and fanned out to worker sessions.

## Non-goals

- A board/kanban view (layout C rejected) or an inbox-sorted list (layout B rejected).
- ACP/A2A, OpenTelemetry receivers, Langfuse export, LLM gateways, model routing, sandboxing, local mode.
- Nested to-dos, estimates, due dates.

## Anti-goals

- Steering that loses edits: a user change must reach the agent and must survive the agent's next write of its own list.
- Feedback loops: an agent's own write never produces a steering note to itself.
- Typing into an agent that is mid-turn, showing a permission/choice dialog, or has exited.
- Pane scraping to invent to-do lists.
- Rewriting files the agent may have in its cached prompt prefix when their content has not changed.
- A busier dashboard. See the noise budget.

## Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only.
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- The laptop reaches remotes only over SSH. `aiman serve` (`internal/server`, newline JSON over `~/.aiman/aiman.sock`) is the remote API. Laptop SSH calls have a 30 s timeout, so no socket method may block longer than that; long waits are client-side polling.
- Every laptop call to a new socket method goes through one entry point: `aiman rpc METHOD --params-stdin` (an allowlist owned by nl-1, extended by each plan), invoked by `usecase.ServeRPC`. No per-feature CLI subcommands for the laptop.
- Both terminal backends (tmux, pty) must work everywhere.
- SQLite is `modernc.org/sqlite` (FTS5 available). `aiman serve` and `aiman-trigger` both open the remote DB; serve is the only writer of the new state introduced here (to-dos, steering queue, usage, CI state, notification log, context index). Other processes write through the socket.
- New worktree files (`.aiman_todo.md`, `.aiman_handover.md`, `.aiman_review.md`) use the `.aiman_context.md` "DO NOT COMMIT" header and ignore mechanism, and are written only when their content changes.
- Import graph: `internal/gateway` imports `internal/server`, which imports `internal/usecase`. New usecase code must not import gateway.

## Noise budget (dashboard)

- Header: `aiman · <host>` and `<n> sessions · <k> needs you`. Nothing else.
- Session row: glyph, ticket, name, progress bar, `n/m`. Nothing else.
- Detail: one dim meta line (`agent model · elapsed · branch · ~$cost · cache %` · `reviewing…` while a verifier runs · `watching PR #n` when on), one compact git/PR line (ahead/behind, changes, PR number, state, review, checks, PR labels) when there is git or PR state, the to-do list, the waiting question only when present, then the pane preview.
- Toasts only when a session starts needing the user (waiting for input, error, CI escalation, review with REQUIRED findings). Everything else, including a clean review pass, is quiet.
- Reviewer sessions are hidden from the default list.

## Architecture

```
 laptop TUI ──ssh──▶ aiman serve (remote, single writer)
     ▲               ├ session registry (all sessions, W0)
     │               ├ todo store + steering queue (W1)
     │               ├ usage parser (W3), CI watcher (W4), verifier (W5)
     │               ├ handover + context index (W6), notification log (W2)
     │               ▲
     │               └── aiman CLI (agents, hook reporters, aiman-trigger)
     └── session.events stream (activity, todo_changed, steer, usage, notify)
 aiman-gateway ── polls serve notification log ──▶ phone push, webhook/ntfy, Slack channel
```

### Shared interfaces (every plan uses these names)

```go
// internal/domain/todo.go
type TodoState string  // "pending" | "active" | "done" | "dropped"
type TodoAuthor string // "user" | "agent" | "aiman" | "verifier"
type TodoItem struct {
    ID        string // "t1", "t2", … never reused within a list
    Text      string
    State     TodoState
    UpdatedBy TodoAuthor
    UpdatedAt time.Time
    Ref       string // optional, prefixed: "child:<id>" (W8), "check:<name>" / "review:<comment id>" (W4), "finding:<review id>/<id>" (W5)
}
type TodoList struct {
    SessionID string
    Items     []TodoItem // display order
    Rev       int64
    UpdatedAt time.Time
}

// internal/todo (new package), owned by serve
type Store interface {
    Get(sessionID string) (domain.TodoList, error)
    Mutate(sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) (domain.TodoList, Change, error)
}
// Change describes what a mutation did, for steering diffs and events.
// A Ref-only change (OpRef) is stored but never steers.

// internal/server/steer.go
type SteerNote struct {
    SessionID string
    Author    domain.TodoAuthor // user | aiman | verifier
    Text      string            // full note text, already formatted
}
type Steerer interface {
    Enqueue(n SteerNote)                      // coalesces within 800 ms per session
    Status(sessionID string) SteerStatus      // queued count, last delivered, last error, held reason, Pending []SteerNote
}

// internal/server/events.go: hub replacing per-connection diff loops for new events
type Hub interface { Publish(ev SessionEvent) }
```

`SessionEvent.Type` gains `todo_changed` (`id`, `rev`), `steer` (`id`, `queued`, `delivered_at`), `usage` (`id`), `notify` (`id`, `kind`), `verify` (`id`, `state`; every finished review). The TUI reacts to these without triggering a pane refresh and fetches details with the matching `*.get` method.

---

## W0. Serve knows every session

- Sessions created on the laptop (snapshot resume, adopted worktrees, workspace reuse, skill selection, remote-create fallback: `internal/ui/create_remote.go:60-68`, `dashboard.go:2014-2033`) are registered with serve through a new `session.register` method carrying the full `domain.Session`. Serve persists them like its own (`handlers_session.go:262-264` keeps them in memory today) and accepts their hook reports (`:787-793` drops them today).
- On TUI start, every known session for a remote that serve lacks is registered (idempotent upsert).
- `CreateSession` records the base branch on the session (`BaseBranch`) for W4/W5 diffs.
- `session.events` no longer exits when serve has no PTY runtime (`handlers_events.go:71-73`); tmux sessions get activity from the hook state.
- Fix: two quick sessions created in the same minute collide on branch, worktree and tmux name (`adhoc-YYYYMMDD-HHMM`). Use seconds plus a 4-char random suffix.

## W1. To-dos, steering and the A1 dashboard

### Store

- `internal/todo` file store: `~/.aiman/todos/<session-id>.json`, written atomically (temp + rename) under a per-session mutex in serve. Only serve writes.
- At most one item is `active`; setting one active demotes the previous to `pending`.
- After every mutation serve renders `.aiman_todo.md` into the session's working directory (only if content changed), publishes `todo_changed`, and, for user/aiman/verifier authors, enqueues a steering note.

### Socket API (`internal/server/handlers_todo.go`)

| Method | Params | Notes |
|---|---|---|
| `todo.get` | `session` | returns `TodoList` (incl. `not_in_agent`) + `SteerStatus` |
| `todo.summary` | `sessions?` | per session `done`, `total`, `active` text, `rev`; feeds list progress bars in one call |
| `todo.add` | `session`, `text`, `after?`, `ref?` | |
| `todo.update` | `session`, `id`, `text?`, `state?` | |
| `todo.move` | `session`, `id`, `to_index` | |
| `todo.remove` | `session`, `id` | |
| `todo.mirror` | `session`, `items[{text,status}]` | agent snapshot from a hook; see merge |
| `steer.send` | `session`, `text`, `as?`, `mode?` | free text; `mode` `note` (default) goes through the delivery gate; `answer` types at once only when the agent shows a dialog and is never queued |

Author is not a free parameter. It is derived from the caller: the CLI inside a session (`AIMAN_ENV=1` and `AIMAN_ID` equal to the target) is `agent`; `todo.mirror` is always `agent`; the laptop TUI passes `as: "user"`; serve-internal callers (W4, W5, W8) use `Mutate` directly with `aiman`/`verifier`.

### CLI (`cmd/aiman/todo_cmd.go`)

`aiman todo list|add|start|done|drop|edit|move|rm [--session NAME] [--json]`. `--session` defaults to `$AIMAN_ID`. `list` prints ids (`t3 ▸ Run preprod --check`), so agents can address items. `aiman todo report --from-stdin` is the hook entry point: it parses the raw hook payload and calls `todo.mirror`.

### Agent mirroring

- The `aiman todo` CLI taught by the skill is the primary path for every agent (this host's Claude transcripts show no `TodoWrite` or `TaskCreate` calls at all). Vendor mirroring is a bonus.
- Claude: `internal/agenthook` registers `PostToolUse` for `TodoWrite` and, when the first task finds a readable full list, the Tasks tools (`TaskCreate`/`TaskUpdate`), invoking a second reporter script (`~/.aiman/hooks/todo-report`) that runs `aiman todo report --from-stdin`. `tool_input.todos[]` has `content` and `status` (`pending|in_progress|completed`). aiman does not disable Claude's Tasks tools.
- Codex: the first task checks whether Codex hooks expose `update_plan` input. If yes, mirror the same way; if not, Codex uses the CLI.
- All agents: the skill gains a "Your to-do list" section: use `aiman todo` with ids, keep one item active, act on `[aiman]` notes.

### Merge rule for `todo.mirror`

1. Normalise texts (lowercase, collapse whitespace, strip punctuation).
2. Match each snapshot item to an existing item by exact normalised text, else by similarity ≥ 0.8 (token Jaccard). Each existing item matches at most once.
3. A matched **agent-authored** item takes the snapshot's text, status and position.
4. A matched **user/aiman/verifier-authored** item takes only the snapshot's status (the agent acting on it) and keeps its text and author.
5. Unmatched snapshot items are added as agent items, in snapshot order.
6. Unmatched existing **agent** items become `dropped` (shown struck through, never deleted silently).
7. Unmatched existing **non-agent** items are kept unchanged. If the steering note announcing them has already been delivered and the agent's snapshot still omits them, the item is marked `⚠ not in agent's list` in the detail pane. No re-send.
8. Display order: snapshot order for matched/new items, with kept non-agent items at their previous relative positions.

### Steering

- `Steerer` keeps an in-memory queue per session, coalescing within 800 ms, and persists undelivered notes in serve's DB so a serve restart keeps them.
- **Delivery gate (the user's decision):** deliver at once when the agent is ready for input: hook state `idle`, or `waiting_input` caused by an idle prompt (Claude `idle_prompt`). Hold while the agent is working or showing a permission/choice dialog; deliver on the next transition to ready. Sessions without hooks use the pane classifier with high confidence only. If the agent has ended, notes are not delivered; they stay visible as "not delivered" and go into the next handover.
- Serve delivers directly: `ptyruntime` input for pty sessions, local `tmux send-keys` for tmux sessions. It does not go through the laptop-side `usecase.SendSessionPrompt` (SSH, ~10 s confirm loop).
- Note text (exact):

```
[aiman] Your to-do list was changed by {author}:
{one line per change: "+ t7 Add rollback test", "~ t3 now: Run preprod --check --dry", "✓ t2 done", "- t5 removed", "↕ t4 moved to 2"}
Current list:
{"t1 ✓ …", "t3 ▸ …", "t4 ○ …"}
Adjust your work to match. Update the list with `aiman todo` using these ids.
```

- The dashboard prompt line sends free text through the same queue and gate, without the wrapper (`steer.send`, mode `note`).
- `steer.send` mode `answer` is the one exception: it types at once when the agent is in `waiting_input` on a dialog, and otherwise does nothing. Only `aiman-trigger`'s autonomous unblocker uses it.

### Dashboard: layout A1

Mockup: https://claude.ai/artifact/UB1zh3xsG688W7YS57iyZq (A1).

- List row and header per the noise budget. State glyphs: ● waiting for you, ◐ working, ✓ done, ✗ failed, ○ idle.
- Detail: title; dim meta line; to-dos (done dim, active bold `▸`, pending `○`, dropped struck, `⚠ not in agent's list` where it applies); the agent's question in the waiting colour under the active item only when state is `waiting_input` and `HookStateMessage` is non-empty; pane preview below as today.
- Prompt line at the bottom: `› Reply to <ticket>…` with a `queued`/`delivered` marker after sending.
- Keys: `i` focuses the prompt line (`enter` send, `esc` leave). `t` focuses the to-do list; inside it `a` add, `e` edit, `space` toggle done, `s` set active, `d` drop, `x` delete, `J`/`K` move, `esc` back. All existing main-view keys keep their meaning. Before binding, the plan checks `i` and `t` are free at `dashboard.go:784-808`; if taken, use `ctrl+p` and `ctrl+t`.
- The TUI fetches `todo.get` for the selected session on selection and on `todo_changed`; the event carries only id and rev, so dropped events are harmless.
- Sessions without a list: "no to-do list yet", no bar.

## W6a. Handover (ships right after W1)

- Serve writes `.aiman_handover.md` and a `handover` context note on: archive, forget, restart, agent switch, and Claude/Codex `PreCompact` (new hook registration). Not on `SessionEnd`, which Claude also fires for `/compact` and `/clear`.
- Content: the to-do list, undelivered steering notes, `git diff --stat` against `BaseBranch`, the last hook title/message, and the last 40 cleaned pane lines (serve captures locally: `ptyruntime` capture or local `tmux capture-pane`).
- Restart and agent switch include the handover in the handoff prompt instead of asking the agent to write `.aiman_session_summary.md` (`internal/usecase/restart_handoff.go:26`), falling back to today's flow when none exists. `ReviveIfNeeded` keeps native resume and appends a one-line pointer to the handover file.

## W3. Usage and cost

- Serve parses known transcripts (`AgentSessionPath`): Claude JSONL `message.usage` (input, output, cache creation, cache read, model); Codex JSONL `token_count`. Others report "unknown".
- Incremental parsing keeps a byte offset plus the first record's id per file; a shrunk file or changed first id resets to zero (Claude rewrites on compact).
- Totals per session in serve's DB. Price table in config with built-in defaults per model id. Costs are labelled `~$`. Codex models show tokens and an estimated `~$` from OpenAI's published API prices (user decision). Only model ids with no known or configured price show tokens without `~$`.
- `usage.get {session|group|issue|day}` returns tokens by kind, estimated cost and cache hit ratio `cache_read / (input + cache_read + cache_creation)`. CLI `aiman usage`.
- Dashboard: cost and cache % on the detail meta line only. `$` opens a usage view with per-ticket and per-day rollups.

## W2. Notifications

- Serve appends notification-worthy transitions to a persisted log: `waiting_input` (idle prompt or dialog), `ended`, `error`, `ci_escalation` (W4), `review_done` (W5). It applies `min_interval` per session and kind, storing last-notified times in its DB, and publishes `notify` events.
- The existing gateway notifier (`internal/gateway/notify.go`, which already polls serve) reads the log instead of inferring transitions itself, so nothing double-pushes. It delivers to phone push (existing), a webhook/ntfy URL, and optionally a Slack channel.
- The laptop TUI raises toasts for the same events when it is open.
- Config, per remote (`~/.aiman/config.yaml`, which serve reads):

```yaml
notifications:
  events: [waiting_input, ended, error, ci_escalation, review_done]
  phone: true
  webhook: ""          # ntfy topic URL or any POST endpoint
  slack_channel: ""    # posts as the approving member; use a channel others watch, or leave empty
  min_interval: 2m
```

- Message: `<ticket or name> on <host>: <event> · <question or last line>`.

## W4. CI and review reaction

- Runs inside serve (single writer, has every session after W0). `aiman-trigger` is not involved, apart from the fixes below.
- For each session with `watch_pr` on (default on for autonomous sessions; toggled in the TUI with a free key chosen in the plan), serve resolves the PR with `gh pr view --json number,headRefOid,statusCheckRollup` in the worktree every 2 minutes.
- **Failed check:** if no to-do with `Ref = check:<name>` is open, add `Fix CI: <name>` (author `aiman`, which steers) with the last 150 lines of `gh run view --log-failed` in the note. While that item is open, no further notes for that check.
- **New review comment** (dedupe by comment id): add `Address review: <first line> (<file>:<line>)` with the comment body in the note.
- Attempts counted per check name per head SHA; re-polling the same SHA never burns an attempt. After 3 failed attempts across new SHAs, stop steering, emit `ci_escalation`, and mark the session as needing the user.
- State (seen comment ids, attempts per SHA) persists in serve's DB.
- `aiman-trigger` fixes: honour `PollFrequencySecs` (`daemon.go:82`), and send scheduled prompts through serve's steering queue (`steer.send` mode `note`) and the unblocker through `steer.send` mode `answer`, instead of raw `tmux send-keys` (`daemon.go:253,313`).

## W5. Verifier sessions

- Start: `aiman verify [--session NAME] [--agent NAME] [--wait]`, a TUI key chosen in the plan (`v` is VS Code), or automatically when `verify.before_pr: true` (the skill tells agents to run `aiman verify --wait` before `gh pr create`).
- Reviewer agent: first entry of `verify.agents` that is installed and differs from the worker's agent.
- Tree under review: serve snapshots the worker's working tree including uncommitted and untracked (non-ignored) files into a commit object (temporary index, `git add -A`, `write-tree`, `commit-tree`) and adds a detached worktree at it. No mutagen sync. The reviewer is a child session with `Role: reviewer`, hidden from the default list.
- Prompt: diff of the snapshot against `BaseBranch`, `.aiman_task.md`, the worker's to-dos, and the findings format: one `## [REQUIRED|SUGGESTION] <title>` per finding with `file:line` and reasoning, ending with `<!-- aiman:review-complete -->`.
- Completion: serve watches for the end marker in `.aiman_review.md` (or reviewer process exit). It then parses findings, adds each REQUIRED finding as a worker to-do (author `verifier`, steers), stores the review as a `review` context note, publishes a `verify` event, emits `review_done` only when there are REQUIRED findings (a clean pass is logged quietly), stops the reviewer, and removes its worktree after the process has gone.
- `--wait` polls `verify.status` (each call under 30 s) and exits 0 with no REQUIRED findings, 1 otherwise, printing findings to stdout. When the worker itself runs `--wait`, its findings are delivered as stdout plus to-dos; the steering note is held until the worker is idle again (normal gate).

## W6b. Memory

- Typed notes: `ContextEntry` gains `Type` (`decision|pitfall|convention|fact|handover|review|summary`), `Supersedes`, `ValidUntil` in frontmatter. Old notes read as `fact`.
- Search index: an FTS5 table in serve's DB, updated inside `Files.Put`/delete when the writer is serve. CLI writes made while serve is down still land in markdown; on start serve compares file mtimes with the index's high-water mark and reindexes changed files. If the index cannot open, search falls back to today's scan. The first task benchmarks search over 1,000 notes so the index is justified with numbers.
- `context.find` returns ranked compact hits (id, type, title, abstract, age) with no body; `context.get` returns the body.
- Packs and search skip superseded and expired entries, and rank decision, pitfall and convention notes before others, then recency.
- Extraction: at archive and when a handover is written, serve queues a "memory candidates" job. The laptop TUI runs it with the local summariser when it connects, proposes up to 5 typed notes, and saves only those the user accepts.

## W7. Faster, cheaper starts

- Worktree bootstrap, run after `git worktree add` in `CreateSession`:

```yaml
repos:
  owner/name:
    copy: [.env.local]
    symlink: [node_modules]
    setup: "make deps"
```

- RTK: an optional install step on the Agent API settings page (`internal/ui/agent_api.go`) installs RTK (the Apache-2.0 edition) and runs its hook setup for the installed agents. The usage view (`$`) shows `rtk gain` totals for the host.
- graphify: if present on the remote, `CreateSession` runs `nice graphify update .` in the background after the agent starts, and `.aiman_task.md` mentions `graphify query`.
- Cache hygiene: every writer of skills, hooks, task, context, todo and handover files compares content first and leaves unchanged files untouched.

## W8. Spec mode and fan-out

- Wizard mode "Spec" creates a planner session. Its prompt asks for `docs/specs/<ISSUE>.md`, committed on the planner branch, and a to-do list where each item is an independently buildable task.
- The user edits the list in A1. `F` on the planner (checked free in the plan) opens a picker of pending to-dos. Each selected item becomes a worker child session branching from the planner branch, with the spec and that one to-do as its task. The planner's to-do gets `Ref = child:<child id>` (a Ref-only change, which does not steer).
- Children open PRs into the planner branch. The planner's item shows ◐ while the child works and is marked done (author `aiman`, which steers the planner) when the child's PR is merged into the planner branch, or, with `verify.before_pr`, when its verifier passes and its PR is open.
- The planner opens the final PR to the default branch.

---

## Order

Revised from the plans' real dependencies (details in `2026-10-22-nl-0-roadmap.md`):

1. W0 + W1 backend (nl-1): registry, to-dos, steering, Hub, `aiman rpc`
2. W1 dashboard A1 (nl-2): needs nl-1
3. W6a handover (nl-3a): needs nl-1 (`SteerStatus.Pending`, `fsutil`, `aiman rpc`)
4. W3 usage (nl-4): needs nl-1's Hub and nl-2's meta-line hook
5. W2 notifications (nl-5): needs nl-1's Hub and the W0 `session.events` fix; after nl-4 (config signature)
6. W4 CI reaction (nl-6): needs nl-1, nl-2, nl-5
7. W6b memory (nl-3b): needs nl-3a; must precede W5 (review notes are typed)
8. W5 verifier (nl-7): needs nl-1, nl-2, nl-3b, nl-5
9. W7 starts (nl-8): needs nl-1, nl-4 (`$` view), nl-5, nl-7 (`PreparedWorktree`)
10. W8 spec mode (nl-9): needs nl-1, nl-2, nl-5, nl-7

Each ships on its own and is tagged as a release.

## Decided by Claude (open to change)

- Steering delivery gate as described under W1 (the user chose "at once when ready, otherwise at the end of the current step").
- To-dos as JSON files with serve as the only writer.
- 800 ms coalescing; persisted undelivered notes.
- Merge thresholds (similarity 0.8) and the "⚠ not in agent's list" marker instead of re-sending.
- `i` and `t` as focus keys, with chord fallbacks.
- The CI watcher lives in serve, not `aiman-trigger`.
- Verifier snapshot via a temporary-index commit; completion marker in `.aiman_review.md`.
- Spec files committed at `docs/specs/<ISSUE>.md`; children PR into the planner branch.
- Memory candidates need explicit acceptance; the summariser stays on the laptop.
- Header shows no cost; cost lives on the detail line and in the `$` view.
- RTK installs its Apache-2.0 edition (`RTK_EDITION=oss`) rather than the BUSL-1.1 default.
- `steer.send` mode `answer` for the trigger's unblocker, so it keeps answering dialogs while notes stay gated.
- A `verify` event for every finished review, so the TUI clears `reviewing…` without a notification on a clean pass.
