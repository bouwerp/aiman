# AWS credentials on the phone: design and plan

Status: draft for review. Spans `aiman` (serve, gateway, TUI) and `aiman-phone`.

## Why

The operator works from the phone while aiman runs on the laptop and agents run on a host. Agents use short-lived AWS tokens that the laptop pushed to that host. When the tokens run out, the agent's AWS calls fail, and today the operator can see and fix that only in the TUI's AWS Credentials page.

The operator asked for the same status, expiry and refresh as the TUI, on the phone. The operator confirmed that the laptop is usually on with aiman open while they are away.

## What exists

- Everything AWS is TUI-only and runs on the laptop. Serve, the gateway and the phone have no AWS method.
- `pushFreshCredentials` (`internal/ui/aws_credentials.go`) mints with the laptop's `aws` CLI and `~/.aws`, then writes tokens to the host over SSH. It is the only mint path. It records `x_security_token_expires` (`awsdelegation.ExpiryKey`) in the host's `~/.aws/credentials`.
- The host normally holds only those temporary tokens, which cannot mint new ones.
- Which profiles are delegated, their source profile and their lifetime are known only to the laptop's config (`config.AWSDelegation`).
- The dashboard holds one `aiman pty events` stream per remote while it runs (`internal/ui/session_events.go`). The stream is lossy by design: a full buffer drops events. Its own comment says it is a trigger, not a source of truth.
- `usecase.ServeRPC` lets the TUI call serve over SSH (`aiman rpc`).

## Goals

1. See every delegated profile per host on the phone: status and time to expiry, with the TUI's `~` for approximate values. This works with the laptop off.
2. Check that credentials really work (`get-caller-identity` on the host), like the TUI's check.
3. Refresh one profile or all of a host's profiles from the phone, like `r` and `R`, carried out by the TUI on the laptop.
4. Warn in the app when a profile is within 15 minutes of expiry or expired, the same window as the dashboard.
5. Always say what happened: refreshed with a new expiry, or a specific reason it did not.

## Non-goals

- Remove, rename, changing lifetime, delegating new profiles, editing the allowlist. Remove and rename from the host would also disagree with the laptop's config.
- `aws sso login` or MFA from the phone.
- Push notifications for expiry. A later change can emit through `notify`.
- Automatic renewal without the operator asking.
- Android or the laptop's own CLI gaining a mint command.

## Anti-goals

These make the feature a failure even if it otherwise works:

- Credential material leaving the host or laptop toward the phone. Only profile names, statuses, times and failure codes may cross.
- Long-lived AWS keys placed on the host to make refresh work offline. An agent on that host could read them.
- An agent making an ordinary call to trigger a mint or answer in the TUI's place, or an agent getting past the check without leaving a record on the laptop. The check is best effort by the operator's choice; see "Who may call what".
- A forged request or report minting a profile the laptop's config does not delegate.
- A spinner that never ends, or a refresh that fires hours later when the laptop wakes.
- A made-up expiry for a profile aiman did not push.

## Design

### Overview

```
phone -> gateway -> serve: aws.status, aws.refresh, aws.refresh_status
TUI (laptop) -> SSH -> serve: aws.report (heartbeat, claims work), aws.refresh_result
serve -> TUI: event aws_refresh_requested (a trigger only)
TUI -> pushFreshCredentials -> host ~/.aws (existing path)
```

### Serve: the laptop's report

`aws.report {client, profiles: [{profile, local_profile, duration_seconds}]}`, from the TUI only.

- `client` is a random id the TUI picks at start, kept for that TUI process.
- It stores the delegated profile list, without secrets, in `~/.aiman/aws-delegated.json` (0600, atomic write), so status keeps working after a serve restart and with the laptop off.
- It records `client` and the time in memory. **A TUI is present when a report arrived in the last 90 seconds.** Presence is not persisted: after a serve restart there is no TUI until the next report.
- Its response carries the requests it just claimed for this client: `{"type":"aws_report","claimed":[{id, profiles:[...], ttl_seconds}]}`. `ttl_seconds` is the time left to the deadline: relative, so clock differences between the laptop and the host do not matter. Claiming is atomic, so with two TUIs exactly one gets a request. A TUI that claims and dies leaves the request to its deadline.
- A second TUI with a different delegated list overwrites the first. The newest report wins.

