# nl-14: Agent updates (spec + plan)

User decisions (2026-10-28): default mode `auto` with window `03:00-06:00`; Deep Code on a root-owned npm prefix gets an offer to move it to a user prefix; the provisioner switches Claude to the native installer; the accidental ageni/muse updates during research are kept.

Every aiman-launched session disables the agent's own self-updater through per-process env vars and flags; user config files are never written. `aiman serve` on each host compares installed and latest versions, stores the result, and applies updates on request or inside the window. Updates are serialized and logged. An update waits while a session of that agent is mid-turn; for in-place installs it waits while any session of that agent runs. The PTY boot wait dismisses startup update dialogs with the skip key, never Enter. After an applied update serve re-runs `agenthook.EnsureOnHost`.

## 1. Research (host regent, 2026-10-28; V verified, U unverified)

| Agent | Install | Update command | Self-update and how to disable | Blocking startup prompt | Version installed / latest |
|---|---|---|---|---|---|
| claude | native `~/.local/share/claude/versions/` (V); provisioner uses npm (`internal/usecase/provisioner.go:38`) | `claude update` (V) | background; `DISABLE_AUTOUPDATER=1` (V). Not `DISABLE_UPDATES` (blocks manual update) | none; non-blocking "Update installed" (V) | `claude --version`; latest `https://downloads.claude.ai/claude-code-releases/stable` (path U) |
| agy | standalone ELF (V) | `agy update` (V) | background; `AGY_CLI_DISABLE_AUTO_UPDATE=1` (name V, value U) | none known (U) | `agy --version`; latest none cheap |
| grok | managed `~/.grok/downloads/` (V) | `grok update` (V) | on launch; `--no-auto-update` (already passed, `launch_flags.go:180`), `GROK_DISABLE_AUTOUPDATER=1` (V) | none (V) | `grok update --check --json`; cache `~/.grok/version.json` (V) |
| codex | standalone `~/.codex/packages/standalone/releases/` (V) | `codex update` (V) | startup check; `-c check_for_update_on_startup=false` (V) plus existing `--disable in_app_updates` | **yes**: "✨ Update available!", "Update now", "Skip until next version" (V). Dismiss `esc` (U). Enter unsafe. Rows use `›`, which matches `composerReadyRe` today, so the boot wait reads it as ready (live bug) | `codex --version`; latest `latest_version` in `~/.codex/version.json` (V) |
| kilo | curl `~/.kilo/bin/kilo` (V) | `kilo upgrade` (V) | npm installs only; `KILO_DISABLE_AUTOUPDATE=1` (V) | none, toast (V) | `kilo --version`; `npm view @kilocode/cli version` (U) |
| copilot | standalone ELF (V) | `copilot update` (V) | on startup; `--no-auto-update`, `COPILOT_AUTO_UPDATE=false` (V) | U | `copilot --version`; `npm view @github/copilot version` (U) |
| cursor-agent | versioned dirs (V) | `cursor-agent update` (V) | silent background; `--disable-auto-update` (already passed) | none (V) | `cursor-agent --version`; latest none cheap |
| pi | managed `~/.pi/agent/install/releases/` (V) | `pi update --self` (V) | no self-update; version check skipped by `PI_SKIP_VERSION_CHECK=1` (V). Not `PI_OFFLINE` | none; banner (V) | `~/.pi/agent/install/current-version`; `https://pi.dev/api/latest-version` `.version` (V) |
| muse | launcher + `muse-bin-<ver>` (V) | `MUSE_LAUNCHER_INSTALL=1 muse` (V, undocumented) | every invocation incl. `--version` (V, happened during research); `MUSE_NO_AUTO_UPDATE=1` (V) | none (V) | `.muse-version` file; channel JSON `https://api.meta.ai/muse-code/channels/muse-stable` `.version` (auth U) |
| deepcode | npm -g into root-owned `/usr/lib/node_modules` (V) | `npm install -g @vegamo/deepcode-cli@latest` (V) | background check writes `pending` to `~/.deepcode/update-check.json`; no env switch (V) | **yes** (V source): "Deep Code latest version has been released: A -> B", "> 1. Install...", "2. Ignore once", "Esc to ignore once". Enter and `1` install then exit. Dismiss `esc` | `deepcode --version`; `npm view @vegamo/deepcode-cli version` (V) |
| ageni | static ELF (V) | `ageni update` (V). **Ignores `--help` and installs** | none found (U) | none found (U) | `ageni --version`; GitHub releases latest (U) |

Hook files live in config dirs separate from versioned install dirs; no evidence updates rewrite them (schema migrations U).

## 2. Spec

