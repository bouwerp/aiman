# Architecture review: serve, gateway, TUI, phone

Date: 2026-11-07. Scope: aiman at `2a9112d` (v0.29.2), aiman-phone at `999bc9f`. Four read-only reviews (one per component) plus spot checks of every high finding against the code. Nothing was exercised at runtime; every claim cites the lines read. Paths are repo-relative.

## The shape of the problem

Aiman's trust model has one boundary it enforces well (the phone's device token, deny by default) and three it assumes: that every process on a host is the operator unless it descends from a pty holder, that whatever is on `main` of two GitHub repos is safe to run on every host and phone, and that text arriving from GitHub, JIRA and other sessions is data. Autonomous agents with push rights break all three assumptions. Most of the high findings below are the same fact seen from four sides.

## Security findings, ranked

### S1 (high) Any agent on a host can act on any other session, or as the operator

- `Request.Caller` is a self-asserted JSON field. `isSessionCall` only checks it is non-empty (`internal/server/peer.go:114-117`); ownership checks trust it: `deriveAuthor` makes a call "the agent" when `callerID == targetID` (`handlers_todo.go:69-75`), `callerIs` (`review_approve.go:95-101`), `reviewOwnerCall` (`review_fix.go:110-116`). Session IDs are listed by `session.list`. Session A can write B's to-dos as B's agent, approve B's design, push as B's fixer.
- With no caller, "operator" means "not under a holder" (`peer.go:68-73`). Nothing sets `PR_SET_CHILD_SUBREAPER`, so `( env -u AIMAN_ID aiman todo update <gate> --state done & )` reparents to PID 1 and becomes the operator, defeating `needs_operator` and `gate_needs_operator`.
- `pty.input`, `pty.run`, `pty.kill`, `pty.forget`, `session.prompt`, `session.forget`, `steer.send` (including `mode=answer`, which types into a held permission dialog, `handlers_todo.go:541-551`), `pty.create` (any role, any command, any env; a `reviewer` role is hidden from the dashboard) never call `isSessionCall`. Of 48 dispatched methods, only restart, terminate, the to-do gates and `review.approve` do.

Fix, in one change: give each holder a random per-session credential in its env, have `isSessionCall` resolve the peer to a holder and refuse a `caller` that is not that holder's session, make `aiman pty hold` a subreaper, and restrict every session-targeting method for a session caller to itself and its children. Drop ancestry walking afterwards.

### S2 (high) A paired phone token is a shell on the host

- `pty.input`, `session.prompt`, `steer.send`, `session.create` are plain device write methods (`internal/gateway/allowlist.go:25-32`); the terminal WebSocket has no allowlist at all (`handler.go:35`, `stream.go:105-131`). `allow_destructive` protects `pty.kill` but not typing `exit` and a shell command.
- No scope per device (sessions, groups, read-only), no rate limit on `/v1/rpc`, token not bound to the tailnet login that paired it (`auth.go:47-58`; `Device.Login` recorded, never compared), Funnel allowed by default (`config.go:529-532`).
- `todo.update`/`todo.remove` from a phone close `gate:design`, `gate:review` and `gate:approve` with no step-up (`allowlist.go:28`, `handlers_todo.go:355`).
- Phone side: `pty.input` is used only for dialog chips and clear-input, and prompts confirm when forced (`PromptBox.tsx:14-23`), but restart has no confirmation (`SessionActionsSheet.tsx:173-174`).

Fix: per-device scopes in `gateway-devices.json` enforced in `rpc`, `terminal` and `getSession`; move `pty.input`, `session.create`, `session.restart`, the terminal WS and gate closes behind `allow_destructive` or a new `allow_terminal`/`allow_approve`; bind tokens to login; default `gateway.funnel` false; request budget per token.

### S3 (high) Supply chain: `main` is production on every host and phone

- Serve, trigger and gateway install and auto-update are `curl raw.githubusercontent.com/.../main/install.sh | sh` (`internal/infra/remotesvc/remotesvc.go:81-85`), no checksum, and `features.auto_update_remotes` defaults on (`internal/ui/serve_autoupdate.go:37`). Laptop self-update trusts TLS only (`cmd/aiman/update.go:287-345`). RTK (`rtk.go:16,53`, `master`), Claude (`provisioner.go:15`), nodesource under `sudo -E bash` (`:37`) are the same shape. Agent auto-update is `npm install -g <pkg>@latest` in a window (`internal/agentupdate/catalog.go:154-163`).
- Phone: `eas update --branch main` on every push to `main` (`.github/workflows/ci.yml:57-58`), no `updates.codeSigningCertificate` in `app.json`, `eas-cli@latest` unpinned, and `main` is not protected on either repo (checked with `gh api .../branches/main/protection`).

Fix: pin installers to a release tag, publish signed `checksums.txt` from the release workflow and verify in `install.sh` and `update.go`; make remote auto-update opt-in; agent updates default `notify`; OTA code signing with the key in a secret; protect `main` on both repos; pin `eas-cli`.

