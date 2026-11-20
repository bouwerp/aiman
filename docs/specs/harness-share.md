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
