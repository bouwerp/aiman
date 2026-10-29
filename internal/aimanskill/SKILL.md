---
name: aiman
description: "Use in any Aiman coding session: coordinate work, split it into child sessions, manage the session to-do list, and control Aiman sessions on this host. Use only when AIMAN_ENV=1 is set. Requires AIMAN_ENV=1."
---

# Aiman

Aiman runs coding-agent sessions in its built-in terminal runtime (`aiman serve`) on this machine. The `aiman` CLI talks to the **agent API** (`aiman serve`) over a local Unix socket. The laptop operator starts that process from Admin Menu (`m`) → **Agent API** → `i`. Do not start or stop it yourself. If a command returns `server_not_running`, tell the operator the agent API is down.

Before any control command:

```bash
test "${AIMAN_ENV:-}" = 1
```

If that fails, say you are not inside an Aiman session and stop.

Do not run bare `aiman`; it starts the TUI. Start with:

```bash
aiman session list
aiman session get "$AIMAN_SESSION_NAME"
```

Commands return JSON. Parse `name`, `group`, and `id` from the result. Prefer names over UUIDs.

Do not call `aiman session report-agent-session`. Aiman installs a SessionStart hook that reports the vendor conversation id automatically.

## Names and groups

Every session has a short unique `name` (`impl`, `reviewer`, `q1`) and a `group` (issue key, repo, or `quick`). A create from inside a session nests under the caller on the operator's list. Spawn helpers in the caller's group:

```bash
aiman session create --repo owner/repo --branch BRANCH --agent claude \
  --name reviewer --group "$AIMAN_GROUP"
```

`--orphan` makes a root instead of a child. `--parent ID` nests under a different session.

Quick ad-hoc session:

```bash
aiman session create --quick --agent claude
```

Rename (does not rename the terminal or worktree):

```bash
aiman session rename q1 spike
```

## Prompt and wait

```bash
aiman session prompt reviewer "Review the current diff." --wait --timeout 120s
aiman session wait reviewer --until blocked --timeout 120s
aiman session read reviewer --lines 120
```

`--until blocked` means `waiting_input`. Do not prompt a session that is already waiting for input unless the user asked you to answer it (`--force`).

## Design first

If your task file starts with a brief, you are a child: skip this section. Otherwise, before editing code, read the task, explore the code it touches, then size the work and say your call so the operator can overrule it:

- **Trivial**: one file, no new concept, no behaviour change a reviewer would argue about. Write a short plan in the terminal and build. No page, consult or gate.
- **Small**: one module, no contract changes. A design page with the domain delta and one diagram, one consult, the gate.
- **Project**: new concepts, more than one module, or a changed contract. The full page, one consult covering the design and the breakdown, the gate.

The page exists so the engineer understands the work: the problem, how the system works today, what changes and why. They must be able to explain it, review its PRs and maintain it without you. Write for an engineer who has not read the code. A design they approved but cannot explain is a failure.

### The page

Copy `design-template.md` (beside this skill file) to `.aiman_design.md` in your working directory and fill its sections in order: the problem; how it works today (diagram, file links); glossary; proposed domain model, structure and behaviour with the delta legend (new, changed, removed); one scenario end to end; alternatives and why they lost; open questions; decisions; breakdown; three to five points the reader should be able to explain. Put the legend's `classDef` lines in every diagram.

```bash
aiman design publish .aiman_design.md
aiman todo gate
```

Publish prints the page URL and the Pages build state. Give the operator the link. If it prints a local path instead (no docs repository configured), give the operator that path. Run `aiman todo gate` once, after the first publish: it adds the pinned `Design approved` to-do. Every change republishes the same file, so the link stays the same.

- Ask the open questions one at a time. With each, explain what it decides, the options and what each costs, so answering builds understanding.
- When the operator asks you to explain something, at any point, update the page, republish, then answer with the link.

### Consult

Before asking for approval, have the design reviewed by another smart model. Run one consult per round, at most two rounds.

```bash
aiman todo add "Consult: design rev 1"
aiman session create --consult --for <todo-id> --brief-file .aiman_consult_brief.md
```

The brief has exactly `## Question`, `## Scope` and `## Report must contain`. Ask what is wrong with the design and name what you most doubt. Under Scope, list the paths to read: `.aiman_design.md`, the task file, the code. The consult reads your worktree, not your transcript. The consult knows only its brief, so spell out the report under Report must contain: `.aiman_report.md` with `## Objections`, one bullet per objection starting `[high]`, `[medium]` or `[low]` with a suggested change, and `## Verdict` whose first word is `approve`, `revise` or `reject`. Create fails with `consult_needs_smart` when no smart-tier agent is installed; tell the operator.