The TUI sends it on connecting a stream, every 30 seconds per connected remote, and at once on an `aws_refresh_requested` event. The poll is what makes delivery reliable. The event only shortens the wait.

### Serve: status

`aws.status {check?: bool}`, read-only: `{"type":"aws_status","laptop_present":bool,"last_report_at":…,"profiles":[…]}`.

Each profile: `{profile, local_profile, status, expires_at, expiry_approx, refreshable, checked_at}`.

Rows are the union of the delegated list and the sections of the host's `~/.aws/credentials`:

| Row | `status` | `refreshable` |
|---|---|---|
| Delegated, section on host, recorded expiry in the future | `valid` | yes |
| Delegated, section on host, recorded expiry past | `expired` | yes |
| Delegated, section on host, no recorded expiry | from mtime + `duration_seconds`, `expiry_approx: true` | yes |
| Delegated, no section on host | `not_pushed`, no expiry | yes |
| Not delegated, section with a recorded expiry | as above from the recorded value | no |
| Not delegated, no recorded expiry | `unmanaged`, no expiry | no |

With no report ever received, the delegated list is empty, so all rows fall in the last two lines and the phone explains that aiman on the laptop has not reported yet.

With `check: true`, serve runs `get-caller-identity` per delegated profile on the host, sequentially, with a 10s limit per profile and 60s overall. A pass makes the status `valid` and a fail `expired`. Guard on `command -v aws`: with no `aws` on the host, skip the check and leave the file-derived status, because `CheckCredentials` reads any non-zero exit as expired, including "command not found". Results are cached 60 seconds per profile, so a stream of checks costs little. Use `errors.Is` on the sentinels and discard the text, since it embeds CLI output.

**Secrets:** add `awsdelegation.ReadHostCredentialProfiles(path)`, which parses `~/.aws/credentials` in-process in serve and returns only section names, recorded expiries and the mtime. Values never leave that function and it returns no parse text in errors. A test plants a recognisable fake key in the file and asserts that it does not appear anywhere in the `aws.status` JSON. A missing file is an empty list, and a file serve cannot read is `unavailable`.

### Serve: refresh requests

`aws.refresh {profile} | {all: true}`, phone-facing: returns `{"type":"aws_refresh","request":{id, state, profiles, deadline}}`.

- Refuses `aws_no_laptop` when no TUI is present.
- Refuses a profile not in the delegated list with `invalid_params`; `all` means every delegated profile, and an empty list is `invalid_params`.
- A request for a profile already pending or claimed returns that open request, so double taps and two phones merge instead of minting twice. Merge only an exact match of the profile set; `all` while a single is open makes a new request, and the TUI's per-host lock serialises them.
- Refuses `aws_rate_limited` after 6 new requests in 10 minutes on this serve. A merged request does not count. This bounds mint churn; it does not stop an agent that refreshes once per token lifetime, which is what the laptop log is for.
- Publishes `aws_refresh_requested` on the hub and leaves the request `pending`.
- `deadline` is created plus 2 minutes for one profile, plus 90 seconds for each additional profile. A request not claimed by the deadline, or claimed and not answered within 15 seconds after it, becomes `timed_out`, with `claimed` saying which: not claimed means nothing happened, and claimed means the outcome is unknown. **An `aws.report` never claims a request past its deadline, which is what stops a refresh firing when the laptop wakes hours later.**

`aws.refresh_status {request}`: `{"type":"aws_refresh","request":{id, state, profiles, results, deadline}}`. `state` is `pending`, `claimed`, `done` or `timed_out`. Finished requests are kept 10 minutes. Requests are in memory: after a serve restart the id is unknown, `not_found`, and the phone says to try again. Publishes `aws_refresh_done` when a request finishes, which only the TUI's own view needs.

