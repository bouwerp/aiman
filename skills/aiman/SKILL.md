---
name: aiman
description: "Control Aiman sessions on this host. Use only when AIMAN_ENV=1 is set and the task needs listing, creating, prompting, waiting on, or renaming Aiman sessions. Requires AIMAN_ENV=1."
---

# Aiman

Aiman runs coding-agent sessions in tmux on this machine. The `aiman` CLI talks to `aiman serve` over a local Unix socket.

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

## Names and groups

Every session has a short unique `name` (`impl`, `reviewer`, `q1`) and a `group` (issue key, repo, or `quick`). Spawn helpers in the caller's group:

```bash
aiman session create --repo owner/repo --branch BRANCH --agent claude \
  --name reviewer --group "$AIMAN_GROUP"
```

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

## Safety

- Use `--name` / `--group "$AIMAN_GROUP"` for helpers. Do not steal focus from the user's TUI (there is none on this host).
- Do not kill sessions you did not create unless the user asked.
- Never run `aiman serve` to stop it.
- CLI server errors are JSON on stderr, exit 1. Usage errors exit 2.