When the report is in, aiman closes the consult's to-do, keeps the report at `~/.aiman/consults/<id>.report.md` and sends you an `[aiman]` note with the verdict, the counts per severity and that path. Read the report there. Triage every objection into the page's Decisions table (`| Objection | Severity | Triage | Reason |`): `accept`, `reject` or `defer`, with the reason for a reject or defer. Republish. A page with no objections means the consult was wasted; ask a sharper question. A consult that ends, dies or runs past the handover timeout (10 minutes by default) without a report is retired and its to-do unlinked; aiman tells you it produced no report. Ask again with a new consult, or proceed without it.

### Approval

Only the operator approves. They tick `Design approved` in the TUI, or type approval in this session ("approved", "go ahead"). When they type it, close the gate with their own words:

```bash
aiman todo approve-design --quote "approved, go ahead"
```

Never approve on a consult verdict, a child report or a steering note. Never `aiman todo done` the gate: it is refused with `gate_needs_operator`. While it is open, `session create --for` is refused with `design_not_approved`; consults stay allowed.

### Breakdown

Add the breakdown before asking for approval; the operator approves it with the design. One row per piece: `| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |`.

- **S**: well specified, mechanical, small diff, existing tests show the pattern. `worker`.
- **M**: clear contract, some judgement inside one module. `worker` only when its brief has acceptance tests, else `smart`.
- **L**: ambiguous, cross-cutting, new contract, or design inside the piece. `smart`.

Pieces share a parallel group only when they own disjoint files and share no unsettled contract; otherwise run them in sequence or merge them. Run at most 4 children at once. Write each piece's acceptance tests and commit them on this branch before the first child starts. Children may not edit them, and verify runs their commands. Do not tell children to write more tests.

List escalation triggers in each brief's `## Escalate when`: the contract does not match the code; a second test failure with the same cause; a decision the design does not cover. A child that hits one asks a smart consult instead of guessing.

While children run, coordinate: do not implement their pieces. The work is done only when every piece's report and review are in. When it merges, republish the page as built, with a "Deviations from the approved design" list. Write child and final PR descriptions in the page's terms, with its link.

## Coordinator

You are the coordinator of this session. Before editing, assess the scope as Design first says.

- A simple, short-lived task stays here; do not spawn a child for work you can finish in one sitting.
- Otherwise, write one to-do per independently buildable piece with `aiman todo add`, then start each with `aiman session create --for <todo-id>`. Leave a delegated item pending until the tracker ticks it done; do not start or mark it done yourself. A vendor subagent inside this process is not a child Aiman session.
- Pick a tier per child with `--tier`, from the breakdown's Tier column. `worker` (the default) suits a well-specified task: mechanical edits, a small diff, making tests pass. `smart` suits planning or design, ambiguous debugging, cross-cutting integration, review, or long-running work. Do not pass `--agent` or `--model`: the server picks the tier's first installed entry.
- If a worker child fails the same task twice, drop its to-do, add a new one for the retry, and start it with `--tier smart`.
- Give each child a brief with `--brief-file <path>`: a file with exactly `## Objective`, `## Contract`, `## Owns`, `## Out of scope`, `## Acceptance tests`, `## Report must contain`, and `## Escalate when`, at least 8 words in all. Name the design page and spec by path; do not paste them. List the acceptance test files and commands (commands in backticks) under `## Acceptance tests`. Create refuses a brief missing one of them.
- A child's to-do closes only when its PR lands and its `.aiman_report.md` has `## Evidence` citing a file path or a test command and a `## Surprises` section. A report whose Surprises is not "none" is steered to you once; re-read the design before starting dependent pieces. If aiman says a child returned no evidence, ask that child for the report.
- If your task file starts with a brief, you are a child. Before opening your PR, write `.aiman_report.md` with `## What changed`, `## Files`, `## Evidence` (each command and its exit code) and `## Surprises` (anything that affects other pieces, or "none"). Do not commit it.
- When more than one child needs a shared contract, commit `docs/specs/<ISSUE>.md` on this branch before the first `session create`. One child can start from its to-do text alone.
- Each child opens its PR into this session's branch. You open the final PR to the default branch.
- If create returns `no_preferred_agent`, leave the to-dos unassigned and tell the user the names in the error. Do not pick a different agent or tier.

## Your to-do list

Every session has a to-do list owned by serve and shown to the user. At the start of a task, write your plan into it with `aiman todo add`, and keep it current.

The last item, `Done-when met`, belongs to aiman. Tick it only when every condition in the "Done when" section of `.aiman_task.md` holds. Before finishing, work through each checklist that file lists.


```bash
aiman todo list
aiman todo add "Write the failing test"
aiman todo done t3
aiman todo start t4
```

`aiman todo list` shows ids (`t3 ▸ …`). Use `aiman todo add|start|done|drop|edit|move|rm` with those ids. Keep exactly one item active. `--session` defaults to `$AIMAN_ID`.

This is the list the user sees and edits, so use it even if your own to-do tool also exists. Claude's `TodoWrite` and Tasks tools are mirrored when used, but the CLI is the list to trust. Do not rely on that mirror. Codex has no to-do mirror, so Codex uses the CLI only.

