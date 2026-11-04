# aiman mobile app: design and plan

Status: draft for review, 2026-11-03.

## Goal

From an iPhone: see which sessions need me, answer them, steer them, and approve
design and review gates, without opening a laptop.

## Where things stand

- `bouwerp/aiman-phone` exists (2 commits, 2026-08-28). It was scaffolded from
  `bouwerp/laughly`: Expo 54, RN 0.81, expo-router 6, NativeWind 4, react-query 5,
  expo-secure-store, expo-notifications, oxlint, Jest, Detox config only. It has
  pairing, multiple servers, session list and detail, a prompt box, push
  registration, a mock gateway and CI. It has never run on a device and has no
  EAS `projectId`.
- `aiman-gateway` (this repo, `cmd/aiman-gateway`, `internal/gateway`) runs per
  host as a systemd user unit and joins the tailnet with tsnet. It serves
  `POST /v1/rpc` (any serve method), `GET /v1/sessions`, WebSocket
  `/v1/events` and `/v1/sessions/{id}/terminal`, and pushes `notify.since`
  entries to Expo. It is installed here but `inactive`.
- Laughly's backend (Cognito, API Gateway, Lambda, Postgres) has no
  counterpart: aiman's state lives on each host's serve, not in a cloud
  database.

## Gaps between the app and the gateway today

1. WebSocket auth: the app sends `?access_token=`; the gateway reads only the
   `Authorization` header, so live events and terminal fail.
2. `api/openapi.yaml` lists 7 methods and 3 event types. serve now has to-dos,
   steering, gates, review, usage, `notify.since` and 9 event types.
3. One shared token per host, no per-device revoke, and `/v1/rpc` forwards
   every method (including `pty.kill`, `session.forget`) as the operator.
4. Push payloads and the deep link `aimanphone://session/<id>` carry no host.
5. Expo push receipts and `DeviceNotRegistered` are not handled; dead tokens
   are never pruned.
6. Pairing means reading `~/.aiman/gateway-token` off the host by hand; there is
   no QR.
7. The app's mock implements a `session.prompt` rule that differs from live
   serve; the app must follow serve.

## Connectivity decision

| Option | Phone needs | New infra | Fit |
|---|---|---|---|
| A. Tailnet (built) | Tailscale app on, signed into the tailnet | none | Private, per-login identity via `WhoIs`; iOS allows one VPN at a time, so it clashes with a work VPN |
| B. Tailscale Funnel (built, `--funnel`) | nothing | none | Public `*.ts.net` URL; the token is the only lock, so gaps 3 and 5 must close first |
| C. AWS relay | nothing | API Gateway WebSocket + Lambda + DynamoDB + Cognito, hosts dial out | One endpoint for all hosts, real user sign-in, store-and-forward; terminal streaming fits poorly (frame limits, 2h connections, per-message cost) and it is a service to run |
| D. tsnet in the app (unproven) | nothing; the app is its own tailnet node | none | No iOS VPN slot used, so a work VPN can stay on; per-phone identity via `WhoIs`; needs a native module, adds the Go runtime to the binary, connected only while the app is in the foreground |

D rests on one verified fact: `tailscale.com/tsnet` cross-compiles to
`ios/arm64` with and without CGO (2026-08-26 gateway prompt). Nothing has been
linked into an app or run on a phone. React Native's `fetch` and `WebSocket`
cannot use the userspace stack, so the module either makes the requests itself
or runs a localhost proxy that `services/api.ts` and `services/events.ts` call.
Push is unaffected: Expo delivers it without the tailnet.

Spike results on Linux, 2026-11-03 (`aiman-phone` branch `spike/tsnet-in-app`,
`modules/tailnet/go`): the bridge serves a localhost reverse proxy that dials
the gateway with tsnet, and `GET /v1/health` through it returns 200.
Restarting from saved state, `Up` returns within 25 ms, before the gateway has
the node's new path, and the first dial then hangs for 2m15s (2 of 3 runs). With
`Start` waiting for a TSMP ping to the gateway, 5 of 5 restarts were ready in
1.3 to 1.7 s, with the first byte in 3 to 4 ms. A stripped Linux build of the
bridge is about 21 MB.

On the iPhone (same day, Tailscale app off, Expo dev build): `Start` was up in
20 ms, `GET /v1/health` through the bridge returned `200 {"ok":true}` with the
first byte in 35 ms, and after a minute in the background the check passed
again on resume. The node joined with an untagged auth key, so the gateway sees
the owner's login. Not measured: the framework's share of the app size, and
coexistence with another VPN (none on this phone). D works and needs no VPN
slot, so it is the planned daily path; A stays as a fallback.