### S4 (high) SSH agent forwarding on every remote call

`-A` on every exec, file write and attach (`internal/infra/ssh/manager.go:147,164,225,391,561`) with `ControlPersist=10m` (`:108`), so the laptop's keys are usable from the remote almost continuously, by every agent running as that uid. Fix: drop `-A` from bulk calls; forward only on interactive attach; use a deploy key or `gh auth` on the remote.

### S5 (high) GitHub review comments are typed verbatim into autonomous agents

ciwatch steers `[aiman] New review comment ... by @<author>:\n<body>` (`internal/ciwatch/comments.go:108-109`) and delivery is `TypeAndSubmit` into the composer (`steer_delivery.go:149-158`). No `author_association` check. Anyone who can comment on a watched PR can prompt an agent with push rights. Fix: steer only from collaborators or maintainers, and deliver the body as a file reference.

### S6 (medium) Shell injection through branch names and `%q`

`session.create` validates `name` but not `branch` (`handlers_session.go:713`); paths built from it reach `bash -c 'if [ -d %q ]…'` (`internal/infra/git/manager.go:338,504,931`, `pr_worktree.go:158`). Laptop side: `worktree add -B %s ../%s %s` (`manager.go:377,533,539`) with `SanitizeBranchName` only on the new-branch path. `%q` is used as a shell quoter in about 40 places and nine private `shellQuote` copies exist. Reachable from a phone via `session.create`. Fix: one `shellx.Quote`, `--` before positionals, `git check-ref-format --branch`, reject leading `-`.

### S7 (medium) Secrets spread further than they need to

- `MarshalServeConfig` pushes the JIRA `api_token`, webhook URL and Slack channel to every remote (`internal/infra/config/config.go:303-322`); any agent on the host reads them.
- The laptop `secrets` table is plaintext (`repository.go:154,599`); on restart every row goes into the agent env, written to `/tmp/aiman-pty-<id>.json` with the default umask (`session_terminal.go:111-115`), and persisted in the holder's `request.json` for the life of the session (`ptyhold/contract.go:64-69`, `client.go:45-49`).
- `ServeRPC` params (steer text, handover bodies, context) are base64 on the ssh command line, visible in `ps` on the remote (`serve_rpc.go:55-56`).

Fix: scoped JIRA token or laptop-side proxy; `umask 077` and `~/.aiman/tmp`; pass env to the holder over its socket; pipe RPC params over stdin as `WriteFile` does.

### S8 (medium) The gateway inherits `AIMAN_ID`

`CallRaw` sets `Caller` from the gateway's own env (`internal/server/client.go:196`, `:242`); `main.go` never unsets it and the nohup fallback inherits the environment (`remotesvc.go:153`). A gateway started from inside a session impersonates it: approvals become agent-authored, restart and terminate refuse. Fix: unset at start, refuse to serve from inside a holder tree, pass the caller explicitly.

### S9 (medium) Remote agents can write files onto the laptop

Mutagen `two-way-safe` with `.git` synced on purpose and no symlink or ignore rules (`dashboard.go:1888`, `mutagen/ignore.go:14`, `engine.go:44-55`): `.envrc`, `.vscode/tasks.json`, `.idea`, hooks appear under `~/.aiman/work/<id>`. Fix: one-way remote to laptop by default, `--symlink-mode ignore`, ignore editor and direnv files, document the boundary.

### S10 (medium) Phone pairing and storage

- A scanned QR pairs immediately (`PairFlow.tsx:102-109`) and replaces any server with the same free-text `host` (`secureServers.ts:68-73`); gateway identity is tailnet membership plus name (`tailnet.go:424-432`). A same-named node on the tailnet gets the push token and every prompt. Fix: confirm before redeeming, confirm replacement, record and pin the peer node key.
- Keychain writes use the default accessibility class (`secureServers.ts:49,69,85,102`), so tokens restore to a new device; the tsnet key does not (`TailnetModule.swift:111-113`). Fix: `WHEN_UNLOCKED_THIS_DEVICE_ONLY`.
- The in-app proxy listens on loopback with no proxy-level auth (`tailnet.go:323`); other apps can reach `/v1/pair`. Fix: per-launch secret header.

### Lower

- Review sandbox has network unless `review.sandbox.network: none` (`internal/review/sandbox.go:202-204`); the rest of the sandbox is sound. Default to none.
- Gateway: only `ReadHeaderTimeout` set (`cmd/aiman-gateway/main.go:138-141`); `session.list` returns hidden roles to devices; a dead socket leaks the home path in a 502 (`handler.go:73`); pairing lockout is global and in-memory; `push.register` unscoped; push bodies carry the pane's last line in plaintext.
- `access_token` in the WS URL (`events.ts:85`, `auth.go:118`), unlogged today, unenforced.
- Push deep links accept any in-app route (`deepLink.ts:44-46`).
- Socket chmod after listen (`socket_unix.go:44-50`); dir is 0700 so harmless.
- TUI renders JIRA summaries and PR titles without control-character stripping (`jira_picker.go:17`).
- Phone `npm audit`: 29 findings, all transitive through Expo SDK 54 tooling.
- The Claude statusLine wrapper embeds the original command inline; a `}` in it breaks the status line (`agenthook/statusline.go:65-72`).