`aws.refresh_result {request, client, results:[{profile, ok, expires_at, failure, note}]}`, TUI only. `client` must match the claimant. A result arriving after `timed_out`, while the request is still kept, is accepted and moves the request to `done`, because it reports what really happened to the file. Every requested profile gets a result. `failure` is a code, never raw CLI output:

| `failure` | Meaning | Phone copy |
|---|---|---|
| `login_required` | the laptop's AWS login expired | Log in to AWS on the laptop, then refresh again |
| `denied` | AWS refused (`AccessDenied`) | AWS refused this profile; check its role on the laptop |
| `role_needs_laptop` | a `managed_role` profile whose role cannot be assumed as it stands | Refresh this profile from aiman on the laptop |
| `not_attempted` | the request's time ran out before this profile | Not refreshed; try again |
| `no_delegation` | the TUI no longer knows this profile | This profile is no longer set up in aiman on the laptop |
| `push_failed` | minted, but writing to the host failed | The laptop could not write to the host |
| `other` | anything else | Refresh failed; see aiman on the laptop |

Classifying is a pure function in `internal/infra/awsdelegation`, with fixtures from `aws` CLI error text. Unknown text is `other`. The TUI shows the raw error on its own screen, where it is shown today. `note` carries only `RootSessionNotice` when it applies.

### Who may call what

| Method | Gateway allowlist | Session caller (`isSessionCall`) |
|---|---|---|
| `aws.status` | read | allowed (read-only, metadata) |
| `aws.refresh` | write | refused, `needs_operator` |
| `aws.refresh_status` | read | allowed |
| `aws.report` | not listed, device refused | refused, `needs_operator` |
| `aws.refresh_result` | not listed, device refused | refused, `needs_operator` |

`aws.refresh` is in the write tier and not destructive: the relay runs no IAM step, so it can only rewrite tokens the operator already delegated, into the same file.

**The session check is best effort, by the operator's choice.** It is the same peer check as `session.restart`, and it stops an agent's ordinary calls. It does not stop an agent that deliberately detaches its process (`systemd-run --user`, `ssh localhost`) or uses the same-uid `~/.aiman/gateway-token`, because an unknown peer counts as the operator. Such an agent could:

- keep its tokens alive past their expiry by calling `aws.refresh`,
- fake presence or replace the stored delegated list with `aws.report`,
- claim a request with `aws.report` and answer it with `aws.refresh_result`, so the phone shows a false outcome or the real TUI never sees the request.

What limits this:

- The TUI mints only what its own config delegates. A forged list or request cannot widen that; an unknown profile is `no_delegation`, with no mint.
- The relay runs no IAM step, so a forged request cannot change a role.
- Every relay mint leaves a toast and a line in a laptop log the host cannot reach (see TUI).
- The phone takes the expiry it shows from a status refetch, never from `results`, so a forged result cannot show a new expiry the file does not have.
- `aws.refresh` is rate limited.

A real boundary would need the laptop to act only on requests signed by the phone, with a new phone-to-laptop trust step. It is out of scope and could be added later without changing the status side.

### TUI

New file `internal/ui/aws_phone_relay.go`:

