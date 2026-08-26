# Task: a network-facing gateway for the aiman agent API

You are working in `/Users/pieter/code/aiman` (Go 1.26, module
`github.com/bouwerp/aiman`). Read `CLAUDE.md`, `AGENTS.md` and `PLAN.md` before
you start; they carry rules that override your defaults.

## What this is for

A React Native / Expo phone app (modelled on `bouwerp/laughly`) will show aiman
sessions, their live state, their output, and let the user reply to an agent that
is blocked. That app cannot reach aiman today: `aiman serve` listens on a **unix
socket only** (`internal/server/socket_unix.go`), and React Native can open
neither a unix socket nor a raw line-protocol TCP stream. It has `fetch` and a
global `WebSocket`.

Your job is the Go side: make the agent API reachable over the network, safely,
without putting Tailscale's dependency tree into the `aiman` binary.

**You are not building the app.** Do not write TypeScript.

## Design already decided — do not redesign these

1. **The gateway is a proxy, not a second server.** It speaks HTTP + WebSocket
   to the phone and the *existing* line-delimited JSON protocol to
   `aiman serve`'s unix socket. `internal/server` and `cmd/aiman/serve.go` need
   no changes to their protocol, and the gateway needs no database, flow manager
   or JIRA provider. One implementation of every method stays in one place.

2. **A separate binary carries tsnet.** `cmd/aiman-gateway` (name it that).
   `cmd/aiman` must never import `tailscale.com/...`. A tsnet hello-world binary
   is 27.4 MB against aiman's ~24 MB, and pulls ~236 modules; keeping it out of
   the main binary is the whole point of splitting it.

3. **Auth is a bearer token always, plus Tailscale identity when available.**
   - Every request carries `Authorization: Bearer <token>`. Compare in constant
     time. The token lives in `~/.aiman/gateway-token` on the remote (0600),
     generated on first start if absent.
   - When the connection arrived over the tailnet, additionally call
     `srv.LocalClient().WhoIs(ctx, remoteAddr)` and require
     `who.UserProfile.LoginName` to match an allow-list from config. Reject
     otherwise. This is free, strong authentication and no token can substitute
     for it.
   - Both checks, not either. The token is what makes Funnel possible at all;
     WhoIs is what makes the tailnet path trustworthy.

4. **Tailnet by default, Funnel opt-in.** Default `Listen` (tailnet only).
   `--funnel` switches to `ListenFunnel` and must log a clear warning: Funnel is
   public and anonymous, so the token is the *only* thing between the internet
   and a shell on the box.

5. **Lifecycle goes through `internal/infra/remotesvc`** as a third `Kind`
   alongside serve and trigger, so install / start / stop / probe / update and
   the client's existing auto-update all work for it unchanged.

## Verified facts — trust these, do not re-derive

Established by testing on this machine and against the real remote:

- `tsnet.Server` methods: `Listen(network, addr)` tailnet-only;
  `ListenTLS(network, addr)`; `ListenFunnel(network, addr, opts...)` public on
  TCP 443 / 8443 / 10000. Auth precedence: `AuthKey` field, then `TS_AUTHKEY`,
  then `TS_AUTH_KEY`, then interactive login URL. No root, no system daemon —
  it is a userspace TCP/IP stack.
- `Server.LocalClient()` returns a client with `WhoIs(ctx, remoteAddr)`, giving
  `who.UserProfile.LoginName` and `who.Node.ComputedName`. Usable to
  authenticate connections on a `Listen` (tailnet) listener.
- **Funnel supplies no identity whatsoever.** Public traffic is anonymous to the
  service; there are no `Tailscale-User-*` headers for Funnel traffic (those
  exist for tailnet `serve`, not Funnel).
- `tailscale.com/tsnet` cross-compiles cleanly to `linux/amd64` and to
  `ios/arm64` (CGO on and off, against the Xcode 26.2 iOS SDK).

## Current shape of what you are proxying

`internal/server`:

- `Listener` embeds `*net.UnixListener`; `Server.Serve(ctx)` loops
  `s.ln.Accept()`; `handleConn(ctx, conn net.Conn)` reads newline-delimited
  requests and writes newline-delimited responses.
- Request/response types and error codes are in `internal/server` — reuse them,
  do not restate the wire format.
- **Two methods take over the connection** and are not request/response. Any
  transport you add must preserve this:
  - `pty.attach` — one JSON response, then raw terminal bytes both ways, with
    framed client messages for input and resize (`attach_framing.go`).
  - `session.events` — one JSON response, then a stream of newline-delimited
    `SessionEvent` lines until the client goes away.
- Client helpers already exist and are the right thing to reuse for talking to
  the socket: `Call`, `CallRaw`, `AttachDial`, `EventsDial` in
  `internal/server/client.go`.