Recommendation: D for daily use (proven on the iPhone, see below), A as the
fallback, B only once the gateway is hardened. Keep the app's transport behind one interface (`services/api.ts`,
`services/events.ts`) so C can be added later without touching screens. Build C
only if a VPN on the phone is a deal-breaker, or if cross-host aggregation and
offline history become needs. Push needs no AWS in any option: the gateway
calls Expo directly.

## Plan

### Phase 0: gateway hardening (this repo)

Contract shared by the gateway, the TUI and the app:

- Node name: `--hostname` defaults to `aiman-gateway-<host>`, `<host>` being
  the OS hostname lowercased to `[a-z0-9-]`, so several remotes on one
  tailnet do not collide. The app's tsnet target is `<node name>:<port>`.
- `aiman-gateway pair [--name <device>] [--json]` (runs on the host, beside the
  service) mints a pairing code: 10 random base32 characters, single use, valid
  5 minutes, stored as a sha256 in `~/.aiman/gateway-pair-codes.json` (0600,
  written atomically). `--json` prints
  `{"target","host","code","expires_at","uri"}` where `uri` is
  `aimanphone://pair?target=<t>&host=<h>&code=<c>`, the QR payload.
- `POST /v1/pair {code, device_name}` needs no bearer token but still the
  tailnet identity check. It consumes the code and returns
  `{device_id, token}`. Tokens are 32 random bytes as hex, stored only as a
  sha256 in `~/.aiman/gateway-devices.json` (0600) with name, created_at,
  last_seen, login and `allow_destructive` (false). More than 5 failed codes
  in a minute locks pairing for that minute.
- Auth: `Authorization: Bearer` with the host token (full access, the TUI) or a
  device token. The two WebSocket routes also accept `access_token` in the
  query; URLs with it are never logged.
- `GET /v1/devices` lists devices (no hashes); `DELETE /v1/devices/{id}`
  revokes. `aiman-gateway devices [list|revoke <id>|allow-destructive <id>
  on|off]` does the same on the host.
- Device tokens pass a method allowlist on `/v1/rpc`, derived from serve's
  handler table, deny by default. Allowed: reads (`session.list/get/read/
  wait/log`, `pty.capture`, `todo.get/summary`, `notify.since`, `usage.get`,
  `ci.get`, `agents.status`, `repo.list`, `host.stats`, `ping`), and writes (`session.prompt`,
  `session.create`, `session.rename`, `session.move`, `session.restart`,
  `session.terminate`, `steer.send`, every `todo.*` including gate approval,
  `ci.watch`, `pty.input`, `push.register/unregister`). Destructive
  (`pty.kill`, `pty.forget`, `session.forget`, `review.post`, `agents.update`,
  `agents.move_prefix`) need `allow_destructive`. A device acts as the
  operator.
- Push `data`: `{session, host, url}` with `url`
  `aimanphone://h/<host>/s/<session>`.

Steps:

1. Gateway (`cmd/aiman-gateway`, `internal/gateway`): node name, pairing codes
   and `/v1/pair`, device store and auth, allowlist, `/v1/devices`, WebSocket
   query token, push `data`, Expo errors (per-ticket `DeviceNotRegistered`
   drops the token at send time; receipts polled every 15 minutes for tickets
   older than 15 minutes; a non-2xx reply is a real error).
2. TUI: Agent API page `p` runs `aiman-gateway pair --json` on the selected
   remote over SSH (starting the gateway service first when it is not
   running) and draws `uri` as a QR in the terminal with the code and expiry
   beside it.
3. `api/openapi.yaml`: the new routes and every serve method and event type,
   with a test that fails when a handled method or published event type is
   missing from the spec.
4. AGENTS.md, README.

The QR carries no Tailscale key. The app joins the tailnet itself in Phase 1
(see Phase 1 step 3).

### Phase 1: on a device (aiman-phone)

1. EAS project and `projectId`, development build, install on the iPhone.
2. Regenerate types from the new spec; align the mock and `promptPolicy` with
   live serve.
3. QR pairing with `expo-camera`, signing in to Tailscale through the browser:
   - The app scans `aimanphone://pair?target&host&code` and starts tsnet with
     no auth key. The module exposes the login URL tsnet reports (from the IPN
     bus `BrowseToURL`) and the node state.
   - The app opens that URL in an in-app browser sheet; the user signs in with
     their Tailscale identity provider and approves the device.
   - Tailscale's login page does not redirect to a custom scheme, so the app
     watches the node state and closes the sheet itself once it is `Running`.
   - It then redeems the code with `POST /v1/pair` over the tailnet and keeps
     the device token in SecureStore. tsnet state stays in the app container,
     so later launches skip the sign-in.
   - The node is owned by the user's login, so `WhoIs` and
     `gateway.allow_logins` work. When the node key expires (180 days by
     default) the same sheet appears again; key expiry can be disabled for the
     phone in the admin console.
   - No auth key is minted or stored anywhere: Tailscale only mints keys
     through an API client, and those must be tagged, which breaks the login
     check.