- **D1 Guards.** Per-agent env and flags merged into every aiman launch: claude `DISABLE_AUTOUPDATER=1`; agy `AGY_CLI_DISABLE_AUTO_UPDATE=1`; grok `GROK_DISABLE_AUTOUPDATER=1` + `--no-auto-update`; codex `-c check_for_update_on_startup=false` + `--disable in_app_updates`; kilo `KILO_DISABLE_AUTOUPDATE=1`; copilot `COPILOT_AUTO_UPDATE=false` + `--no-auto-update`; cursor `--disable-auto-update`; pi `PI_SKIP_VERSION_CHECK=1`; muse `MUSE_NO_AUTO_UPDATE=1`; deepcode handled by D3. Env goes into the session env map at the single injection point `CreatePTYSession` (`internal/usecase/session_terminal.go:84`), never as a command prefix; an existing key is never overwritten. No tmux code is added (nl-13).
- **D2 Startup dialogs.** One table `UpdateDialogs{Agent, Re, Key}` (Codex `Update available!|Skip until next version` → `esc`; Deep Code `latest version has been released` → `esc`). The boot wait checks it before trust and before ready, continues after a dismissal (budget 20, outcome `reason=update`). A test fails if any key is `enter` or a digit. `steerReady` holds `dialog` when the pane matches the table even if a hook says idle; no mid-session auto-dismiss.
- **D3 Deep Code.** `DeepcodeLaunchScript` (`internal/agenthook/deepcode.go`) removes `$HOME/.deepcode/update-check.json` before exec under `AIMAN_ENV=1`. D2 is the backstop.
- **D4 Version state.** New `internal/agentupdate`: catalog (detection via `readlink -f $(command -v X)`, installed probe, latest probe, update command, `SwapSafe`), state `~/.aiman/agent-versions.json` (installed, latest, method, path, checked_at, error, last_update), log `~/.aiman/logs/agent-updates.log`. Probes run with guard env and never run an update subcommand (`muse` only with `MUSE_NO_AUTO_UPDATE=1`, never `ageni update`). Serve probes 2 min after start, then every `check_every`, and on demand. `SwapSafe` true for versioned installs, false for npm/yarn/pnpm/bun prefixes.
- **D5 Applying.** One update at a time per host, 10 min timeout, never sudo. EACCES gives `needs root: <command>`. Gate: SwapSafe waits while any live session of that agent is `working`; in-place waits while any live session of that agent exists; deferred updates retry next sweep; manual requests on a busy agent return the reason. After success: re-probe, `agenthook.EnsureOnHost`, log. Running sessions keep the old version until restart.
- **D6 Config** (`agents.update`, synced by `MarshalServeConfig`; `AgentsConfig.IsZero` accounts for it):
  ```yaml
  agents:
    update:
      mode: auto            # off | notify | auto  (default auto)
      window: "03:00-06:00" # host-local; auto applies only inside it
      check_every: 6h
      hold: []              # never auto-updated; manual still works
  ```
  `off`: no guards, probes or notifications (D2 dismissal stays). `notify`: guards, probes, one notification per new version. `auto`: notify plus apply inside the window, subject to the gate.
- **D7 UX.** Notify kind `agent_update` (NeedsYou false), deduped per (host, agent, latest). Agent API page `a` opens "Agents on <host>" (Agent, Installed, Latest, Method, Status; `u` update selected, `U` all outdated, `r` re-probe). CLI on the host: `aiman agents status [--json] [--refresh]`, `aiman agents update <cmd>|--all` via RPCs `agents.status`, `agents.update`.
- **D8 Deep Code user prefix.** When Deep Code's install is root-owned, the agents page offers `m` "move to user prefix": `npm config set prefix ~/.npm-global`, ensure `~/.npm-global/bin` is on the PATH aiman uses (remote preamble), `npm install -g @vegamo/deepcode-cli@latest`, re-probe. Never uninstalls the root copy (needs root); the user prefix wins on PATH. Confirm screen shows the exact commands.
- **D9 Provisioner.** `internal/usecase/provisioner.go` installs Claude with the native installer instead of `npm -g`.

## 3. Plan (TDD; gate `make ci`, `make eval` green)

```
A1 ─┬─> A2 launch guards ─> A3 boot wait + steer gate ─> A4 deepcode launcher
    └─> B1 agentupdate core ─> B2 serve+RPC+CLI ─> B3 UI + notify ─> B4 deepcode prefix + provisioner
```
- **A1** `internal/infra/agent/selfupdate.go`: `SelfUpdateGuard{Env, Flags}`, `GuardFor(base)`, `UpdateDialogs`, `BaseOf(cmd)`. Tests per agent; no dialog key enter/digit.
- **A2** Flags in `launch_flags.go` (`EnsureInteractiveLaunch`) and `PrepareSession`; env merged in `CreatePTYSession`; mode=off skips. Tests incl. user env wins over guard.
- **A3** `internal/usecase/boot_dialogs.go` builds the boot-wait branch; `flow_manager.go` boot wait order update → trust → ready; `steer_delivery.go` holds on a table match. Fixtures from real screens captured with a throwaway HOME (Codex `CODEX_HOME=tmp` with a newer `latest_version`; Deep Code temp HOME with `pending`), only `esc` pressed, checked into `internal/usecase/testdata/bootdialogs/`. Tests incl. `TestCodexUpdateModalIsNotReady` (fails today).
- **A4** Deep Code launcher removal of `update-check.json` under `AIMAN_ENV=1`.
- **B1** `internal/agentupdate/{catalog,probe,state,gate,runner,policy}.go` with parser tests from the real version strings, `TestProbeNeverRunsUpdateSubcommand`, gate cases, window across midnight, serialized runner, needs-root.
- **B2** Config `AgentsConfig.Update`; `internal/server/handlers_agents.go` + dispatch; serve loop and post-update `EnsureOnHost` in `cmd/aiman/serve.go`; `cmd/aiman/agents_cmd.go`.
- **B3** `NotifyAgentUpdate`; `internal/ui/agent_updates.go` view, `a` key in `agent_api.go`, route in `remote_services.go`.
- **B4** D8 Deep Code user-prefix action; D9 provisioner native Claude.

Overlaps: nl-13 touches `flow_manager.go`, `session_terminal.go`, `steer_delivery.go`, `server.go`, `serve.go`, `config.go`, `agent_api.go`; keep edits there to one call or case and rebase. A boot-dialog eval needs nl-13 T4's `FakePTY`.
