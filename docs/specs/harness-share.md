# harness-share

Design: the approved design page (rev 3), copy in .aiman_design.md of the coordinator session app-debug-harness. Wave 1 contracts follow; the gateway piece (P4) is specified after wave 1 lands.


## P1 skill and installer (`internal/aimanskill`, `internal/infra/skills`, `internal/worktree`)

- New embedded file `internal/aimanskill/harness/SKILL.md` exposed as `aimanskill.HarnessSkillText string`.
  Frontmatter `name: aiman-harness`; description mentions setting up the harness and `AIMAN_ENV=1`.
  Body contains: `test "${AIMAN_ENV:-}" = 1`, `aiman harness list --dirty`, `aiman harness up`, `aiman harness check`, `aiman harness share`, and `harness.md`.
- `type Skill struct { Name string; Files []BundleFile }` and `func Skills() []Skill` returning, in order,
  `{Name:"aiman", Files: Bundle()}` and `{Name:"aiman-harness", Files: [{SKILL.md, HarnessSkillText}, {harness.md, Harness}]}`.
- `Bundle()` keeps returning the aiman skill's three files (unchanged contract).
- `EnsureOnHost(home, roots)` installs every skill's files into `<agent dir>/skills/<skill name>/` for each user and project agent dir it already covers.
- `IsProjectSkillPath(rel)` is true for every skill's files, e.g. `.claude/skills/aiman-harness/SKILL.md` and `.codex/skills/aiman-harness/harness.md`; false for `.claude/skills/other/SKILL.md`.
- `UserSkillFiles` / `ProjectSkillFiles` keep their current meaning (the aiman skill's SKILL.md paths). New `UserSkillDirs(home, skill string) []string` and `ProjectSkillDirs(root, skill string) []string` give the directories for any skill name.
- `internal/infra/skills/engine.go` pushes every skill's files to the worktree (the `PrepareSession` path that loops `Bundle()` today).
- The aiman `SKILL.md` `## Harness` section names the `aiman-harness` skill.
- Acceptance: `go test ./internal/aimanskill -run TestHarnessSkill`.

## P2 share in serve (`internal/server`, `internal/infra/sqlite`, `cmd/aiman`, `internal/config`)

- Sqlite: `(*sqlite.Repository).SaveHarnessShare(ctx, domain.HarnessShare) error`, `GetHarnessShare(ctx, id string) (domain.HarnessShare, error)` (missing → `domain.ErrNotFound`-style error `sqlite.ErrShareNotFound`), `RevokeHarnessShares(ctx, session string, at time.Time) (int, error)`, `SaveGatewayAnnounce(ctx, domain.GatewayAnnounce) error`, `GetGatewayAnnounce(ctx) (domain.GatewayAnnounce, bool, error)`.
- `domain.HarnessShare{ID, Session, Run string; CreatedAt time.Time; CapAt *time.Time; RevokedAt *time.Time}`; `domain.GatewayAnnounce{Base string; ConsolePort int; Scheme string; PID int; At time.Time}`.
- RPCs:
  - `gateway.announce {base, console_port, scheme, pid}` → `{"type":"gateway_announced"}`; session caller → `needs_operator`.
  - `harness.share {session?, run?}` → `{"type":"harness_share","url":"<base>:<console_port>/s/<id>","share":"<id>","run":"<run>"}`. The id is 32 random bytes base64url (43 chars). Needs the session's run `ready` (else `harness_not_running`). No announce, or announce older than 3 minutes (`Server` clock) → `unavailable` with message containing `gateway is not running`. Revokes the session's earlier shares. Ledger `harness.shared` with session, run, share (domain kind `EventHarnessShared`). Session callers allowed (scope-checked).
  - `harness.unshare {session?}` → `{"type":"harness_unshared","revoked":n}`.
  - `harness.share_get {id}` → `{"type":"harness_share_state","share":id,"session":s,"run":r,"state":"live"|"ended"}`; ended when revoked, past `cap_at`, or the run is not `ready`. Session caller → `needs_operator`. Unknown id → `not_found`.
  - Hub event `harness_share {id, state}` published on share, unshare and when share_get first sees it ended.
  - `harness.verdict` accepts optional `tester` (string): when set, the ledger row's actor is `tester:<login>` and the steer note reads `[aiman] tester <login> marked harness scenario …`.
- Config: `config.GatewayConfig.ConsoleLogins []string` (yaml `console_logins`), `config.HarnessConfig.ShareTTL string` (yaml `share_ttl`); pushed by `MarshalServeConfig`.
- CLI: `aiman harness share`, `aiman harness unshare`.
- `api/openapi.yaml` lists the four methods and the event; gateway device allowlist refuses all four (`internal/gateway/allowlist_test.go` names them).
- Acceptance: `go test ./internal/server -run TestHarnessShare && go test ./internal/infra/sqlite -run TestHarnessShares`.

## P3 console remote mode (`internal/console`)

- `Options` gains `Hosts []string` (accepted `Host` names without port), `Port int` (public port), `HTTPS bool`, `Methods []string` (allowed serve methods; nil = all, as today).
- `New(opts, nil)` with `len(opts.Hosts) > 0` returns a server without a listener; `Handler() http.Handler` serves it. `New` with a non-loopback listener and no Hosts still fails as today.
- With Hosts: `hostOK` accepts `<h>:<Port>` for each h in Hosts (and bare `<h>` when Port is 443 with HTTPS or 80 without); `originOK` accepts `<scheme>://<h>:<Port>` with scheme `https` when HTTPS else `http`; CSP `connect-src` lists `wss://<h>:<Port>` (HTTPS) or `ws://<h>:<Port>`.
- A call to a serve method not in `Methods` answers 403 with code `method_not_allowed` and never reaches `RPC`.
- `/api/state` carries `"shared": true` when Hosts is set; the page hides up, down and the editor launch entry then.
- `console.HostForwarder{}` implements `Forwarder`: `Open` returns forwards with `LocalPort == HostPort`, opens nothing, never dies.
- Acceptance: `go test ./internal/console -run 'TestRemoteHosts|TestMethodSet|TestHostForwarder'`.

## P4 gateway console and forwards (`internal/gateway/console*.go`, `cmd/aiman-gateway/*.go`)

Design: rev 3, "Gateway console", "tsnet app forwards", "Debugger". Wave 1 as built: the announce goes stale after 3 minutes (`server.GatewayAnnounceMaxAge`), so announce every minute; a shared console's app links use `Hosts[0]`; a shared console's `Serve` only polls and must still run; `Methods` gates `harness.status` too.

- Types in `internal/gateway`:
  - `type Peer struct { Login, NodeID string }` (`NodeID` is the tailnet node's `StableID`) and `type PeerWhoIs func(ctx context.Context, remoteAddr string) (Peer, error)`.
  - `type ConsoleOptions struct { Logins, Hosts []string; Port int; HTTPS bool; WhoIs PeerWhoIs; HostNodes func(ctx context.Context) ([]string, error); RPC console.RPC; Listen func(network, addr string) (net.Listener, error); Dial func(ctx context.Context, network, addr string) (net.Conn, error); Log io.Writer }`.
  - `func NewConsoleShares(ConsoleOptions) (*ConsoleShares, error)`: an error when `Logins`, `Hosts`, `WhoIs`, `HostNodes` or `RPC` is missing or `Port` is 0. `Dial` nil is a `net.Dialer`.
  - `(*ConsoleShares).Handler() http.Handler`, `ShareEvent(id, state string)` (`ended` ends the share), `PollOnce(ctx)` (one `harness.share_get` per live share; `ended` or `not_found` ends it; for a live share it reads `harness.status {run}` and opens or closes forwards), `Run(ctx) error` (`PollOnce` every 10s until ctx ends), `Close() error` (ends every share).
  - `type AnnounceParams struct { Base string; ConsolePort int; Scheme string; PID int }` and `func Announce(ctx, console.RPC, AnnounceParams) error` calling `gateway.announce {base, console_port, scheme, pid}`.
- Every request, `/` and static files included, passes the peer check first: WhoIs succeeds, `NodeID` is non-empty and not in `HostNodes()` (an error from either fails closed), and `Login` is in `Logins` ignoring case. Anything else is 403 and calls nothing on serve.
- `GET /s/<id>`: `harness.share_get {id}` with no cache. Live: mint a visit token (32 random bytes, base64url, 43 chars) bound to the share, login and node, 12h renewed by use, and answer 302 `Location: /#token=<token>`. Ended or `not_found`: 404.
- `/api/*` and the `/api/dap` WebSocket carry the visit token as the console does (`X-Aiman-Console-Token`, or the `aiman-token.<token>` subprotocol). An unknown token, a token of an ended share, or a token presented by another login or node is 401. The call goes to that share's `console.Server`, made on first use with `New(opts, nil)`: `Hosts`, `Port`, `HTTPS` from the gateway, `Run` the share's run, `Forwarder: console.HostForwarder{}`, its `Serve` running, and `Methods` exactly `harness.status`, `list`, `logs`, `capture`, `check`, `verdict`, `debug`, `files`, `file`, `db`, `evm_txs`, `evm_trace`, `aws`, `aws_calls`, `aws_write` (no `up`, no `down`). The gateway hands the console its own token and swaps the visit token for it, or routes by visit token in an equivalent way; the console's own Host, Origin and CSP checks stay on.
- The share console's `RPC` adds `tester: <login>` to every `harness.verdict` it sends.
- Ending a share (event, poll, `Close`) drops its visit tokens, closes its console, its `/api/dap` bridges and its forwards at once.
- Forwards: for a live share whose run is `ready`, listen with `Listen("tcp", ":<host_port>")` for each run port whose `service` is `app` and whose `name` is not `dap`, never at 8080 or 8090. Each accepted connection passes the peer check and needs a live visit token for that share held by the same login; otherwise it is closed without dialling. Admitted connections are proxied to `Dial(ctx, "tcp", "127.0.0.1:<host_port>")`. A run that is not ready, or a share that ends, closes them.
- `cmd/aiman-gateway`: the console starts only when `gateway.console_logins` is non-empty and Funnel is off. It listens on the tailnet at `:8090` with `ListenTLS` when `CertDomains()` is non-empty (scheme https, base `https://<first cert domain>`), else plain (scheme http, base `http://<hostname>`). `Hosts` are the short hostname first, then every cert domain, then the node's tailnet IPv4 and IPv6 addresses. `HostNodes` returns the tsnet node's `Status.Self.ID` (StableID) and, when the system tailscaled socket answers, that daemon's `Self.ID`. `PeerWhoIs` comes from `LocalClient.WhoIs` (`Node.StableID`, `UserProfile.LoginName`). It announces at start and every minute, subscribes to `session.events` and passes each `harness_share` event to `ShareEvent`, and runs `Run`. `tsnet.Server.Listen` backs `Listen`.
- Acceptance: `go test ./internal/gateway -run 'TestConsoleShare|TestConsoleForward'`.