4. Deep link by host and session; reconnect refetches the list (events can be
   dropped, there is no replay).
5. End-to-end check against this host's gateway over the tailnet.
6. Spike for option D: a bare Expo native module (`gomobile bind` or
   `libtailscale`) that starts tsnet with an ephemeral pre-auth key, keeps its
   state in the app container, and fetches `GET /v1/health` from this host's
   gateway on a real iPhone with the Tailscale app off. Record binary size
   growth, cold-start time to first byte, and what happens after the app is
   suspended and resumed. Then decide between a module that makes the requests
   and a localhost proxy, and how a phone gets its auth key (pairing QR or
   in-app login).

### Phase 2: aiman features

1. Needs-you list: `waiting_input`, `dialog`, CI escalations, open gates first.
2. Session detail: pane text (`pty.capture` with `have_hash`), to-do list
   (`todo.get`, live on `todo_changed`), steering note and answer
   (`steer.send`).
3. Gate approval: design and review gates, with a link to the published design
   page.
4. Notification history from `notify.since`.
5. Usage: `usage.get by:issue` for the last 30 days.
6. Terminal attach: xterm.js in a WebView over the terminal socket.

### Phase 2b: run sessions and the host from the phone

serve today, for each need:

| Need | serve today | Work |
|---|---|---|
| New session | `session.create` exists; agents from `agents.status` | New `repo.list` (the `git.Manager.ListRepos` result, cached; `gh` is slow) for the picker |
| Rename, group | `session.rename`, `session.move` exist | Add both to the device allowlist |
| Restart with another agent | None: the flow lives in the TUI (`restartTerminal`, `RecreatePTYSession`) | New `session.restart {session, agent, model, effort}` that writes the handover with the target agent, recreates the PTY and delivers the handoff prompt. The TUI keeps its flow for now |
| Terminate | None: teardown lives in the TUI; `pty.kill` and `session.forget` alone leave the worktree behind | New `session.terminate {session, discard}`: forget handover, stop the terminal, remove a clean worktree, forget the row. Refuses with `worktree_dirty` (listing what is dirty or unpushed) unless `discard` is true. Laptop-only steps (mutagen, snapshot) stay in the TUI |
| Host overview | None | New read-only `host.stats`: uptime, load, CPU, memory, swap, disk, top processes by CPU and memory, and each session's agent process tree mapped to its session. Reads `/proc` on Linux and reports only what it has elsewhere |

Each new method lands in `api/openapi.yaml` and the allowlist. The app then gets
a New session flow (repo, branch or ad hoc, agent and model, prompt), a session
actions sheet (rename, move to group, restart with agent, terminate) and a Host
tab refreshing every few seconds while visible. Phone actions need a serve
release first, so the app says "update serve" when serve answers
`invalid_params` with `unknown method <name>`, or an older gateway answers
`forbidden`. There is no `method_not_found` code, and an old serve could not send
one.

`session.create` already takes what the New session flow sends: `repo`
(`owner/name` from `repo.list`), `branch` (with `existing_branch` and `base`),
or `quick`; `agent` with `model` and `effort`, or `tier`; `prompt`; `name`,
`group`, `dir`, `issue`. `api/openapi.yaml` documents each.

### Phase 3 (only if chosen): AWS relay

Hosts open an outbound WebSocket to an API Gateway WebSocket API; Lambda routes
phone requests to the host connection by host id (DynamoDB connection table);
Cognito signs the phone in. Infra in one CDK app with OIDC deploys from CI,
using laughly's `backend-deploy.yml` pattern (path filters, OIDC role, smoke
test) and not its CDK plus Terraform split.

## From laughly: keep and drop

Keep: Expo Router groups with a guard in the root layout, react-query, OpenAPI
generated types, ports-and-adapters split, OIDC deploys, path-filtered CI.
Drop: tokens in AsyncStorage (SecureStore only), config values committed in
`eas.json` (EAS environment variables instead), committed build artifacts, no
`expo-updates` (add it with a `runtimeVersion` policy for OTA).

## Decisions

Settled 2026-11-03: iPhone only; a device may create sessions.

Settled 2026-11-04: any paired phone may terminate a session, with confirmation
in the app and no `allow_destructive`. Dirty work needs an explicit discard:
`session.terminate` refuses with `worktree_dirty` until `discard` is true. The
host view is read-only.