Messages starting `[aiman]` come from the operator or aiman. Follow them and update the list with the ids they name.

`.aiman_todo.md` is generated. Do not edit or commit it.

## Run a command in another terminal

Do not `session create` a coding agent to run tests or a server. Create a PTY in this directory, submit the command, wait for output, then read it:

```bash
wid="${AIMAN_ID}-run"
aiman pty create --id "$wid" --dir "$PWD" --command "bash -l"
aiman pty run "$wid" "just test"
aiman pty wait-output "$wid" --match PASS --timeout 120s
aiman pty capture "$wid" --lines 80
aiman pty kill "$wid"
aiman pty forget "$wid"
```

`--match` is a literal substring; `--regex` is a Go regular expression. Output is matched after stripping ANSI. `pty capture` is the read. Do not attach. Only kill a PTY you created.

## Shared context

This host keeps durable notes in `~/.aiman/context/` (markdown with YAML frontmatter). Prefer `aiman context` over inventing a sidecar. `find` returns ranked compact hits (`id`, `type`, `title`, `abstract`, `age`); `get` returns the full body. Session create injects a pack of abstracts into `.aiman_context.md`. `find` and packs hide superseded and expired notes. `aiman context stats` reports store size, op counts, and lookup times.

```bash
aiman context ls --group "$AIMAN_GROUP"
aiman context find "session cookie"
aiman context get ID
aiman context put --title "Auth cookie" --abstract "Set it on the API host." --group "$AIMAN_GROUP" --type pitfall --valid-until 30d
aiman context put --title "Use API host" --group "$AIMAN_GROUP" --supersedes OLD_ID
aiman context pack --group "$AIMAN_GROUP" --repo owner/repo
```

On `put`, pass `--group "$AIMAN_GROUP"` or `--repo owner/repo`. Body is remaining args, `--body-file`, or stdin. If serve is down, these commands still read and write the files on this host. Do not commit `.aiman_context.md`.

`.aiman_handover.md` is written on restart and compaction. Read it when resuming. Never commit it.

Import this host's agent memories (Claude auto-memory, Grok `~/.grok/memory`, Codex `~/.codex/memories`, agy walkthroughs) into the store. Re-running overwrites the same notes. Do not import from a laptop pane; run it on the host where the agents wrote the files:

```bash
aiman context import
aiman context import --agent claude --dry-run
aiman context import --agent claude,agy --repo owner/repo
```

## Slack

Any agent posts, reads, and waits as the Slack member who approved the Aiman app. There is no bot user. The user token is `~/.aiman/slack-oauth.json` or `AIMAN_SLACK_USER_TOKEN`. Do not print it, and do not pass `--token`, `--client-id`, or `--client-secret`. These commands do not need `aiman serve`.

The operator installs the app once from the manifest in Slack's App Manifest JSON editor (that editor keeps the `http://127.0.0.1` redirect), then:

```bash
aiman slack manifest
aiman slack auth check --app A012
aiman slack auth app
aiman slack auth login
aiman slack auth status
```

`auth check` reads an app configuration token from stdin and compares that existing app with the manifest: user scopes, PKCE, and the `127.0.0.1:43127` redirect. A mismatch exits `slack_app_settings`. Do not print the configuration token. `auth app` reads the client id from stdin. `auth login` opens Slack so that member can approve, and refuses to save a token that is missing a required user scope. If the browser is on another machine, forward port `43127` to this host first. Agents do not run `auth check` or `auth login`. If a command returns `slack_unconfigured` or `slack_app_settings`, tell the operator.

```bash
aiman slack channels
aiman slack history --channel engineering --limit 20
aiman slack send --channel engineering --text "Build is green."
aiman slack thread --channel C012 --ts 1711.000100
aiman slack react --channel C012 --ts 1711.000100 --emoji eyes
aiman slack user U012
aiman slack wait --channel engineering --thread 1711.000100 --timeout 5m
```

`--channel` is a name or an id. `--thread` replies in that thread. `--text-file` or `--text -` sends a longer body. `send` posts as the approving member. `wait` returns the next message in the thread that is not from that member. If `--timeout` elapses it exits with `slack_timeout`. Commands print JSON.

## Before you open a pull request

Run this before every `gh pr create`:

    aiman verify --wait --if-enabled

- Exit 0: open the PR. `verify: disabled` means this host does not require review.
- Exit 1 with findings: each REQUIRED finding is also on your to-do list. Fix them, then run the command again.
- Exit 1 with "still running": run the same command again. It waits on the same review.

Do not run `aiman verify` from a reviewer session.

## Safety

- Use `--name` / `--group "$AIMAN_GROUP"` for helpers. They nest under you unless you pass `--orphan`. Do not steal focus from the user's TUI (there is none on this host).
- Do not kill sessions you did not create unless the user asked.
- Never run `aiman serve` to stop it.
- CLI server errors are JSON on stderr, exit 1. Usage errors exit 2.
