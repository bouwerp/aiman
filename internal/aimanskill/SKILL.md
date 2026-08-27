---
name: aiman
description: "Control Aiman sessions on this host. Use only when AIMAN_ENV=1 is set and the task needs listing, creating, prompting, waiting on, or renaming Aiman sessions, or reading and writing shared context. Requires AIMAN_ENV=1."
---

# Aiman

Aiman runs coding-agent sessions in tmux on this machine. The `aiman` CLI talks to the **agent API** (`aiman serve`) over a local Unix socket. The laptop operator starts that process from Admin Menu (`m`) → **Agent API** → `i`. Do not start or stop it yourself. If a command returns `server_not_running`, tell the operator the agent API is down.

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

Rename (does not rename tmux):

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

This host keeps durable notes in `~/.aiman/context/` (markdown with YAML frontmatter). Prefer `aiman context` over inventing a sidecar. `ls`/`find` return abstracts; `get` returns the full body. Session create injects a pack of abstracts into `.aiman_context.md`. `aiman context stats` reports store size, op counts, and lookup times.

```bash
aiman context ls --group "$AIMAN_GROUP"
aiman context find "session cookie"
aiman context get ID
aiman context put --title "Auth cookie" --abstract "Set it on the API host." --group "$AIMAN_GROUP"
aiman context pack --group "$AIMAN_GROUP" --repo owner/repo
```

On `put`, pass `--group "$AIMAN_GROUP"` or `--repo owner/repo`. Body is remaining args, `--body-file`, or stdin. If serve is down, these commands still read and write the files on this host. Do not commit `.aiman_context.md`.

Import this host's agent memories (Claude auto-memory, Grok `~/.grok/memory`, Codex `~/.codex/memories`, agy walkthroughs) into the store. Re-running overwrites the same notes. Do not import from a laptop pane; run it on the host where the agents wrote the files:

```bash
aiman context import
aiman context import --agent claude --dry-run
aiman context import --agent claude,agy --repo owner/repo
```

## Safety

- Use `--name` / `--group "$AIMAN_GROUP"` for helpers. They nest under you unless you pass `--orphan`. Do not steal focus from the user's TUI (there is none on this host).
- Do not kill sessions you did not create unless the user asked.
- Never run `aiman serve` to stop it.
- CLI server errors are JSON on stderr, exit 1. Usage errors exit 2.