## Effectiveness findings

### E1 One flat RPC surface, no capability model

72 methods on one switch (`internal/server/server.go:344-448`), `ProtocolVersion = 1` frozen, "unknown method" as version negotiation, and the gateway allowlist kept in sync by a test that parses `dispatch`. The OpenAPI spec is checked for method names only; no response or event body is validated. Fix: a method registry (`name, handler, who may call, destructive, since`) that dispatch, the gateway, the spec test and a `capabilities` RPC all read.

### E2 Three sources of truth for session state

Laptop sqlite, serve sqlite and live discovery are merged heuristically (`session_merge.go:126-152`, `applyDiscoveryResult` at `dashboard.go:6997` on the UI goroutine); the COALESCE rules and `ClearAgentEnded` exist to survive that. Agent state is a vote between hooks, `pane.Classify`, `TurnOpen` and silence checks; AGENTS.md needs 400 words to state the precedence. Fix: serve's DB is the registry and the laptop caches it; discovery calls `UpdateLive(id, fields)`; hook state is authoritative with a TTL and pane classification is diagnostic.

### E3 The TUI still does host work over raw SSH

Teardown with `rm -rf` and dirty checks (`dashboard.go:1172-1189, 2480-2626`), `pgrep aiman-trigger`, and a trigger rule injected by writing SQL into serve's sqlite with the `sqlite3` CLI (`:6782`). Serve already owns `session.terminate` with safer classification. `Model` has 163 fields, 62 view states, 98 message types and four dispatch layers; `triggerDetailsMsg` is routed only in the main view so the trigger page can show "Fetching…" forever (`:4282`, `:5449`). Fix: move teardown and rules onto RPC, then split page models with their own `Update`.

### E4 The phone reconstructs the conversation from a screen capture

About 2,200 lines of regex over pane text with per-agent rewrites (`lib/transcript.ts`, `paneFormats.ts`, `dialogMenu.ts`, `transcriptProse.ts`). Every agent UI change breaks it silently. Serve already reads vendor JSONL transcripts for usage and holds hook state, titles and the ledger. Fix: a `session.transcript {since}` RPC from the JSONL plus hooks, with the pane as fallback. Also: detail polling is 3s (1s while a prompt is pending) and not gated by visibility (`useSession.ts:11-15`); the list already uses the events WebSocket, the pane does not. The terminal screen is a stub, to-dos are read-only, and there is no usage, notification log, CI toggle or forget.

### E5 Operator gaps

No record of which device did what (the gateway sends no identity, serve's ledger says "operator"); `config.Load` failure is swallowed so serve runs on an empty config (`cmd/aiman/serve.go:67-70`); no config validation, schema version or backup; no version endpoint; no rate limits; event hub drops silently at 64 buffered events (`events.go:29-38`); errors from remote auto-update surface only in the console log.

## What is done well

Deny-by-default device allowlist pinned by tests; constant-time token compares, sha256 at rest, atomic 0600 writes everywhere; pairing bound to WhoIs and refused on Funnel; every state directory 0700; prompts, params and task files kept out of argv; `readConfined` for design publishes and the docs-repo visibility gate; limits sources that never leak a key, header or URL, with the proxy disabled; the Grok probe env allowlist; the Docker sandbox shape (read-only root, no capabilities, pids and memory limits, loopback-only debug ports); `WriteFile` refusing shell rc files; the phone's pure tailnet resolver with fake-tailnet tests, single-shot non-idempotent calls, and pane re-read before any byte is typed.

## Suggested order

1. **This week, small diffs:** drop `-A` from bulk SSH; unset `AIMAN_ID` in the gateway; gate `pty.*`, `session.prompt`, `steer.send`, `session.forget` to the caller's own tree; move `pty.input`, `session.create`, restart and the terminal WS behind `allow_destructive`; pin `install.sh` to the release tag and add checksums; OTA code signing and branch protection; `author_association` on ciwatch; branch validation and one `shellQuote`; sandbox network default `none`; `umask 077` for the pty params file.
2. **Next:** per-session holder credential replacing ancestry; device scopes and login binding; the method registry with a `capabilities` RPC; env over the holder socket instead of `request.json`; scoped JIRA token.
3. **Then:** serve-authoritative session state with `UpdateLive`; `session.transcript` for the phone; TUI teardown onto RPC and page-model split.

## Unverified

Whether `systemd --user` is a subreaper on the hosts (S1 stands either way); mutagen's symlink defaults; whether `/tmp` on the remotes is shared with other uids; exploitability of the transitive phone audit findings; runtime behaviour of the dropped `triggerDetailsMsg`; the exact `expo-secure-store` default class on this SDK.
