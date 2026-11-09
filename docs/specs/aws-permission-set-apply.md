# `aiman aws permission-set-apply`: set up the delegated permission set with the AWS CLI

**Revision:** 4 · **Size:** small · **Status:** approved by the operator on 2026-11-09 ("approve the permission-set-apply design") after two consult rounds (both revise, every objection accepted); rev 4 adds P2, writing the delegation into the laptop config, at the operator's request the same day ("is there no way we can do this through the aws cli and set these values automatically?", "yes")
**Task:** operator request on 2026-11-09 ("Can we have scripts in the aiman repo that sets that up through the AWS cli?") · **Spec:** extends `docs/specs/phone-aws-login.md` (the permission set it needs)

## Problem

The phone's AWS sign-in (`aws.login_start`) only works for a profile whose account has the `aiman-delegated` permission set in IAM Identity Center, assigned to the operator's user. Today `aiman aws permission-set-policy --steps` prints the policy and four manual console steps. The operator has three accounts, each its own Identity Center, so that is three rounds of console clicking, and a typo in the session duration or the policy is only found when the phone sign-in fails.

## How it works today

```mermaid
flowchart LR
  classDef new fill:#dcfce7,stroke:#16a34a,color:#14532d
  classDef changed fill:#fef9c3,stroke:#ca8a04,color:#713f12
  classDef removed fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray:4 2
  op[operator] -->|aiman aws permission-set-policy --steps| cli[cmd/aiman/aws_cmd.go]
  cli --> ps[awsdelegation.PermissionSetSteps]
  ps -->|text: policy + 4 steps| op
  op -->|clicks| console[Identity Center console]
  console --> pset[(permission set aiman-delegated)]
  phone[phone sign-in] -->|aws.login_start| serve[aiman serve]
  serve -->|device flow, sso_role_name| pset
```

Files: `cmd/aiman/aws_cmd.go` (`runAWS`), `internal/infra/awsdelegation/permissionset.go` (`PermissionSetPolicy`, `PermissionSetSteps`), `internal/infra/awsdelegation/policy.go` (`BuildRegionPolicy`, the region-locked document), `internal/infra/awsdelegation/sso_profile.go` (`ReadSSOProfile`, reads `sso_start_url` and `sso_region` of a laptop profile), `internal/infra/awsdelegation/iam.go` (the existing pattern for running `aws` with a profile).

## Glossary

- **Identity Center instance**: one per organisation; `sso-admin list-instances` returns its ARN and identity store id. The operator's three accounts are three instances.
- **Permission set**: a named role template in Identity Center. Assigning it to a user for an account creates the `AWSReservedSSO_<name>_<hash>` role in that account.
- **Account assignment**: (permission set, principal, account). Creating one is asynchronous; `describe-account-assignment-creation-status` reports `IN_PROGRESS`, `SUCCEEDED` or `FAILED`.
- **Admin profile**: a laptop `~/.aws/config` profile with rights to administer Identity Center in that organisation. aiman never reads its credentials; the AWS CLI does.
- **Delegation**: `remotes[].aws_delegation` on the laptop, pushed to serve; the phone sign-in needs its `sso_start_url`, `sso_region`, `sso_account_id` and `sso_role_name`.

## Proposed design

Legend: **new**, *changed*, ~~removed~~.

### Domain model

- **new** `awsdelegation.AWSCLI` interface: `Run(ctx context.Context, args ...string) ([]byte, error)`. Runs one `aws` invocation and returns stdout. **new** `awsdelegation.ExecAWSCLI{Profile string}` implements it with `exec.CommandContext(ctx, "aws", "--profile", p, "--output", "json", args...)`, like `iam.go`. Tests use a scripted fake.
- **new** `awsdelegation.ApplySpec{Profile, AccountID, Name, User string; Regions []string}`. Empty `Name` is `DefaultPermissionSetName`; empty `User` means the caller; session duration is the constant `PermissionSetSessionDuration = "PT12H"` (the phone-aws-login decision).
- **new** `awsdelegation.ApplyResult{InstanceARN, IdentityStoreID, PermissionSetARN, PrincipalID, UserName, AccountID string; CreatedPermissionSet, CreatedAssignment bool; Delegation DelegationHint}`.
- **new** `awsdelegation.DelegationHint{StartURL, Region, AccountID, RoleName string}`: the lines to put in `remotes[].aws_delegation`. `StartURL` and `Region` come from the admin profile's SSO section via `ReadSSOProfile`; empty when the admin profile is not SSO-based.
- **new** `awsdelegation.ApplyStep{Name string; Args []string}`: one planned `aws` call, for `--dry-run`.