- A 30-second tick sends `aws.report` for each remote with a live stream. The profile list comes from the same entry builder as the credentials page, read from config, without running any checks.
- `aws_refresh_requested` on a stream sends a report at once for that host. The event carries no authority.
- For each claimed request, a command renews the listed profiles sequentially through `pushFreshCredentials`, classifies errors, and calls `aws.refresh_result`. A profile this TUI does not have a delegation for is `no_delegation`, without minting.
- The command runs under a context limited to `ttl_seconds`, measured from when the claim arrived. Waiting on the per-host lock honours it. It checks the context before each profile, marks the rest `not_attempted`, and still reports what finished. So a laptop that sleeps mid-batch cannot start another mint after the time is up, and the result call runs on its own short context so finished work is still reported.
- **No IAM from the phone.** `pushFreshCredentials` gains an option to skip `EnsureRole`, and the relay always sets it. `EnsureRole` can create a role, rewrite its trust policy and change its permissions, which is more than renewing tokens. The relay still builds the role ARN and assumes it; if that fails for a `managed_role` profile the result is `role_needs_laptop`. The TUI's own `r` and `R` keep running `EnsureRole`.
- **Race:** two writers to the host's `~/.aws/credentials` lose updates, which is why `renewHostCmd` is sequential. Add one per-host mutex in a small helper and take it in all three renew paths: `renewCmd`, `renewHostCmd` and this relay.
- Results route through `Update` globally, like the other `awsCred*` messages (AGENTS.md gotcha 9), and must keep working on any view. A finished relay refresh raises a toast (`refreshed <profile> by remote request`) and updates `awsCredExpiry` and the credentials page. The wording does not say "phone", because serve cannot prove who asked.
- **Audit:** each claimed request appends to `~/.aiman/logs/aws-remote-refresh.log` on the laptop (0600): time, host, request id, and each profile's outcome and failure code. Names and codes only, never tokens or CLI output. It lives on the laptop so an agent on the host cannot edit it.
- A report failing, including `unknown method` from an older serve, is ignored and retried at the next tick. It must never disturb the dashboard.

### Phone

- `lib/awsCredentials.ts`: types and pure logic. Urgency (15-minute window), countdown text matching `formatExpiresIn`, failure copy, whether a row offers Refresh.
- `hooks/useAwsCredentials.ts`: `aws.status` every 30 seconds while the screen is visible through `useVisibleForPolling`, `check: true` on opening the screen and on pull to refresh. The countdown recomputes locally from `expires_at`, not from polling.
- `hooks/useAwsRefresh.ts`: `aws.refresh` through `callServerOnce`, then `aws.refresh_status` every 2 seconds until finished, then one status refetch, whose expiry is the one the row shows. `aws_rate_limited` shows "Too many refreshes; try again in a few minutes".
- `components/AwsCredentials.tsx`: an **AWS** section on the Host tab, per server, using the tab's existing server picker. Rows: profile, status, time left, Refresh when `refreshable`. A **Refresh all** button when two or more rows are refreshable. An amber or red line above the section when a profile is within the window or expired.
- `laptop_present` false disables Refresh with "aiman isn't open on the laptop", and the list still shows.
- An unknown method renders the existing update-serve copy (`isMissingMethod`).
- `mocks/server.ts` gets all three phone methods, covering a TUI present and absent.

### Compatibility

| Mix | Behaviour |
|---|---|
| New phone, old serve | "update serve" copy, from `isMissingMethod` |
| New serve, old TUI | no reports, `laptop_present` false, Refresh disabled, host sections listed as `unmanaged` |
| New TUI, old serve | report fails silently, retried |
| New serve, old gateway | allowlist refuses; updating serve also updates the gateway |

### Unchanged contracts to keep

Each new method needs a literal `case` in `Server.dispatch`, entries in `api/openapi.yaml` (method enum, result union, the new event types), allowlist entries, and `TestDeviceMayCall` cases. `openapi_spec_test.go` and `allowlist_test.go` enforce these. The phone regenerates types with `npm run openapi:generate`.

## Decisions

| Decision | Why | Whose call |
|---|---|---|
| Refresh goes through the TUI on the laptop | Source credentials live there; laptop is usually on with aiman open | Operator |
| Blocking agents from refresh is best effort, with a rate limit and an audit trail | Matches how aiman treats agents elsewhere; a signed boundary needs a new trust step | Operator |
| Six new requests per 10 minutes, per serve | Allows retries after a login fix; stops a loop from churning mints | Claude |
| Audit log on the laptop, not the host | An agent on the host runs as the same user as serve and could edit a host log | Claude |
| No long-lived keys on the host | An agent there could read them | Claude, shown to the operator |
| Poll plus event, not event alone | The stream drops events under load | Claude |
| Presence = report within 90 seconds | Three missed 30-second ticks tolerates a slow SSH call | Claude |
| Requests in memory, not sqlite | Lifetime is minutes; a request surviving a restart is what the deadline forbids | Claude |
| Delegated list on disk at serve | Status must work with the laptop off, including after a serve restart | Claude |
| `aws.refresh` in the write tier, not destructive | It rewrites only what the operator already delegated | Claude |
| No IAM step in a phone-triggered refresh | Renewing tokens should not rewrite a role's trust policy or permissions | Claude |
| `timed_out` says whether the laptop had claimed it; late results are accepted | Goal 5 needs a specific reason, and the file is what really changed | Claude |
| In-app warning only, no push | Not asked for; easy to add later | Claude, shown to the operator |
| Section on the Host tab, no new tab | It is state of the host, and the tab already picks a server | Claude |

