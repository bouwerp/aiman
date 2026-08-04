# Run-target menu, Jira scoping, and editable AWS credential lifetime

Date: 2026-08-04

Three independent fixes to the aiman TUI, grouped because they were reported together.

## 1. EC2 loop belongs in the initial menu

### Problem

`aiman`'s new-session flow puts "EC2 Autonomous Loop" at step two, as option `[6]` of the
mode picker, which sits behind the remote-server picker. Two consequences:

- With exactly one remote configured, the remote picker is skipped and the mode picker is
  reached with that remote pre-selected — so EC2 is reachable, but only by way of a remote
  it does not use.
- With zero remotes configured, `n` fails with "No remote servers configured. Go to Admin
  Menu to add one." The EC2 loop needs no remote at all, so this is a dead end for the one
  session type that would still work.

### Design

The first screen after `n` becomes a **run-target picker**: it lists configured remote
servers as numbered entries and the EC2 loop as `[e]`.

```
Where should this session run?

  [1]  dev-box (ubuntu@10.0.1.5)
  [2]  build-2 (ec2-user@10.0.1.9)

  [e]  EC2 Autonomous Loop — on-demand AWS instance

  esc: cancel
```

- The picker is shown for any remote count, including one and zero. With zero remotes it
  shows only `[e]`, plus a dim note that no remote servers are configured.
- `[6] EC2 Autonomous Loop` is removed from the mode picker. The mode picker is now only
  reached by choosing a remote, and only describes remote session kinds.
- Choosing `[e]` sets `sessionCfg.IsEC2Loop`, leaves `selectedRemote` zero, and enters the
  issue picker — the same entry point the old `[6]` used.

Three places in the EC2 path read `m.selectedRemote`, which is now empty, and each gets an
EC2 branch:

| Site | Today | Change |
| --- | --- | --- |
| `fetchAgents()` | SSH-scans the selected remote for installed agents | For EC2, return the static `agent.KnownAgents()` list |
| `handleAgentPickerUpdate` → `fetchWorkspaceStatus` | SSHes to the remote to check the workspace | Skipped for EC2; go straight to the summary |
| `handleAgentPickerUpdate` `esc` | Returns to `viewStateDirPicker` | For EC2, return to the repo picker (the state it actually came from) |

Using the static agent list for EC2 is also a correctness fix: the EC2 instance is
provisioned from scratch by `usecase.Provisioner`, so the agents installed on some other
dev box were never the right candidate set. `knownAgents` in `internal/infra/agent` gains an
exported `KnownAgents()` accessor returning a copy.

`createEC2LoopSession()` needs no change — it already sources everything from
`cfg.EC2Loop`.

## 2. Jira picker shows only my issues, in working statuses

### Problem

The default issue list is
`(assignee = currentUser() OR status = "Dev Ready") AND statusCategory != "Done"`, and a
second query then appends up to 50 issues explicitly assigned to *other* people. The
picker is therefore full of work that is not mine, and mine includes backlog states I am
not working in.

### Design

Only issues assigned to me, and only in statuses that represent live work:

- Groomed
- Analysis In Progress
- Research
- Discovery
- Dev Ready
- In Development
- Dev Review

Nothing in To Do, Later, or any Done state.

The list is configurable rather than hardcoded, so a status rename in Jira does not need a
rebuild:

- `config.JiraConfig` gains `IssueStatuses []string` (`issue_statuses` in config.yaml).
- `jira.Config` gains the same field, populated at all three `NewProvider` call sites
  (`cmd/aiman`, `cmd/aiman-trigger`, `internal/ui/dashboard.go`).
- The JIRA Configuration screen gains a fourth field, comma-separated.
- An empty or absent list falls back to the seven defaults above.

Both JQL paths are scoped:

```
default:  assignee = currentUser() AND status IN (…) ORDER BY created DESC
search:   assignee = currentUser() AND status IN (…) AND (summary ~ "q" [OR key = KEY-1])
          ORDER BY created DESC
```

The other-people's-issues query is deleted, along with `OR status = "Dev Ready"` and
`statusCategory != "Done"` (redundant once statuses are an explicit allow-list). Ordering
stays `created DESC`.

Two accepted consequences:

- Free-text search is scoped too, so a ticket in To Do or Done cannot be pulled up from
  this picker. This is intentional: the picker's job is to start work on tickets that are
  ready to be worked.
- `key = %s` is currently interpolated raw, so searching `login bug` sends
  `key = login bug` and Jira rejects the request. The `key =` clause is now emitted only
  when the query looks like an issue key (`^[A-Za-z][A-Za-z0-9_]*-\d+$`).

Status names are quoted for JQL with embedded quotes and backslashes escaped.

## 3. AWS credential lifetime is editable; warn only under 15 minutes

### Problem

Two separate irritations on the AWS Credential Status screen:

- The expiry warning fires a full hour out (`awsCredExpiryWarnWindow = time.Hour`), so the
  dashboard banner and the amber countdown are noise for most of the day.
- The credential lifetime that produces that expiry (`duration_seconds`) can only be
  edited under Manage Remote Servers → AWS delegation, several screens away from where the
  countdown is displayed.

### Design

**Warning window.** `awsCredExpiryWarnWindow` becomes `15 * time.Minute`. It feeds
`urgencyOf`, so both the dashboard banner (`formatAWSCredExpiryBanner`) and the amber
"Expires in" cell move together. Expired remains its own state and still shows red.

**Editable lifetime.** A `t` key on the AWS Credential Status screen edits the selected
profile's lifetime inline, mirroring the existing `e` rename flow (a textinput plus an
editing flag, Enter to commit, Esc to cancel).

```
  Status        Host            Local profile  Remote profile  Lifetime  Expires in
  ✓ Valid       ubuntu@dev-box  long-lived     default         12h       11h04m

  Credential lifetime (seconds): [43200        ]
  Enter to save · Esc to cancel   (900–43200, empty = default 12h)
```

- Validated as an integer in 900–43200 inclusive. Empty stores `0`, which the credential
  layer already reads as `DefaultDurationSeconds` (43200).
- Written to the matching `AWSDelegation` in config.yaml, matched on host/user/root plus
  profile name — the same matching `renameManagedDelegationProfile` already uses — then
  `cfg.Save()`.
- Rows with no local delegation config (leftover `aiman-*` profiles on the remote) cannot
  be edited; the screen says so.
- Takes effect on the next renew. Saving does not implicitly re-mint credentials, so the
  countdown does not change until `r` or `shift+R`.
- A **Lifetime** column is added so the value being edited is visible, rendered as a
  compact duration (`12h`, `45m`) or `—` when unset.

## Verification

`go build ./... && make lint && go test ./...`, plus new tests:

- JQL construction: default query, search query, key-shaped vs prose search terms, config
  override vs defaults, quoting of status names.
- `urgencyOf` at the 15-minute boundary (14m warns, 16m does not).
- Lifetime parse/validation and config write-back, including the no-delegation case.
- Run-target picker key mapping for remotes, `[e]`, and the zero-remote case.
- `fetchAgents` returning the static known-agent list on the EC2 path.

Existing `provider_test.go` and `aws_credential_expiry_test.go` are updated where the
changed queries and constants touch them.
