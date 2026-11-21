# harness-share

As built. Design: the approved design page (rev 3), copy in `.aiman_design.md` of the coordinator session `app-debug-harness`, and `docs/specs/harness-share-design.md`.

## Overview

An agent that has a `ready` harness run runs `aiman harness share`. Serve prints `<scheme>://aiman-gateway-<host>:8090/s/<id>` (`https` and the node's cert domain when the tailnet has cert domains, else `http` and the short hostname). The operator opens it on a laptop on the tailnet. `aiman-gateway` checks the peer's login against `gateway.console_logins`, asks serve whether the share is live, sets a visit token and serves a web console pinned to that run, with a reduced method set (no `harness.up`, no `harness.down`) and forwards for the run's app ports. The tester's verdicts reach the agent as `[aiman] tester <login> marked harness scenario ...`. A share ends with the run, at `harness.unshare`, or at `harness.share_ttl` (default 12h).

## P1 skill and installer (`internal/aimanskill`, `internal/infra/skills`, `internal/worktree`)

- `internal/aimanskill/harness/SKILL.md` is `aimanskill.HarnessSkillText`. Frontmatter `name: aiman-harness`; the body names `test "${AIMAN_ENV:-}" = 1`, `aiman harness list --dirty`, `aiman harness up`, `aiman harness check`, `aiman harness share` and `harness.md`.
- `type Skill struct { Name string; Files []BundleFile }` and `Skills()` return, in order, `{aiman, Bundle()}` and `{aiman-harness, [SKILL.md, harness.md]}`. `Bundle()` is still the aiman skill's three files.
- `EnsureOnHost(home, roots)` installs every skill's files into `<agent dir>/skills/<skill name>/` for each user and project agent dir. `IsProjectSkillPath(rel)` is true for every skill's files and false for a skill that is not bundled. `UserSkillFiles` and `ProjectSkillFiles` stay the aiman skill's `SKILL.md` paths; `UserSkillDirs(home, skill)` and `ProjectSkillDirs(root, skill)` give the directories of any skill.
- `internal/infra/skills/engine.go` pushes every skill's files to a session worktree.
- The aiman `SKILL.md` `## Harness` section names the `aiman-harness` skill. `harness.md` belongs to both skills and has a `## Sharing a run` section for agents.
- `cmd/aiman/serve.go` (`ensureAimanSkill`) logs one `aiman skill:` line whose counts cover both skills (`aimanskill.Summarize`).

## P2 share in serve (`internal/server/handlers_harness_share.go`, `internal/infra/sqlite/harness_shares.go`, `internal/infra/config`)

- Sqlite tables `harness_shares (id, session, run, created_at, cap_at, revoked_at)` and `gateway_announce` (one row, `id = 1`: `base`, `console_port`, `scheme`, `pid`, `at`). Methods: `SaveHarnessShare`, `GetHarnessShare` (a missing id is `sqlite.ErrShareNotFound`), `ListLiveHarnessShares(session)`, `RevokeHarnessShares(session, at)`, `SaveGatewayAnnounce`, `GetGatewayAnnounce`. Serve's `revokeShares` uses `ListLiveHarnessShares` to name the ended shares for the hub; the gateway does not read the table. A serve whose repository lacks these methods answers `unavailable` to all four RPCs.
- RPCs (`dispatchHarnessShare`):
  - `gateway.announce {base, console_port, scheme, pid}` answers `{"type":"gateway_announced"}`. A session caller is `needs_operator`. `base` must be `scheme://host` with no port, path, query or credentials, `scheme` `http` or `https` matching it, `console_port` 1-65535, else `invalid_params`. It is accepted even when the harness is disabled.
  - `harness.share {session?, run?}` answers `{"type":"harness_share","share":<id>,"run":<run>,"url":"<base>:<console_port>/s/<id>"}`. The id is 32 random bytes, base64url (43 characters). A session with no `ready` run, or a `run` that is not that run, is `harness_not_running`. No announce, or one older than `GatewayAnnounceMaxAge` (3 minutes), is `unavailable` with `gateway is not running`. A new share first revokes the session's earlier ones (hub `harness_share ended` for each). `CapAt` is now plus `harness.share_ttl` (`config.DefaultHarnessShareTTL`, 12h). It records `harness.shared` (session, run, share). A session caller is scope-checked as for other harness calls. With the harness off it is `unavailable`.
  - `harness.unshare {session?}` answers `{"type":"harness_unshared","revoked":n}` and publishes `harness_share ended` for each live share.
  - `harness.share_get {id}` answers `{"type":"harness_share_state","share","session","run","state":"live"|"ended"}`. `ended` means revoked, past `cap_at`, or the run is gone or not `ready`. A session caller is `needs_operator`; an empty id is `invalid_params`; an unknown id is `not_found`. The first time it sees a share ended (and the hub has not already said so) it publishes `harness_share ended`.
- Hub event `harness_share {id, state}` on share (`live`), unshare, revoke and the first ended `share_get`.
- `harness.verdict` takes `tester` (control characters and spaces removed, at most 200 bytes). With it the verdict's `by` is `tester:<login>` and the steering note starts `[aiman] tester <login> marked harness scenario ...`; without it `by` is `operator`.
- Config: `gateway.console_logins` (`config.GatewayConfig.ConsoleLogins`) and `harness.share_ttl` (a Go duration; `HarnessConfig.ShareTTLOrDefault`); `MarshalServeConfig` pushes both, `cmd/aiman/harness_serve.go` passes the TTL to `HarnessDeps.ShareTTL`.
- CLI: `aiman harness share [--session NAME] [--json]` prints the URL, then `run <id>; earlier links of this session no longer work`. `aiman harness unshare` prints `revoked <n> share link(s)`.
- The four methods are in `api/openapi.yaml`, with the `harness_share` event, and off the gateway device allowlist (`internal/gateway/allowlist_test.go`).

## P3 console remote mode (`internal/console`)

- `Options` has `Hosts []string` (accepted `Host` names without port), `Port int`, `HTTPS bool` and `Methods []string` (serve methods the console may call; nil is all).
- `New(opts, nil)` with `Hosts` set returns a server with no listener; `Handler()` serves it. A non-loopback listener without `Hosts` still fails.
- With `Hosts`, `hostOK` accepts `<h>:<Port>` (and bare `<h>` on 443 with HTTPS or 80 without), `originOK` accepts `<scheme>://<h>:<Port>`, and the CSP `connect-src` lists `wss://<h>:<Port>` (HTTPS) or `ws://<h>:<Port>`. The app links use `Hosts[0]`.
- A call to a method outside `Methods` is 403 `method_not_allowed` and never reaches `RPC`; `harness.status` is gated too. `/api/state` carries `shared: true`, and the page then hides up, down and the editor launch entry.
- `console.HostForwarder` opens nothing: its forwards have `LocalPort == HostPort` and never die. A shared console's `Serve` only polls, and must still run.

## P4 gateway console and forwards (`internal/gateway/console*.go`, `cmd/aiman-gateway/console.go`)

- Types: `Peer{Login, NodeID}`, `PeerWhoIs`, `ConsoleOptions{Logins, Hosts, Port, HTTPS, WhoIs, HostNodes, RPC, Listen, Dial, Log}`, `NewConsoleShares` (an error when `Logins`, `Hosts`, `WhoIs`, `HostNodes` or `RPC` is missing or `Port` is not 1-65535), `ConsoleShares` with `Handler`, `ShareEvent`, `PollOnce`, `Run` and `Close`, `AnnounceParams` and `Announce`, `RunAnnounce`, `WatchShareEvents`, `SocketRPC` (serve's socket as the operator).
- Peer check, on every request (`/`, static files, `/api/*`, `/s/*`): `WhoIs` succeeds, `NodeID` is non-empty and not in `HostNodes()`, and `Login` is in `Logins` ignoring case. Any error fails closed with 403 and calls nothing on serve. The log names the method and path, with `/s/(id)` for share links, never a token.
- `GET /s/<id>` (a 43-character id, else 404): `harness.share_get` with no cache. `live` mints a visit token (32 random bytes, base64url) bound to the share, login and node, valid 12h from last use (`VisitTokenTTL`), and answers 302 to `/#token=<token>`. `ended` or `not_found` is 404; another serve error is 502 `serve_failed`. The gateway knows a share only after someone opened `/s/<id>` since it started.
- `/api/*` and the `/api/dap` WebSocket carry the visit token as the console does (`X-Aiman-Console-Token`, or the `aiman-token.<token>` subprotocol). An unknown or expired token, a token of an ended share, or one presented by another login or node is 401 `bad_token`. The call goes to the share's `console.Server`, made on first use (`consoleFor`) with `Hosts`, `Port`, `HTTPS`, `Run` the share's run, `HostForwarder`, its own random token (the gateway swaps the visit token for it) and `Methods` `TesterMethods`: `harness.status`, `list`, `logs`, `capture`, `check`, `verdict`, `debug`, `files`, `file`, `db`, `evm_txs`, `evm_trace`, `aws`, `aws_calls`, `aws_write`. `consoleFor` waits up to 5s (`consoleReadyWait`) for the console's first poll before serving, since the console has no ready hook. The console's own Host, Origin and CSP checks stay on.
- The console's `RPC` is `testerRPC`: every `harness.verdict` carries `tester: <login>` of the visiting peer, and a verdict with no login is refused.
- Ending a share (a `harness_share ended` event, a poll, `Close`) drops its visit tokens and closes its console, its debugger bridges and its forwards at once. `PollOnce` runs every 10s (`SharePollEvery`): one `harness.share_get` per known share (`ended` or `not_found` ends it), then `harness.status {run}` to open or close forwards. `WatchShareEvents` follows `session.events` for `harness_share` and polls once after each reconnect. A gateway restart forgets every share and visit token; the visitor opens the link again.
- App forwards: for a live share whose run is `ready`, the gateway listens on the tailnet at the host port of each run port whose `service` is `app` and whose name is not `dap` (relays for db, evm, aws and the debugger are never forwarded), except 8080, 8090 and the console port. Each connection passes the peer check and needs a live visit token of the same login for that share, else it is closed without a dial; admitted ones are proxied to `127.0.0.1:<host port>`. The forwards close when the run is not `ready` or the share ends. Because they need the visit token, app links work only after the share link was opened in that browser.
- `cmd/aiman-gateway/console.go`: the console starts only when `gateway.console_logins` is non-empty and Funnel is off (under Funnel it logs and stays off). It listens at `:8090` with `ListenTLS` when `CertDomains()` is non-empty (scheme `https`, base `https://<first cert domain>`), else plain (`http://<hostname>`). `Hosts` are the short hostname, every cert domain, then the node's tailnet IPs. `HostNodes` is the tsnet node's `Self.ID` plus the system tailscaled's (default socket path) when it answers, cached 30s; a failed tsnet read refuses. `PeerWhoIs` is `LocalClient.WhoIs` (`Node.StableID`, login name). It announces at start and every minute (`AnnounceEvery`), subscribes to events and runs `Run`; failures are logged once per distinct error.

## Deviations from the approved design

Design: `.aiman_design.md` rev 3. Surprises, in the order found:

- `cmd/aiman/serve.go` logs skill counts that cover both skills, so the `aiman skill:` line changed.
- `gateway.announce` is accepted even when the harness is disabled; only `harness.share`, `unshare` and `share_get` need it.
- `ListLiveHarnessShares` is a sqlite method the design did not list. Serve uses it to publish `ended` for each revoked share; the gateway learns of shares from `/s/<id>`, events and its poll instead.
- A shared console waits up to 5s for its first poll before serving (`consoleFor`), because the console has no ready hook.
- The gateway knows a share only after someone opened `/s/<id>` since it started. A gateway restart forgets shares and visit tokens, so the visitor opens the link again; an app link opened before the share link finds no visit token.
- App links work only after the share link was opened in that browser, because forwards need the visit token of the same login.
- Forwards skip 8080, 8090 and the console port only; no other host port is excluded.
- The system tailscaled host-node check uses the default socket path; a tailscaled on another socket is not seen, so its node is not refused.
- An empty `gateway.console_logins` turns the console off; the gateway never falls back to `allow_logins`.
- The tester's method set leaves out `harness.down` as well as `harness.up` (the design listed `down` in the set and then excluded it); the agent owns the run's lifecycle.
- Wave 1 (as built earlier): the announce goes stale after 3 minutes, so the gateway announces every minute; a shared console's app links use `Hosts[0]`; `Methods` gates `harness.status` too.