### Structure

```mermaid
flowchart LR
  classDef new fill:#dcfce7,stroke:#16a34a,color:#14532d
  classDef changed fill:#fef9c3,stroke:#ca8a04,color:#713f12
  classDef removed fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray:4 2
  op[operator] -->|aiman aws permission-set-apply --profile admin --account 123| cli[cmd/aiman/aws_cmd.go]:::changed
  cli --> apply[awsdelegation.Apply]:::new
  apply --> plan[awsdelegation.Plan]:::new
  apply --> awscli[AWSCLI: ExecAWSCLI]:::new
  awscli -->|aws sso-admin / identitystore / sts| ic[Identity Center of that org]
  ic --> pset[(permission set + assignment)]
  apply -->|ReadSSOProfile| cfg[~/.aws/config, admin profile section]
  apply -->|ApplyResult| cli
  cli -->|progress lines, delegation hint| op
```

New file `internal/infra/awsdelegation/permissionset_apply.go`. *Changed* `cmd/aiman/aws_cmd.go`: `runAWS` dispatches `permission-set-policy` and **new** `permission-set-apply`; `awsUsage` lists both. *Changed* `PermissionSetSteps`: a first line says `Or run: aiman aws permission-set-apply --profile <admin profile> --account <id>`.

### Behaviour

`Apply(ctx, cli AWSCLI, spec ApplySpec, log io.Writer) (ApplyResult, error)` runs, in order, logging one line per step to `log`. Empty `spec.Regions` is an error before any call: `no regions: pass --regions a,b or set them on a delegation`.

