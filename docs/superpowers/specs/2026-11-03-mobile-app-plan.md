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
bridge is about 21 MB; the iOS framework size, suspend and resume, and running
beside a work VPN are still to be measured on the iPhone.

Recommendation: ship on A, with B as the no-VPN fallback once the gateway is
hardened. If the Phase 1 spike proves D, it replaces A and B for daily use. Keep the app's transport behind one interface (`services/api.ts`,
`services/events.ts`) so C can be added later without touching screens. Build C
only if a VPN on the phone is a deal-breaker, or if cross-host aggregation and
offline history become needs. Push needs no AWS in any option: the gateway
calls Expo directly.

## Plan

### Phase 0: gateway hardening (this repo)

1. WebSocket auth: accept the token from `Sec-WebSocket-Protocol` (and the
   `access_token` query for the existing app), constant-time compared like the
   header. Tests in `internal/gateway`.
2. Per-device tokens: `POST /v1/pair` exchanges a short-lived pairing code for
   a device token stored hashed in `~/.aiman/gateway-devices.json`; `GET/DELETE
   /v1/devices` lists and revokes. The host token stays for the TUI.
3. RPC allowlist for device tokens: read methods, `session.prompt`,
   `steer.send`, `todo.*` (operator gate closes included), `notify.since`,
   `usage.get`, `session.create`. Destructive methods (`pty.kill`,
   `session.forget`, `review.post`) need an explicit `allow_destructive` on
   the device.
4. Host id in push `data` and the deep link: `aimanphone://h/<host>/s/<id>`.
5. Expo receipts: poll `getReceipts`, drop `DeviceNotRegistered` tokens, return
   a real error on non-2xx.
6. Pairing in the TUI: Agent API page gains `p`, which starts the gateway if
   needed, mints a pairing code (5 minutes) and draws a QR of
   `{url, code, host}` in the terminal.
7. Refresh `api/openapi.yaml` from serve's handler table, with a test that
   fails when a handled method or event type is missing from the spec.
8. AGENTS.md, README.

### Phase 1: on a device (aiman-phone)

1. EAS project and `projectId`, development build, install on the iPhone.
2. Regenerate types from the new spec; align the mock and `promptPolicy` with
   live serve.
3. QR pairing with `expo-camera` against the `/v1/pair` flow; device token in
   SecureStore.
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

## Open questions

1. Does the phone already run a VPN that would block Tailscale (decides A vs B
   for daily use until the D spike reports)?
2. iPhone only, or Android too?
3. Should a device token be able to create sessions, or only answer and steer?