- Method names are the source of truth for your routes: `ping`, `session.list`,
  `session.get`, `session.read`, `session.prompt`, `session.wait`,
  `session.create`, `session.rename`, `session.move`,
  `session.report_agent_session`, `context.*`, `pty.*`.

`internal/infra/remotesvc`: `Kind` drives `Unit`, `Binary`, `ExecLine`,
`LogFile`, `PidFile`, `procPattern`, `InstallPipe`, and the
Install/Start/Stop/Update/Probe scripts plus `ParseProbe`. The serve unit sets
`KillMode=process`; read the comment there before copying it — the gateway owns
no child processes that must outlive it, so it should keep the default.

`install.sh` already resolves `BINARY_NAME`, and
`.github/workflows/release.yml` builds `aiman` and `aiman-trigger` per platform
from a matrix. Add the third binary there the same way.

## What to build

1. **HTTP + WebSocket gateway** (`internal/gateway`, so it is unit-testable
   without tsnet):
   - `POST /v1/rpc` taking `{"method": "...", "params": {...}}` and returning the
     socket's response verbatim. One endpoint keeps the surface honest: the
     method list is already the API.
   - Optionally, thin REST conveniences over the common reads
     (`GET /v1/sessions`, `GET /v1/sessions/{id}`) if it costs little. Do not
     invent semantics the socket does not have.
   - `GET /v1/events` (WebSocket) bridging `session.events`.
   - `GET /v1/sessions/{id}/terminal` (WebSocket) bridging `pty.attach`,
     including resize messages.
   - `GET /v1/health` — unauthenticated, no information beyond liveness.
   - Auth middleware as described above.
2. **`cmd/aiman-gateway`**: flags for `--funnel`, `--hostname`, `--port`,
   `--allow-login` (repeatable), `--socket`. Reads `TS_AUTHKEY`. Prints the URL
   it is reachable on, and the token's location (never the token itself).
3. **`remotesvc` third kind** with its unit file, scripts and probe parsing.
4. **Config**: an allow-list of Tailscale logins, and whether Funnel is
   permitted. Follow the existing `FeatureFlags` pattern — a pointer when absent
   must mean "on".

## Rules you must follow

From `CLAUDE.md`, and they are not optional:

- **Every change ships with unit tests**, a compile check, and updates to
  `PLAN.md` and `README.md`.
- Source comments describe the code **as it stands** — why it is this way,
  constraints, gotchas. Migration narrative ("used to", "previously") belongs in
  the commit message, never in the source.
- Autonomous loop: run the checks yourself and iterate to green. Stop and report
  after three failed attempts at the same thing.

Verification gate, all of it, before you call anything done:

```
go build ./...
GOOS=windows GOARCH=amd64 go build ./...   # aiman must still cross-compile
go vet ./...
go test -race ./...
make lint                                   # must report 0 issues
```

Plus a check that matters here: **prove `cmd/aiman` did not gain the Tailscale
tree.** `go list -deps ./cmd/aiman | grep tailscale.com` must be empty, and
assert it in a test so a stray import cannot slip in later.

## Traps this codebase has already been bitten by

Do not rediscover these the hard way:

- **Never smuggle text or control characters through a shell.** `--data "\r"`
  sent the two literal characters backslash and `r`, because no shell interprets
  that escape. Arbitrary text goes in a params file; control characters go
  through a named-key mechanism resolved in Go.
- **Tests must never touch the real `~/.aiman`.** Sandbox with
  `t.Setenv("HOME", t.TempDir())`. The suite once overwrote the maintainer's
  config and deleted 97 sessions.
- **macOS caps unix socket paths at ~104 bytes**, and `t.TempDir()` paths are
  long. `internal/server`'s tests use a `shortTempDir` helper; do the same for
  any socket you bind in a test.
- **Socket-only commands must not open the database.** `pty` and `session` are
  dispatched before the config/SQLite/JIRA/SSH setup in `cmd/aiman/main.go` for
  that reason. A test asserts the DB file is never created; keep it passing.
- **Streaming methods hold the connection.** A WebSocket bridge must keep
  reading and keep the far end serviced; an unconsumed stream stalls it
  permanently.
- Constant-time token comparison (`subtle.ConstantTimeCompare`). A
  timing-distinguishable check on a public endpoint is a real weakness.

## Acceptance

- The full gate above passes, including the no-Tailscale-in-`aiman` assertion.
- The gateway is verifiable end to end without a phone: `curl` against the
  tailnet address for the RPC endpoint, and `websocat` or a small Go client for
  both WebSocket bridges.
- Verify against the real remote **in an isolated `HOME`** with its own serve, so
  the live serve, its holders and the user's tmux sessions are untouched. Clean
  up afterwards and confirm it. Note that `pkill -f <pattern>` matches your own
  shell if the pattern appears in its command line — build the pattern so it
  cannot, or kill by PID.
- Report what you did not do, and anything you found that you did not fix.