0. **Identity Center region**: `ReadSSOProfile(spec.AWSConfigPath, spec.Profile)` once. Its `Region` (the profile's `sso_region`, the `sso_session` form included) is passed as `--region <r>` on every `sso-admin` and `identitystore` call, because those APIs answer only in the instance's region and a profile's working `region` is usually another one. A profile with no SSO keys passes no `--region` and the CLI's default applies. `sts get-caller-identity` never gets it.
1. **Instance**: `sso-admin list-instances`. Exactly one instance is required; zero is the error `no Identity Center instance visible to profile <p> in <region>; check the profile's sso_region`; more than one is an error listing their ARNs (an `--instance` flag is not in scope; see Open questions).
2. **Account**: `spec.AccountID`, else `sts get-caller-identity` `Account`, logged as `account <id> (the admin profile's own account)`.
3. **Permission set**: `sso-admin list-permission-sets --instance-arn <i>` (one call; the CLI paginates on its own, no `--starting-token`), then `describe-permission-set --instance-arn <i> --permission-set-arn <arn>` for each until `.PermissionSet.Name == spec.Name`. Found: when `.PermissionSet.SessionDuration != PT12H`, `update-permission-set --instance-arn <i> --permission-set-arn <arn> --session-duration PT12H`. Missing: `create-permission-set --instance-arn <i> --name <name> --session-duration PT12H --description "aiman delegated sessions"`, ARN from `.PermissionSet.PermissionSetArn`; `CreatedPermissionSet` true.
4. **Policy**: `put-inline-policy-to-permission-set --instance-arn <i> --permission-set-arn <arn> --inline-policy <PermissionSetPolicy(regions)>`, always, so a changed region list is applied.
5. **Principal**: the user name is `spec.User`, else inferred from the caller ARN only when it is an Identity Center session, `assumed-role/AWSReservedSSO_<ps>_<hash>/<session>`, whose session name is the Identity Center user name. Any other caller (IAM user, another assumed role, root) is the error `cannot infer your Identity Center user name from <arn>; pass --user <Identity Center user name>`. The lookup is `identitystore get-user-id --identity-store-id <id> --alternate-identifier '{"UniqueAttribute":{"AttributePath":"userName","AttributeValue":"<user>"}}'`, principal id from `.UserId`; `ResourceNotFoundException` is the error `no user <name> in identity store <id>; pass --user with the Identity Center user name`. `list-users` is not used: it returns whole user records (emails, phones, addresses) that the log must not carry.
6. **Assignment**: `sso-admin list-account-assignments --instance-arn <i> --account-id <acct> --permission-set-arn <arn>`, looking for `.AccountAssignments[].PrincipalId == <id>`. Present: skip, `CreatedAssignment` false. Missing: `create-account-assignment --instance-arn <i> --target-type AWS_ACCOUNT --target-id <acct> --permission-set-arn <arn> --principal-type USER --principal-id <id>`. A `ConflictException` is a propagation race: an assignment exists or a create is still in flight. Retry the create up to 3 times at `PollInterval`; when it still conflicts, list the assignments again and, if the principal is now listed, treat it as present (step 7 then provisions). If it is still not listed, `list-account-assignment-creation-status --instance-arn <i> --filter Status=IN_PROGRESS` finds the in-flight request; poll its `RequestId` as below, and `CreatedAssignment` is true once it succeeds (no provisioning needed, the create provisions). Only when nothing is listed and nothing is in flight does the conflict become the error. Otherwise read `.AccountAssignmentCreationStatus` of the create response: `SUCCEEDED` is done; `FAILED` is the error with `.FailureReason` at once; `IN_PROGRESS` polls `describe-account-assignment-creation-status --instance-arn <i> --account-assignment-creation-request-id <RequestId>` every `PollInterval` (2s) for up to `PollTimeout` (2m) until `SUCCEEDED` (`CreatedAssignment` true) or `FAILED` (error with `.FailureReason`). Timeout is an error naming the request id; the assignment may still complete, and a rerun finds it. Creating an assignment provisions the role in the account.
7. **Provision**: when the assignment already existed (step 6 skipped or hit the conflict), the role in the account still holds the old policy and duration, so `provision-permission-set --instance-arn <i> --permission-set-arn <arn> --target-type AWS_ACCOUNT --target-id <acct>` pushes steps 3 and 4 to it. Its response `.PermissionSetProvisioningStatus` follows the same rule as a create: `SUCCEEDED` is done, `FAILED` is the error with `.FailureReason` at once, `IN_PROGRESS` polls `describe-permission-set-provisioning-status --instance-arn <i> --provision-permission-set-request-id <RequestId>` like step 6. Skipped when step 6 created the assignment, or waited for an in-flight create, in this run: a create provisions the permission set as it stands, and steps 3 and 4 ran before it.
8. **Hint**: `ReadSSOProfile` again fills `Delegation.StartURL` and `Delegation.Region`; `AccountID` and `RoleName` (`spec.Name`) always. The admin profile's `sso_role_name` is never copied: it is the admin's permission set. A read failure here, after the apply succeeded, is a `warning:` line in the log, not an error.

Every step is safe to repeat: lookups before creates, the policy and the provisioning re-applied each run, conflicts read as presence. `Plan(spec ApplySpec) ([]ApplyStep, error)` returns the union of the calls steps 1 to 7 can make, with placeholders `<instance-arn>`, `<identity-store-id>`, `<permission-set-arn>`, `<principal-id>` and `<request-id>` where a value comes from an earlier call, with the same `--profile` and `--region` flags; `--dry-run` prints them as `aws --profile <p> ...` lines, headed by a line saying it is every call the command may make and that a run skips the ones its lookups make unnecessary (a first apply creates and never provisions; a rerun provisions and never creates), and makes no call.

The command needs AWS CLI v2 (`get-user-id` takes a document-type argument and list calls rely on v2's automatic pagination) and an admin profile with a valid session: `aws sso-admin` under `--profile` does not run `aws sso login`, so an expired SSO session fails the first call with the CLI's own message telling the operator to log in. `aws` runs with `--output json` through `CombinedOutput`, as `iam.go` does, because the CLI writes `An error occurred (...)` to stderr; on success `Run` returns stdout, on failure the error reads `aws <service> <subcommand>: <first line of the combined output>`. The outputs the command reads (`list-instances`, `get-caller-identity`, permission set records, `get-user-id`, assignment and provisioning statuses) hold ARNs, ids and names, never credentials; the log names the account, the permission set, the user name and the request ids, and never `UserId` from `get-caller-identity`. aiman never reads the admin profile's credentials, `~/.aws/credentials` or an SSO cache; it reads only the SSO keys of the admin profile's config section, which `ReadSSOProfile` already does. `--debug` is never passed to the CLI.

### CLI

`aiman aws permission-set-apply --profile <admin profile> [--account <id>] [--regions a,b] [--name aiman-delegated] [--user <username>] [--dry-run] [--json]`

- `--profile` is required (`errUsage` without it). `--regions` defaults to the first delegation with regions in the laptop config, as `permission-set-policy` does. `--user` names the Identity Center user to assign; the help text says this is how to target a dedicated sign-in user that holds only `aiman-delegated` (the phone-aws-login design's recommendation), and that without it the admin's own user is assigned when the admin profile is an Identity Center session.
- Progress lines go to stderr, so stdout holds only the result; on success stdout prints:

```
Done. Put this in remotes[].aws_delegation for that account:
  sso_start_url: https://d-xxxx.awsapps.com/start
  sso_region: eu-west-1
  sso_account_id: 123456789012
  sso_role_name: aiman-delegated
Then push the config to the host (Agent API page, c) and sign in from the phone.
```

- `--json` prints `ApplyResult` as one JSON object instead of the text.
- Exit 1 with the error on stderr when a step fails.

Permissions the admin profile needs: `sso:ListInstances`, `sso:ListPermissionSets`, `sso:DescribePermissionSet`, `sso:CreatePermissionSet`, `sso:UpdatePermissionSet`, `sso:PutInlinePolicyToPermissionSet`, `sso:ListAccountAssignments`, `sso:CreateAccountAssignment`, `sso:DescribeAccountAssignmentCreationStatus`, `sso:ProvisionPermissionSet`, `sso:DescribePermissionSetProvisioningStatus`, `identitystore:GetUserId` and `sts:GetCallerIdentity`. The `AWSReservedSSO_*` role in the account is created by Identity Center's own service-linked role, not by the caller. For the operator's stand-alone accounts `AdministratorAccess` on the admin profile covers all of it; in an organisation the management account's `AWSSSOMasterAccountAdministrator` does.

### P2: write the delegation into the laptop config (`--remote`)

`aiman aws permission-set-apply ... --remote <remote name> [--delegation-profile <name>]` writes the result into `~/.aiman/config.yaml` instead of asking the operator to copy the lines.

- **Before any `aws` call**: the config must have loaded (a nil config with `--remote` is the error `--remote needs a readable config: <reason>`), and the remote must exist by `name`, compared ignoring case; an unknown name is the error `no remote <name> in the config; remotes: <names>`. Nothing is written or called on either error.
- **After a successful apply** the command upserts one `config.AWSDelegation` on that remote: `Profile` is `--delegation-profile`, else the admin profile's name; `SourceProfile` is the admin profile; `SyncCredentials` true; `Regions` the regions the policy was built with; the four SSO facts from `ApplyResult.Delegation`. `(*Config).UpsertAWSDelegation(remote, d)` matches an existing entry by `Profile` across the remote's singular `aws_delegation` and its `aws_delegations` list, replaces those fields on it and keeps the entry's other fields (`duration_seconds`, `session_policy`, `role_name`, `region`, `managed_role`); with no match it appends to `aws_delegations`. Then `cfg.Save()` writes the file the way the TUI does (0600, whole config marshalled). Other remotes and other entries are untouched.
- A failed apply writes nothing. `--dry-run` with `--remote` prints the entry it would write as YAML under a `remote <name>:` line and writes nothing.
- Text output ends with `Wrote aws_delegations entry <profile> on remote <name> in <path>. Press c on the Agent API page to push it to the host.` instead of the `Put this in remotes[].aws_delegation` block. `--json` output is the `ApplyResult` object plus `"config": {"remote","profile","path"}`.
- Pushing stays the TUI's: `c` on the Agent API page runs `MarshalServeConfig`, which also fills any SSO fact still empty from the laptop's `~/.aws/config`.

## Scenario

The operator runs `aws sso login --profile acme-admin`, then `aiman aws permission-set-apply --profile acme-admin` on the laptop. aiman reads `sso_region = eu-west-1` from that profile and asks Identity Center there: one instance, account `123456789012` from the caller, no `aiman-delegated` yet, so it creates it with PT12H and puts the eu-west-1 policy. The caller ARN is an `AWSReservedSSO_AdministratorAccess_…/pieter@acme.io` session, so the user name is `pieter@acme.io`; `get-user-id` gives the principal id, the assignment is created and polled for 20 seconds until `SUCCEEDED`, and no provisioning call is needed because the create provisioned the role. It prints the four delegation lines. The operator copies them into `remotes[].aws_delegation`, reloads serve, opens the phone's Host tab and signs in; `aws.login_start` now finds the role. They repeat the command twice with the other two admin profiles. A month later they add `af-south-1` to the regions and rerun: the permission set and assignment exist, the policy is put again, and because the assignment was already there the command provisions the permission set to the account and waits for `SUCCEEDED`, so the live role has the new region. For a dedicated sign-in user they pass `--user aiman-phone`.

## Alternatives

- **A shell script under `scripts/`**: what was asked for literally. Lost because the repo tests Go, not bash; the policy document and the SSO-profile reader already live in Go; and `--dry-run` gives the script view anyway.
- **Let serve do it from the host**: lost because it would need admin credentials on the host, which the design keeps on the laptop only.
- **CloudFormation or Terraform**: lost because the operator has three stand-alone accounts and no IaC for them; one idempotent command is less to maintain.
- **Use the AWS SDK for Go instead of the CLI**: lost because the rest of `awsdelegation` shells out to the CLI, which also gives SSO sign-in for the admin profile for free.

## Open questions

1. Instance choice when `list-instances` returns more than one (an organisation with a delegated administrator sees one; an account-instance setup may see two). Default: error naming them; add `--instance <arn>` only when someone hits it.
2. Should the command also write the delegation into `~/.aiman/config.yaml`? Rev 1 to 3 said no, print it. The operator asked for it to be set automatically, so rev 4 adds `--remote` (P2 below): the operator names the remote and the command writes the entry; without `--remote` the lines are still printed.

## Decisions

Consult round 1 (Grok 4.6, report `~/.aiman/consults/a0211239-5a60-4e16-ab24-327b405f6fb1.report.md`): verdict revise, 3 high, 4 medium, 1 low. All accepted in rev 2.

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| A policy or duration change is not pushed to an already assigned account without `provision-permission-set`; a rerun left the live role stale | high | accept | Behaviour step 7: provision and poll `describe-permission-set-provisioning-status` whenever the assignment already existed; skipped only right after a create, which provisions |
| `sso-admin` and `identitystore` answer only in the Identity Center region; `--profile` alone uses the profile's working region and finds zero instances | high | accept | Behaviour step 0: `--region <sso_region>` from `ReadSSOProfile` on every Identity Center call; the zero-instance error names the region |
| Inferring the user from the caller ARN's last segment is wrong for IAM users, other assumed roles and external IdP session names, and can assign the set to the wrong user | high | accept | Step 5 infers only from an `AWSReservedSSO_*` role session; anything else requires `--user`; help text points `--user` at a dedicated sign-in user |
| `list-users --filters` is deprecated and dumps whole user records (PII) into the log | medium | accept | Step 5 uses `get-user-id` with `UniqueAttribute userName`; `ResourceNotFoundException` is the not-found path |
| Missing `--instance-arn` on every `sso-admin` call, wrong poll flag name, manual paging against the CLI's own pagination | medium | accept | Steps 3 to 7 spell out every flag; one `list-permission-sets` call, no paging flags; `--account-assignment-creation-request-id`; an immediate `SUCCEEDED` skips the poll |
| `ConflictException` on `create-account-assignment` when one exists or is in progress; the idempotency sentence was false without provisioning | medium | accept | Step 6 lists again on a conflict and treats present as present; the idempotency paragraph now rests on provisioning |
| The permissions list named IAM actions the service-linked role uses, not the caller, and an org-only managed policy | medium | accept | The CLI section lists the caller's `sso:*`, `identitystore:GetUserId` and `sts:GetCallerIdentity` actions and names `AdministratorAccess` for stand-alone accounts |
| Progress on stdout breaks `--json`; the hint must not copy `sso_role_name`; a hint read failure must not fail a finished apply; keep `--debug` off | low | accept | Progress to stderr; step 8 warns instead of failing and never copies the admin role name; the CLI never passes `--debug` |

Consult round 2 (Grok 4.6, report `~/.aiman/consults/7ed0cba6-01a4-4a62-b269-29f8f8af42f0.report.md`, against rev 2 and the acceptance tests): verdict revise, 1 high, 3 medium, 2 low. All accepted in rev 3. It confirmed the flags, JSON paths, the `--region` rule and that no provisioning is needed after a create in the same run.

| Objection | Severity | Triage | Reason |
|---|---|---|---|
| The conflict-path test did not require provisioning, and the CLI happy-path test tolerated a first-run provision, so the stale-role bug could ship on the lag path | high | accept | `TestApplyAssignmentConflictIsPresent` asserts one `provision-permission-set` with polls; the CLI happy path fails on any provision call |
| `ConflictException` is a propagation race: retry with backoff, and an in-flight create is invisible to `list-account-assignments`, so "list again or fail" dies on a rerun after a timeout | medium | accept | Step 6 retries the create 3 times, then lists, then `list-account-assignment-creation-status --filter Status=IN_PROGRESS` and polls that request; fails only with nothing listed and nothing in flight; two new tests |
| An immediate `FAILED` on the create or provision response was unspecified, so a build could hang until the timeout | medium | accept | Steps 6 and 7 treat the first response like a poll result; tests for immediate FAILED and immediate SUCCEEDED on both |
| `Run` returning stdout only would turn a real `AccessDeniedException` into `exit status 1` | medium | accept | Behaviour and Contracts: `CombinedOutput` as in `iam.go`, first line of it in the error |
| `get-user-id` and auto-pagination need CLI v2; the scenario wrongly said the CLI signs in on its own | low | accept | Behaviour names the v2 requirement and a valid session; the scenario says the admin profile is signed in first |
| `--dry-run` always lists `provision-permission-set`, which a first apply skips | low | accept | Behaviour: the plan is the union of possible calls and says so in its first line |

## Breakdown

| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |
|---|---|---|---|---|---|---|
| P1 apply + CLI | `internal/infra/awsdelegation/permissionset_apply.go`, `cmd/aiman/aws_cmd.go`, `permissionset.go` (steps line), `AGENTS.md` | 1 | none | M | smart | `internal/infra/awsdelegation/permissionset_apply_acceptance_test.go`, `cmd/aiman/aws_apply_acceptance_test.go`; `go test ./internal/infra/awsdelegation/ ./cmd/aiman/` |
| P2 write the delegation (`--remote`) | `internal/infra/config/config.go` (`UpsertAWSDelegation`), `cmd/aiman/aws_cmd.go` (flags, write, output), `AGENTS.md`, `README.md` (config example) | 2 | P1 | S | worker | `internal/infra/config/aws_delegation_upsert_acceptance_test.go`, `cmd/aiman/aws_apply_config_acceptance_test.go`; `go test ./internal/infra/config/ ./cmd/aiman/` |

## Contracts

Exact signatures the acceptance tests compile against (`internal/infra/awsdelegation/permissionset_apply_acceptance_test.go`, `cmd/aiman/aws_apply_acceptance_test.go`, both behind `//go:build acceptance` until the child removes the tag).

```go
// internal/infra/awsdelegation/permissionset_apply.go
const PermissionSetSessionDuration = "PT12H"

type AWSCLI interface {
    Run(ctx context.Context, args ...string) ([]byte, error) // one aws invocation; stdout
}

type ExecAWSCLI struct{ Profile string }
// Run executes command(ctx, args...).CombinedOutput(); on a non-zero exit the
// error is "aws <service> <subcommand>: <first line of that output>".
func (e ExecAWSCLI) Run(ctx context.Context, args ...string) ([]byte, error)
// command builds the exec.Cmd: aws --profile <p> --output json <args...>
func (e ExecAWSCLI) command(ctx context.Context, args ...string) *exec.Cmd

type ApplySpec struct {
    Profile, AccountID, Name, User string
    Regions                        []string
    AWSConfigPath                  string        // empty: ~/.aws/config (os.UserHomeDir)
    PollInterval, PollTimeout      time.Duration // zero: 2s and 2m
}

type DelegationHint struct {
    StartURL  string `json:"sso_start_url"`
    Region    string `json:"sso_region"`
    AccountID string `json:"sso_account_id"`
    RoleName  string `json:"sso_role_name"`
}

type ApplyResult struct {
    InstanceARN          string         `json:"instance_arn"`
    IdentityStoreID      string         `json:"identity_store_id"`
    PermissionSetARN     string         `json:"permission_set_arn"`
    PrincipalID          string         `json:"principal_id"`
    UserName             string         `json:"user_name"`
    AccountID            string         `json:"account_id"`
    CreatedPermissionSet bool           `json:"created_permission_set"`
    CreatedAssignment    bool           `json:"created_assignment"`
    Delegation           DelegationHint `json:"delegation"`
}

type ApplyStep struct {
    Name string
    Args []string // full aws argument list, --profile included
}

func Plan(spec ApplySpec) ([]ApplyStep, error)
func Apply(ctx context.Context, cli AWSCLI, spec ApplySpec, log io.Writer) (ApplyResult, error)
```

Every `Run` call carries `--profile <spec.Profile>`, and Identity Center calls also `--region <sso_region>` when the admin profile has one; `ExecAWSCLI` prepends `--profile <p> --output json` and passes the rest through (the fake checks both). Errors from a call read `aws <service> <subcommand>: <first line of the CLI's output>`.

```go
// cmd/aiman/aws_cmd.go
var newAWSCLI = func(profile string) awsdelegation.AWSCLI { return awsdelegation.ExecAWSCLI{Profile: profile} }
```

```go
// internal/infra/config/config.go (P2)
// UpsertAWSDelegation adds d to the named remote's aws_delegations, or updates
// the entry (singular or listed) whose Profile equals d.Profile. The remote name
// is matched ignoring case. An unknown remote or an empty d.Profile is an error
// and changes nothing.
func (c *Config) UpsertAWSDelegation(remote string, d AWSDelegation) error
```

P2 CLI flags: `--remote <name>`, `--delegation-profile <name>` (meaningful only with `--remote`). JSON with `--remote`: the `ApplyResult` fields plus `"config": {"remote": ..., "profile": ..., "path": ...}` where `path` is `config.GetConfigPath()`. The acceptance tests are `internal/infra/config/aws_delegation_upsert_acceptance_test.go` and `cmd/aiman/aws_apply_config_acceptance_test.go`.

`runAWS(args, cfg, out)` keeps its signature and dispatches `permission-set-apply`. `--dry-run` prints each planned step as one line `aws --profile <p> ...` and never calls `newAWSCLI`'s result. `--json` prints `ApplyResult` as one object. Text output ends with the `Done.` block in "CLI" and never prints `Done.` on failure. `awsUsage` names both subcommands and `--dry-run`.

## What the reader should be able to explain

1. Why the permission set lives in each account's Identity Center and why its session duration is PT12H.
2. Which `aws` calls the command makes, in order, and why each is safe to repeat.
3. Where the delegation hint's start URL comes from and why aiman never touches credentials.
4. Why the account assignment needs polling and what a timeout means.