## Known limits

- Refresh needs the laptop awake and aiman open with its stream connected to that host. Delivery takes up to 30 seconds if the event is lost.
- `check` needs `aws` on the host. Without it the status comes only from the file.
- The TUI's streams key on host alone, while credentials key on `user@host`. A second configured user on the same host would get no reports or refresh. The operator confirmed one user per host, so this is not handled.
- An agent that deliberately gets past the session check can keep its tokens alive by requesting refreshes. The rate limit does not stop this; the laptop's toast and log record it.
- If the laptop's AWS login has expired, refresh reports `login_required`. The phone cannot fix that.
- These are existing issues, not fixed here: a session created by serve gets no AWS env, and `session.restart` from the phone drops the region env of a TUI session (`restart_session.go`). Sessions without `AWS_PROFILE` use the default chain, so a non-default profile may be ignored. Worth a separate look.

## Plan

Test first in each piece. `make ci` and `graphify update .` in `aiman`, `npm run check` in `aiman-phone`.

**aiman**

1. `awsdelegation.ReadHostCredentialProfiles` and `ClassifyRefreshFailure`, with tests, including the planted-secret test.
2. `internal/server/aws.go`: `awsState` with the delegated list on disk, presence, requests, claim, deadlines, merge. A fake clock tests presence expiry, a request past its deadline not being claimed, two clients claiming, merging, the rate limit (a merge not counting), and result from the wrong client.
3. Handlers for the five methods, `isSessionCall` refusals, status row table test, `check` guard with a fake runner including no `aws` and non-zero exit. `dispatch`, `callDeadline` for `aws.status` (70s with `check`), openapi, allowlist and tests.
4. Per-host renew lock taken in `renewCmd` and `renewHostCmd`.
5. `internal/ui/aws_phone_relay.go`: tick, event, claimed work, results, toast, audit log, global routing, with tests driving `Update`. One test claims a profile the config does not delegate and asserts no mint.
6. End to end: an in-process serve with a fake runner and a fake mint, from `aws.refresh` to `aws.refresh_status` done. Add an `evals/` case when it fits the fake remote.
7. AGENTS.md (serve RPCs, TUI, the stale AWSProfileName row and 1h warning line), README's AWS section including its stale "fresh tokens per session" claim. Release.

**aiman-phone**

8. Vendor the spec, regenerate types, add API wrappers and mock methods.
9. `lib/awsCredentials.ts` with tests, then the hooks.
10. `components/AwsCredentials.tsx` on the Host tab.
11. Ships as an update: JS only, so the fingerprint must stay the same; compare before pushing.

**Verify on the real thing** (the operator's phone and laptop, since none of this runs here):

- Status matches the TUI's page for every profile, with the laptop open.
- Refresh one and all: expiry moves on both screens and the TUI shows a toast.
- Close aiman: within 90 seconds Refresh is disabled and the list stays.
- Tap Refresh, then close the laptop: `timed_out` within the deadline, and no refresh when it reopens.
- Log out of AWS on the laptop: the phone says to log in on the laptop.
- A session on the host calling `aiman rpc aws.refresh` the ordinary way is refused. This does not prove a detached process is refused; it would not be.
- Every refresh above appears in `~/.aiman/logs/aws-remote-refresh.log` on the laptop.
