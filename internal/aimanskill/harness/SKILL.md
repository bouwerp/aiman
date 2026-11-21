---
name: aiman-harness
description: "Use in an Aiman session to set up, add, fix, update or debug the harness for this repository: the app runner, its services, test scenarios and checks that a person tests from the harness console, and to share a ready run with the operator. Use only when AIMAN_ENV=1 is set. Requires AIMAN_ENV=1."
---

# aiman harness

Use this skill only inside an Aiman session. Check first:

```bash
test "${AIMAN_ENV:-}" = 1 || echo "not an aiman session: stop"
```

If the check prints anything, stop: this skill does not apply.

The harness starts the app and its services in an offline sandbox on this host, so that a person can test a scenario. The file is `.aiman/harness.yaml`. Serve reads it from the commit, never from the working tree, except with `--dirty`.

Read `harness.md` beside this file before you write or change the file. It has the file format, the run lifecycle and its refusals, AWS services, the database, the debugger, EVM traces, the network and stubs, image approval, and which host config keys are the operator's.

## Set up the harness

1. Detect the stack: `go.mod`, `package.json` (Node or Bun), `pyproject.toml` or `requirements.txt`. Find how the app builds and starts, and the ports it listens on.
2. Find the services it needs (database, queue, cache, chain, AWS) and every external API it calls. A run is offline: each external call a scenario makes needs a stub.
3. Write `.aiman/harness.yaml` with the app, the services and one scenario. Add an `app.debug` block for the stack (`go`, `node`, `bun` or `python`; see `harness.md`, Debugger).
4. Run `aiman harness list --dirty`. It reads the file from the working tree and starts nothing. Fix every error it prints and run it again until it prints none.
5. Run `aiman harness up <scenario> --dirty --wait`. Fix a failed step from `aiman harness status` and `aiman harness logs <service>`, run `aiman harness down`, and start again.
6. Run `aiman harness check`. Every check must exit 0.
7. Run `aiman harness down`. Commit the harness file and the files it reads.
8. Run `aiman harness up <scenario> --wait` on the commit. Add `--debug` only when the operator asked to debug.
9. Run `aiman harness share` and give the operator the URL it prints. If it answers that the gateway is not running, give them `aiman harness console --remote <host> --session <session name>` for their laptop instead.

## Fix or update the harness

When your change alters what a scenario tests, update the scenario in the same commit. Check it with `aiman harness list --dirty`, then run it as in steps 5 to 9.

A failed step comes to you as a to-do `Harness: <scenario> step <n>: <note>` and an `[aiman]` note. Fix it, commit, start the run again on the commit, share it, and tell the operator the scenario is ready.

## Rules

- A session has one live run at a time. Run `aiman harness down` when you are done, unless the operator is testing a shared run.
- Do not use `--force`. Verdicts, database and AWS writes, debug take and release, and debugging a check are the operator's; you get `needs_operator`.
- Host config (`harness.*`, `repos.<slug>.harness`) is the operator's. When a run needs another value, stop and tell the operator the key, the value and why. Do not work around a limit in the file.
- Do not put `AWS_*` credential or profile variables, or an AWS access key, in the file. It is refused.
