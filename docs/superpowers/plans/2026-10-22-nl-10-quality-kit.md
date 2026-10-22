# W9 Session Quality Kit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every repository session the content a persona would carry: matching domain checklists, a "Done when" definition with a pinned `Done-when met` to-do, a tools check at create, per-repo and per-role model/effort overrides, and a verifier that grades against the same checklists and conditions.

**Architecture:** A new pure package `internal/quality` parses checklists, renders and parses the kit blocks of `.aiman_task.md`, and builds the remote shell scripts (list, match, requires, push). `usecase.BuildQualityKit` runs those scripts through the creating process's `RemoteExecutor` (the laptop over SSH, or serve locally), so laptop and serve creates behave the same. The skill engine writes the kit into `.aiman_task.md` once at create and refreshes only the checklist block at restart. Serve owns the `Done-when met` item (new `todo.ensure_done`), and the verifier reads the kit back from the worker's task file.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (frontmatter, already a dependency), git `:(glob)` pathspecs, Bubble Tea v2.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (section "W9. Session quality kit"). Contracts: `docs/superpowers/plans/2026-10-22-nl-0-roadmap.md`.

## Global Constraints

- Go 1.26; Bubble Tea v2, Bubbles v2, Lip Gloss v2 only. UI tests build keys with `pressKey`/`pressRune` (`internal/ui/keystrokes_test.go`).
- `make ci` green before every commit (`TestOllamaIntelligenceSummariseSession` is a known failure); run `graphify update .` after code changes.
- Laptop SSH `Execute` has a 30 s timeout. Every remote script here is one bounded `Execute`.
- Both terminal backends (tmux, pty) must work; nothing here touches the terminal layer.
- Laptop→serve calls go through `aiman rpc METHOD --params-stdin` via `usecase.ServeRPC`; new methods join `rpcMethods` with one `TestRPCParams` row.
- Serve is the only writer of to-dos (`todo.Files` under `Server.MutateTodos`/`TodoAccess`).
- Scaffolding files keep the "DO NOT COMMIT" header and are written only when content changes (`remotefile.WriteIfChanged`, nl-8 T5).
- Cache stability (spec, verbatim): "Checklist and Done-when text is fixed for the life of a session, so it stays in the agent's cached prompt prefix."
- Config shapes (exact):

```yaml
repos:
  owner/name:              # nl-8 block; W9 adds the three keys below
    done_when: ["`make ci` green", "tests added", "PR open"]
    requires: [gh, make, "mcp:atlassian"]
    agent_overrides:
      claude: {model: opus, effort: high}
roles:
  verifier: {agent: codex, model: "", effort: high}
  planner:  {agent: claude, model: opus, effort: high}
  worker:   {agent: "", model: sonnet, effort: ""}
integrations:
  jira:
    acceptance_criteria_field: customfield_10035   # optional
```

- Fixed strings: to-do text `Done-when met`, Ref `done`, finding title prefix `Done-when not met: `.

## Review Focus

1. **A checklist glob without a slash** (`*.go`, `Dockerfile`). Users expect `.gitignore` semantics (any depth); git's `:(glob)` anchors at the root. Slash-free globs become `**/<glob>`. Test: `TestPathspecsForGlob` (Task 3).
2. **The agent's own task list omits `Done-when met`** (Claude `TodoWrite` never sees it). It must stay last and must not get the `⚠ not in agent's list` marker. Tests: `TestMergeKeepsDoneItemLastWithoutMarker`, `TestMutatePinsDoneItemLast` (Task 6).
3. **A restart or agent switch with an unchanged checklist set** must not rewrite `.aiman_task.md`, and must never change the Done-when block. Tests: `TestRefreshKitUnchangedWritesNothing`, `TestRefreshKitKeepsDoneWhen` (Task 5).
4. **A JIRA description with no acceptance-criteria heading, or a custom field holding ADF or plain text.** Must yield the field's items, else the heading's list items, else nothing (repo default only), never the whole description. Tests: `TestExtractAcceptanceCriteria`, `TestCustomFieldAcceptanceCriteria` (Task 2).
5. **A checklist file with broken frontmatter, or a huge file.** Skipped with a progress note, never failing the create; bodies are read capped at 16 KiB. Test: `TestParseChecklistRejectsBadFrontmatter`, `TestBuildQualityKitSkipsBadChecklist` (Tasks 3, 7).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/infra/config/repos.go` (modify, nl-8) | `RepoBootstrap.DoneWhen/Requires/AgentOverrides`, `LookupRepo` |
| `internal/infra/config/roles.go` (create) | `RoleOverride`, `RolesConfig`, `ResolveLaunchDefaults`, `RoleAgent`, `ReviewerCandidates` |
| `internal/infra/config/config.go` (modify) | `Config.Roles`, `ServeConfig.Roles`, `JiraConfig.AcceptanceCriteriaField` |
| `internal/domain/issue.go`, `internal/domain/quality.go` (modify/create) | `Issue.AcceptanceCriteria`; `QualityKit`, `Checklist` |
| `internal/infra/jira/acceptance.go` (create), `provider.go` (modify) | AC extraction and custom field |
| `internal/quality/checklist.go`, `kit.go`, `scripts.go` (create) | parsing, rendering, remote scripts |
| `internal/usecase/quality_kit.go` (create) | `BuildQualityKit`, `CheckRequires`, `PushUserChecklists`, `DoneItems` |
| `internal/infra/skills/engine.go`, `launch_flags.go`, `internal/domain/skills.go` (modify) | kit in the task file, kit-only task file, launch overrides |
| `internal/usecase/flow_manager.go` (modify) | create wiring |
| `internal/ui/dashboard.go`, `summary.go`, `agent_picker.go` (modify) | restart wiring, requires warning, role preselect, `V` offer |
| `internal/todo/done.go` (create), `store.go`, `mirror.go` (modify) | `Done-when met` invariant |
| `internal/server/handlers_todo.go` (modify, nl-1), `cmd/aiman/rpc_cmd.go`, `cmd/aiman/serve.go` | `todo.ensure_done` |
| `internal/verify/brief.go`, `manager.go` (modify, nl-7) | rubric and Done-when in the brief; reset the done item |
| `internal/infra/git/manager.go`, `internal/domain/session.go` (modify) | `GitStatus.AheadOfBase`, `CommitsAheadOfBase` |

---

### Task 1: Config keys for repos and roles

**Files:**
- Modify: `internal/infra/config/repos.go` (nl-8 T1), `internal/infra/config/config.go:20-64` (`Config`, `ServeConfig`, `MarshalServeConfig`), `:229-237` (`JiraConfig`)
- Create: `internal/infra/config/roles.go`, `internal/infra/config/roles_test.go`
- Test: `internal/infra/config/repos_test.go`

**Interfaces:**
- Consumes: nl-8 `RepoBootstrap`, `Config.Repos`, `(*Config).RepoBootstrapFor`; nl-5 `MarshalServeConfig(host)`; `AgentDefaults`, `(*Config).LaunchDefaultsFor` (`agent_defaults.go:6-22`).
- Produces:
  - `RepoBootstrap` gains `DoneWhen []string yaml:"done_when,omitempty"`, `Requires []string yaml:"requires,omitempty"`, `AgentOverrides map[string]AgentDefaults yaml:"agent_overrides,omitempty"`.
  - `func LookupRepo(repos map[string]RepoBootstrap, slug string) (RepoBootstrap, bool)` (exact, then case-insensitive); `RepoBootstrapFor` calls it.
  - `type RoleOverride struct { Agent, Model, Effort string }` (yaml `agent,model,effort`, omitempty); `type RolesConfig struct { Verifier, Planner, Worker RoleOverride }` (yaml `verifier,planner,worker`); `Config.Roles` and `ServeConfig.Roles` (`yaml:"roles,omitempty"`), copied by `MarshalServeConfig`.
  - `func (r RolesConfig) For(role string) RoleOverride`: `"reviewer"`→Verifier, `"planner"`→Planner, `""` and `"worker"`→Worker.
  - `func (c *Config) RoleAgent(role string) string` (trimmed `For(role).Agent`).
  - `func (c *Config) ResolveLaunchDefaults(name, command, repo, role string) AgentDefaults`: start from `LaunchDefaultsFor`; overlay non-empty `Model`/`Effort` from `LookupRepo(c.Repos, repo).AgentOverrides` matched by the same key rule as `LaunchDefaultsFor`; then overlay the role's non-empty `Model`/`Effort` only when the role's `Agent` is empty or equals the command's first field (lowercased).
  - `func (c *Config) ReviewerCandidates() []string`: `Roles.Verifier.Agent` (when set) followed by `Verify.Agents`, de-duplicated, order kept.
  - `JiraConfig.AcceptanceCriteriaField string yaml:"acceptance_criteria_field,omitempty"`.

- [ ] **Step 1: Write the failing tests**

```go
func TestResolveLaunchDefaultsPrecedence(t *testing.T)
// agent_defaults claude{model:sonnet,effort:low}; repos["o/r"].agent_overrides claude{effort:high}; roles.planner{agent:claude, model:opus}
// ("Claude Code","claude","o/r","") → {sonnet, high}; ("Claude Code","claude","O/R","planner") → {opus, high}
// roles.planner{agent:codex, model:o3} with command "claude" → role ignored → {sonnet, high}; unknown repo → {sonnet, low}
func TestRolesFor(t *testing.T)                 // "reviewer"→Verifier, "planner"→Planner, ""/"worker"→Worker
func TestReviewerCandidates(t *testing.T)       // roles.verifier.agent "codex", verify.agents [claude, codex] → [codex, claude]; empty role → [claude, codex]
func TestMarshalServeConfigCarriesRolesAndRepoKit(t *testing.T) // YAML round-trip keeps roles.worker.model and repos["o/r"].done_when, requires, agent_overrides
func TestLookupRepoCaseInsensitive(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/infra/config/ -run 'ResolveLaunch|RolesFor|ReviewerCandidates|RolesAndRepoKit|LookupRepo'` → FAIL (undefined).
- [ ] **Step 3: Implement** per Interfaces.
- [ ] **Step 4: Run** `go test -race ./internal/infra/config/` → PASS.
- [ ] **Step 5: Commit** `git add internal/infra/config/ && git commit -m "feat(config): per-repo done_when, requires, agent overrides and role overrides"`

---

### Task 2: JIRA acceptance criteria

**Files:**
- Create: `internal/infra/jira/acceptance.go`, `internal/infra/jira/acceptance_test.go`
- Modify: `internal/domain/issue.go:28-37` (`Issue`), `internal/infra/jira/provider.go:16-23` (`Config`), `:39-53` (`jiraIssue`), `:78` (`fields` param), `:241-262` (`toDomainIssue`)
- Modify: the four `jira.NewProvider(jira.Config{…})` sites: `cmd/aiman/main.go:129`, `cmd/aiman/serve.go:70`, `cmd/aiman-trigger/main.go:52`, `internal/ui/dashboard.go:1413`

**Interfaces:**
- Produces: `Issue.AcceptanceCriteria []string`; `jira.Config.AcceptanceCriteriaField string`; `func ExtractAcceptanceCriteria(markdown string) []string`; `func (p *Provider) toDomainIssue(ji jiraIssue) domain.Issue` fills `AcceptanceCriteria`.
- Today the client fetches only `summary,description,status,assignee,created,updated` (`provider.go:78`); `GetIssue` (`:208`) fetches all fields. Acceptance criteria exist nowhere in the code.
- Rules: `jiraIssue` keeps the raw `fields` object (`json.RawMessage`) so a configured custom field can be read by key. When the field is configured, `fetchIssues` appends it to `fields`. Field value (ADF, string, or array of strings) → `formatIssueDescription` → list items (`- `, `* `, `+ `, `1. `, `- [ ] `, `- [x] ` stripped); a value without list items gives its non-empty lines. Without the field, or when it is empty, `ExtractAcceptanceCriteria(desc)`: find the first heading line (`#`–`######`) or whole-line bold/plain label matching `(?i)^(acceptance criteria|acceptance|definition of done|AC)\s*:?$`, then take list items until the next heading. At most 20 items, each trimmed and single-line.

- [ ] **Step 1: Write the failing tests**

```go
func TestExtractAcceptanceCriteria(t *testing.T) // table:
// "## Acceptance Criteria\n\n- A\n- [ ] B\n\n## Notes\n- C" → [A, B]
// "**Acceptance criteria:**\n1. X\n2. Y" → [X, Y]
// "Just a description\n- not AC" → nil
// 25 items → 20
func TestCustomFieldAcceptanceCriteria(t *testing.T)
// fields JSON {"summary":"s","customfield_10035":{"type":"doc","content":[bulletList A,B]}} with Config field set → [A, B];
// plain string "one\ntwo" → [one, two]; field set but null → falls back to description heading
func TestFetchIssuesRequestsCustomField(t *testing.T) // httptest server records fields param contains "customfield_10035"
```

- [ ] **Step 2: Run** `go test ./internal/infra/jira/ -run 'Acceptance|RequestsCustomField'` → FAIL.
- [ ] **Step 3: Implement**, and pass `AcceptanceCriteriaField: cfg.Integrations.Jira.AcceptanceCriteriaField` at all four sites.
- [ ] **Step 4: Run** `go test -race ./internal/infra/jira/ ./cmd/... ./internal/ui/` → PASS.
- [ ] **Step 5: Commit** `feat(jira): read acceptance criteria from a custom field or the description`

---

### Task 3: Checklists: parse, match, push scripts

**Files:**
- Create: `internal/domain/quality.go`, `internal/quality/checklist.go`, `internal/quality/scripts.go`, `internal/quality/checklist_test.go`, `internal/quality/scripts_test.go`

**Interfaces:**
- Produces (domain, no deps):

```go
type Checklist struct {
    Name      string   // file base name without .md
    Path      string   // absolute path on the session host
    Source    string   // "repo" | "user"
    AppliesTo []string
}
type QualityKit struct {
    Repo       string      // owner/name; carried for launch overrides even when the kit is empty
    Role       SessionRole // nl-7 type; "" is an ordinary session
    DoneWhen   []string    // nil at restart means "leave the existing block"
    Checklists []Checklist
}
func (k QualityKit) Empty() bool // no DoneWhen and no Checklists
```

- Produces (`internal/quality`):
  - `const RepoChecklistDir = ".aiman/checklists"`, `const UserChecklistDir = ".aiman/checklists"` (under `$HOME`), `const maxChecklistBytes = 16 << 10`, `const MaxChecklists = 8`.
  - `func ParseChecklist(name, path, source string, content []byte) (domain.Checklist, error)`: YAML frontmatter between leading `---` lines; `applies_to` is a string or a list; missing frontmatter or `applies_to` means "applies to every repo session"; invalid YAML is an error.
  - `func PathspecsFor(globs []string) []string`: each glob → `:(glob)<g>`, and `:(glob)**/<g>` when `g` has no `/`.
  - `func ListScript(worktree string) string`: prints `$HOME`, then for each `*.md` in `<worktree>/.aiman/checklists` and `$HOME/.aiman/checklists`: `==> <source> <abs path>` followed by the first 16 KiB. `func ParseListOutput(out string) (home string, files []RawChecklist)` with `type RawChecklist struct{ Source, Path string; Content []byte }`.
  - `func MatchScript(worktree string, lists []domain.Checklist) string`: for each checklist with globs, `git -C <wt> ls-files --cached --others --exclude-standard -- <pathspecs> | head -n1` and prints `MATCH <name>` when non-empty. `func ParseMatchOutput(out string) map[string]bool`.
  - `func Select(all []domain.Checklist, matched map[string]bool) []domain.Checklist`: keeps glob-less and matched; a repo checklist shadows a user one of the same `Name`; sorted by `Source` (repo first) then `Name`; capped at `MaxChecklists`.
  - `func PushPlan(local map[string][]byte, remoteSums map[string]string) []string`: names whose sha256 differs or are absent remotely. `func RemoteSumsScript() string` (`mkdir -p "$HOME/.aiman/checklists"`; prints `$HOME`; `sha256sum` of each `*.md`).
- Every path in a script goes through `shellQuote`-style single quoting (copy the helper at `internal/usecase/flow_manager.go:814-816`).

- [ ] **Step 1: Write the failing tests**

```go
func TestParseChecklist(t *testing.T)                    // "---\napplies_to: [\"**/*.go\"]\n---\n- [ ] errors wrapped" → AppliesTo ["**/*.go"]; string form "applies_to: \"*.tf\"" → ["*.tf"]; no frontmatter → nil globs
func TestParseChecklistRejectsBadFrontmatter(t *testing.T) // "---\napplies_to: [\n---" → error
func TestPathspecsForGlob(t *testing.T)                  // "*.go" → [":(glob)*.go", ":(glob)**/*.go"]; "web/**/*.ts" → [":(glob)web/**/*.ts"]
func TestSelectShadowsAndCaps(t *testing.T)              // repo "go" + user "go" → only repo; 10 eligible → 8; order repo before user, then name
func TestMatchScriptRealGit(t *testing.T)                // local executor, temp repo with internal/a.go and untracked web/x.ts:
                                                         // "go" (*.go) and "ts" (*.ts) match, "tf" (*.tf) does not
func TestListScriptRealBash(t *testing.T)                // HOME=tmp with one user checklist, worktree with one repo checklist → both parsed, Source set, content capped at 16 KiB
func TestPushPlan(t *testing.T)                          // identical sum → skipped; different or missing → listed
```

- [ ] **Step 2: Run** `go test ./internal/quality/` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/quality/ ./internal/domain/` → PASS.
- [ ] **Step 5: Commit** `feat(quality): parse and match domain checklists`

---

### Task 4: Kit blocks for `.aiman_task.md`

**Files:**
- Create: `internal/quality/kit.go`, `internal/quality/kit_test.go`

**Interfaces:**
- Produces:
  - `const DoneItemText = "Done-when met"`, `const DoneWhenFindingPrefix = "Done-when not met: "`.
  - `func DoneWhen(acceptance, repoDefault []string) []string`: acceptance items first, then repo defaults, de-duplicated by `todo.Normalize`-equivalent lowercase/space-collapse (do not import `internal/todo`; a local `normalize`).
  - `func RenderDoneWhenBlock(conds []string) string` and `func RenderChecklistBlock(lists []domain.Checklist) string`. Each block is wrapped in `<!-- aiman:done-when -->` … `<!-- /aiman:done-when -->` and `<!-- aiman:checklists -->` … `<!-- /aiman:checklists -->`; empty input → `""`. Done-when body (exact first line): `## Done when` then `- [ ] <cond>` lines, then the line ``The last to-do, `Done-when met`, is yours to tick with `aiman todo update <id> --state done` only when every condition above holds. A reviewer checks each one.`` Checklist body: `## Checklists` then `Before you finish, work through each checklist that applies to the files you change:` and one `- <abs path>` per checklist.
  - `func ReplaceKitBlocks(task string, doneWhen *string, checklists string) string`: replaces existing blocks in place; a `nil` `doneWhen` keeps the existing Done-when block untouched; a missing block is appended after the first `# Task` section (or at the end when there is none) in the order Done-when, Checklists.
  - `func ParseTaskKit(task string) (doneWhen []string, checklistPaths []string)`.
  - `func KitOnlyTaskFile(doneWhen, checklists string) string`: the "DO NOT COMMIT" header copied from `engine.go:337-340`, `# Session kit`, then the blocks.
  - `const KitPrompt = "Read .aiman_task.md — it lists when this session is done and the checklists that apply to it."`

- [ ] **Step 1: Write the failing tests**

```go
func TestDoneWhenMergesAndDedupes(t *testing.T)   // AC ["Tests added", "Docs updated"], default ["tests added", "`make ci` green"] → [Tests added, Docs updated, `make ci` green]
func TestRenderBlocksDeterministic(t *testing.T)  // same input twice → identical bytes; contains "<!-- aiman:done-when -->", "- [ ] Docs updated", DoneItemText
func TestReplaceKitBlocksKeepsDoneWhen(t *testing.T) // nil doneWhen + new checklists → Done-when bytes unchanged, checklist block replaced
func TestReplaceKitBlocksInsertsAfterTask(t *testing.T)
func TestParseTaskKitRoundTrip(t *testing.T)      // render → ParseTaskKit returns the same conditions and paths
func TestParseTaskKitWithoutBlocks(t *testing.T)  // → nil, nil
```

- [ ] **Step 2: Run** `go test ./internal/quality/ -run 'DoneWhen|RenderBlocks|ReplaceKit|ParseTaskKit'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/quality/` → PASS.
- [ ] **Step 5: Commit** `feat(quality): render and parse the task-file kit blocks`

---

### Task 5: Skill engine writes the kit and applies launch overrides

**Files:**
- Modify: `internal/domain/skills.go:40-49` (interface + doc), `internal/infra/skills/engine.go:153-158` (prompts), `:194-288` (`PrepareSession`), `:333-418` (`writeTaskFile`), `:472-695` (the six `prepareX` functions), `internal/infra/skills/launch_flags.go:15-21`
- Modify call sites: `internal/usecase/flow_manager.go:368`, `internal/ui/dashboard.go:7585` (pass `domain.QualityKit{}` for now; Tasks 7 and 8 fill it)
- Test: `internal/infra/skills/engine_test.go`, `internal/infra/skills/launch_flags_test.go`

**Interfaces:**
- Consumes: Task 3/4 functions; `remotefile.WriteIfChanged` (nl-8 T5); `(*config.Config).ResolveLaunchDefaults` (Task 1).
- Produces:
  - `PrepareSession(ctx, remote, worktreePath, agent, selectedSkills, promptFree, issue, snapshot, kit domain.QualityKit) (domain.PreparedSession, error)`.
  - `func applyConfiguredLaunchFlags(cmd string, ag domain.Agent, cfg *config.Config, repo, role string) string` (uses `ResolveLaunchDefaults`).
  - The six `prepareX` functions and `bareSession` take `hasTask bool` instead of `issue *domain.Issue` (their only use of `issue` is the nil check).
- Rules:
  - Kit is ignored when `kit.Role == reviewer` (nl-7 writes its own brief) except for `Repo`/`Role` in launch flags.
  - `issue != nil`: `writeTaskFile` inserts the blocks right after the description and before `# Working Guidelines`; `hasTask = true`; prompt unchanged.
  - `issue == nil && !kit.Empty()`: read the existing `<worktree>/.aiman_task.md` (`cat … 2>/dev/null`); if present, `ReplaceKitBlocks` (Done-when `nil` when `kit.DoneWhen == nil`), else `KitOnlyTaskFile`. Write with `remotefile.WriteIfChanged`. `hasTask = true`; the single-task-file prompt is `KitPrompt` instead of `initialPrompt`.
  - `issue == nil && kit.Empty()`: today's behaviour.

- [ ] **Step 1: Write the failing tests**

```go
func TestPrepareSessionWritesKitIntoIssueTask(t *testing.T) // issue + kit{DoneWhen:[A], Checklists:[go]} → task file has "## Done when" before "# Working Guidelines" and the checklist path
func TestPrepareSessionKitOnlyTaskFile(t *testing.T)        // no issue, kit with DoneWhen → .aiman_task.md written with header; InitialPrompt == KitPrompt (claude and a bare agent)
func TestRefreshKitUnchangedWritesNothing(t *testing.T)     // second PrepareSession with the same kit → 0 WriteFile calls for .aiman_task.md
func TestRefreshKitKeepsDoneWhen(t *testing.T)              // existing file with Done-when [A]; kit{DoneWhen:nil, Checklists:[ts]} → Done-when block bytes unchanged, checklist block now lists ts
func TestPrepareSessionReviewerIgnoresKit(t *testing.T)     // Role reviewer → no task file write
func TestPrepareSessionEmptyKitUnchanged(t *testing.T)      // no issue, empty kit → no task file, no prompt (existing behaviour)
func TestLaunchFlagsUseRepoAndRole(t *testing.T)            // cfg repos["o/r"].agent_overrides claude{model:opus} → command contains "--model opus"
```

- [ ] **Step 2: Run** `go test ./internal/infra/skills/ -run 'Kit|Reviewer|LaunchFlags'` → FAIL.
- [ ] **Step 3: Implement** and update both call sites to compile with `domain.QualityKit{Repo: …, Role: …}` (repo and role from `config.Repo.Name`/`config.Role` and `s.RepoName`/`s.Role`).
- [ ] **Step 4: Run** `go test -race ./internal/infra/skills/ ./internal/usecase/ ./internal/ui/` → PASS. nl-8's `TestScaffoldingWritersCompareFirst` still passes.
- [ ] **Step 5: Commit** `feat(skills): write the quality kit into .aiman_task.md and apply repo and role launch overrides`

---

### Task 6: The `Done-when met` to-do

**Files:**
- Create: `internal/todo/done.go`, `internal/todo/done_test.go`
- Modify: `internal/todo/store.go:130-180` (`update`), `internal/todo/mirror.go:62` (`Merge`, rule 7), `internal/domain/todo.go:54-56` (Ref comment lists `done`)
- Modify: `internal/server/handlers_todo.go` (nl-1 T13), `internal/server/server.go:125-190` (`dispatch`), `cmd/aiman/rpc_cmd.go:16-25`
- Create: `internal/usecase/done_item.go`
- Test: `internal/server/handlers_todo_test.go`, `cmd/aiman/rpc_cmd_test.go`, `internal/usecase/done_item_test.go`

**Interfaces:**
- Produces (`todo`): `const DoneRef = "done"`; `const DoneItemText = "Done-when met"` (a local copy so `todo` does not import `quality`; a test in `internal/quality` asserts the two are equal); `func EnsureDoneItem(l *domain.TodoList) bool` (appends `{Text: DoneItemText, State: pending, Ref: DoneRef}`; false when an item with `Ref == DoneRef` exists); `func pinDoneLast(l *domain.TodoList)` (stable move of the `Ref == DoneRef` item to the end).
- `update` calls `pinDoneLast(&next)` right after `fn` returns, so every `Mutate` and `Mirror` keeps it last and `Diff` sees no move. `Merge` rule 7 never puts the `DoneRef` item in `NotInAgent`.
- Produces (socket): `todo.ensure_done {session}` → `{"type":"todo_list","list":…,"added":bool}`. Author `aiman`. It writes through the store, renders `.aiman_todo.md`, publishes `todo_changed`, and does **not** enqueue a steering note (the task file already tells the agent). Added to `rpcMethods`.
- Produces (usecase): `type DoneItems interface { Ensure(ctx context.Context, remote domain.RemoteExecutor, s domain.Session) error }`; `type RemoteDoneItems struct{}` whose `Ensure` calls `RegisterSessionsOnRemote(ctx, remote, []domain.Session{s})` then `ServeRPC(ctx, remote, "todo.ensure_done", {"session": s.ID})`, treating `ErrRPCUnsupported` and `ErrServeNotRunning` as nil; `func (s *Server) DoneItems() usecase.DoneItems` (direct store path, no SSH).

- [ ] **Step 1: Write the failing tests**

```go
func TestEnsureDoneItemIdempotent(t *testing.T)            // twice → one item, Ref "done", Text "Done-when met"
func TestMutatePinsDoneItemLast(t *testing.T)              // list [t1 done-item, t2]; Mutate adds t3 → order t2, t3, t1; Change has no OpMove for t1
func TestMergeKeepsDoneItemLastWithoutMarker(t *testing.T) // delivered list [t1 A, t2 Done-when met]; snapshot [A, B] → [A, B, Done-when met], NotInAgent empty
func TestMergeTicksDoneItemFromSnapshot(t *testing.T)      // snapshot contains "Done-when met" completed → item done, text and author kept
func TestDoneItemTextMatchesQuality(t *testing.T)          // todo.DoneItemText == quality.DoneItemText (in internal/quality tests to keep todo import-free)
func TestTodoEnsureDoneDoesNotSteer(t *testing.T)          // handler: added true, UpdatedBy aiman, steer Queued stays 0, todo_changed published
func TestRemoteDoneItemsToleratesOldServe(t *testing.T)    // fake executor "unknown command \"rpc\"" → nil
```
Plus a `TestRPCParams` row: `todo.ensure_done` + `{"session":"s1"}` → ok.

- [ ] **Step 2: Run** `go test ./internal/todo/ ./internal/server/ ./internal/usecase/ ./cmd/aiman/ -run 'DoneItem|PinsDone|EnsureDone|RPCParams'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/todo/ ./internal/server/ ./internal/usecase/ ./cmd/aiman/` → PASS.
- [ ] **Step 5: Commit** `feat(todo): keep a Done-when met item last on every list that has one`

---

### Task 7: Build the kit at create (laptop and serve)

**Files:**
- Create: `internal/usecase/quality_kit.go`, `internal/usecase/quality_kit_test.go`
- Modify: `internal/usecase/flow_manager.go:21-46` (fields, setters), `:265-487` (`CreateSession`: after nl-8's bootstrap; before `PrepareSession` at `:368`), `:487-503` (`finaliseCreate`)
- Modify: `cmd/aiman/main.go` (after `NewFlowManager`), `cmd/aiman/serve.go` (after `server.New`), `cmd/aiman-trigger/main.go:63-70`

**Interfaces:**
- Consumes: Tasks 1, 3, 4, 6; nl-8 `SetRepoBootstrap` map; `infraGit.ReportProgress`.
- Produces:
  - `type KitInput struct { Repo, Worktree string; Role domain.SessionRole; Issue *domain.Issue; RepoCfg config.RepoBootstrap; Restart bool }`.
  - `func BuildQualityKit(ctx context.Context, remote domain.RemoteExecutor, in KitInput) (domain.QualityKit, []string, error)`: returns the kit and progress notes (skipped checklist files). `DoneWhen` is `nil` when `Restart`, else `quality.DoneWhen(issue AC, RepoCfg.DoneWhen)`. Two `Execute` calls (list, match); a list failure returns an empty kit and a note, never an error that fails the create.
  - `func CheckRequires(ctx context.Context, remote domain.RemoteExecutor, worktree, agentCommand string, requires []string) ([]string, error)`: one `Execute`. Binaries: `command -v` under the same PATH prefix nl-8 uses for graphify. `mcp:<name>` entries: present when `<name>` appears as a key in the launched agent's MCP config: Claude `~/.claude.json` and `<worktree>/.mcp.json`; Codex `~/.codex/config.toml` (`[mcp_servers.<name>]`); Cursor `~/.cursor/mcp.json`; Gemini/agy `~/.gemini/settings.json`; other agents: reported missing with suffix ` (cannot check for this agent)`. Returns the missing entries in config order.
  - `func PushUserChecklists(ctx context.Context, remote domain.RemoteExecutor, localDir string) (int, error)`: one `RemoteSumsScript` call, then `WriteFile` for each name in `PushPlan`. Never deletes remote files.
  - `func (m *FlowManager) SetUserChecklistDir(dir string)` (laptop: `~/.aiman/checklists`; serve and trigger: unset, because their home is the remote's); `func (m *FlowManager) SetDoneItems(d DoneItems)` (default `RemoteDoneItems{}`; serve sets `srv.DoneItems()`); `func (m *FlowManager) SetRoles(r config.RolesConfig)`.
- Rules: the kit runs only for repository sessions (`Repo.Name` set and not "No Repository", not `AdHoc`, not `Role == reviewer`). Order in `CreateSession`: push user checklists (when the dir is set) → `CheckRequires` (each missing entry → `ReportProgress("Missing on remote: <entry>")`, never fatal) → `BuildQualityKit` → `PrepareSession(…, kit)`. `finaliseCreate` calls `m.doneItems.Ensure` when `len(kit.DoneWhen) > 0`; a failure is a progress line. Add `PushUserChecklists` to nl-8's `TestScaffoldingWritersCompareFirst` allowlist with the reason "compares sha256 for all files in one listing call", and add `internal/quality` to the scanned packages.

- [ ] **Step 1: Write the failing tests** (real bash via `local.NewExecutor` and temp repos, as nl-8 does)

```go
func TestBuildQualityKitMatchesAndOrders(t *testing.T)   // repo checklist go(*.go) + user checklist tf(*.tf) + user general (no globs); repo has a.go → [go(repo), general(user)]
func TestBuildQualityKitDoneWhen(t *testing.T)           // issue AC [A], RepoCfg.DoneWhen [B] → DoneWhen [A, B]; Restart → DoneWhen nil
func TestBuildQualityKitSkipsBadChecklist(t *testing.T)  // one broken frontmatter file → skipped, note names its path, others kept
func TestCheckRequires(t *testing.T)                     // PATH has fake "gh"; requires [gh, nope, "mcp:atlassian"] with ~/.claude.json {"mcpServers":{"atlassian":{}}} → missing [nope]
func TestCheckRequiresUnknownAgentMCP(t *testing.T)      // agent "kilo" → "mcp:x (cannot check for this agent)"
func TestPushUserChecklistsWritesOnlyChanged(t *testing.T) // second push with no edits → 0 WriteFile calls
func TestKitSkippedForReviewerAndAdHoc(t *testing.T)     // pure: shouldBuildKit(cfg) table
```

- [ ] **Step 2: Run** `go test ./internal/usecase/ -run 'QualityKit|CheckRequires|PushUserChecklists|KitSkipped'` → FAIL.
- [ ] **Step 3: Implement** and wire the setters at the three `NewFlowManager` sites (`cfg.Roles`, checklist dir on the laptop only, `srv.DoneItems()` in serve).
- [ ] **Step 4: Run** `go test -race ./internal/usecase/ ./internal/infra/remotefile/ ./cmd/...` → PASS.
- [ ] **Step 5: Commit** `feat(create): build the quality kit and check required tools at create`

---

### Task 8: Restart, agent switch and wizard

**Files:**
- Modify: `internal/ui/dashboard.go:7485-7600` (`restartSession`: before `PrepareSession` at `:7585`), `:1566-1582` (`workspaceStatusMsg`, `fetchWorkspaceStatus`), `:2851-2856` (handler), `:6960-6972` (`applyScanAgents`)
- Modify: `internal/ui/summary.go:13-33`, `:94-97`, `:199-205` (warning line), `internal/ui/agent_picker.go:23-45`
- Modify: `internal/fanout/fanout.go` (nl-9 T5: `Deps.WorkerAgent`), `cmd/aiman/serve.go` (fan-out and verifier wiring)
- Test: `internal/ui/summary_test.go`, `internal/ui/agent_picker_test.go`, `internal/ui/restart_agent_switch_test.go`, `internal/fanout/fanout_test.go`

**Interfaces:**
- Consumes: `BuildQualityKit` (with `Restart: true`), `PushUserChecklists`, `CheckRequires`, `(*config.Config).RoleAgent`, `ReviewerCandidates`.
- Produces: `workspaceStatusMsg.missing []string`; `func (m *SummaryModel) SetMissingTools(missing []string)`; `func (m *AgentPickerModel) Preselect(command string) bool`; `fanout.Deps.WorkerAgent string`.
- Rules:
  - Restart (includes agent switch) captures `cfg` at dispatch time with the other values (AGENTS.md "Session Restart"), pushes user checklists, builds the kit with `Restart: true` against `s.WorktreePath`, and passes it to `PrepareSession`. The Done-when block and the to-do are never touched at restart.
  - `fetchWorkspaceStatus` keeps one SSH call for the repo-exists test; when the repo block has `requires` it makes a second call to `CheckRequires` (the worktree may not exist yet, so the `.mcp.json` check uses the main clone path). The summary shows one wait-coloured line `⚠ Missing on remote: gh, mcp:atlassian (create anyway with enter)`; enter still creates.
  - `applyScanAgents` calls `Preselect(m.cfg.RoleAgent(string(m.sessionCfg.Role)))` when the scan belongs to the create wizard; a role agent that is not installed leaves the cursor at the top.
  - Fan-out child agent: `r.Agent`, else `Deps.WorkerAgent` (serve passes `cfg.RoleAgent("worker")`), else the planner's agent (nl-9's rule).
  - Verifier wiring passes `Agents: cfg.ReviewerCandidates()` instead of `cfg.Verify.Agents` (nl-7 T9).

- [ ] **Step 1: Write the failing tests**

```go
func TestSummaryShowsMissingTools(t *testing.T)        // SetMissingTools([gh]) → viewString contains "Missing on remote: gh"; enter still sets confirmed
func TestSummaryNoWarningWhenNothingMissing(t *testing.T)
func TestAgentPickerPreselect(t *testing.T)            // agents [claude, codex]; Preselect("codex") → SelectedItem is codex; Preselect("grok") → false, cursor 0
func TestRestartPassesKitWithoutDoneWhen(t *testing.T) // fake SkillEngine records kit: DoneWhen == nil, Repo == s.RepoName
func TestFanOutUsesWorkerRoleAgent(t *testing.T)       // r.Agent "", WorkerAgent "codex" → CreateChild got Agent "codex"
```

- [ ] **Step 2: Run** `go test ./internal/ui/ ./internal/fanout/ -run 'MissingTools|NoWarning|Preselect|RestartPassesKit|WorkerRoleAgent'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/ui/ ./internal/fanout/ ./cmd/...` → PASS.
- [ ] **Step 5: Commit** `feat(ui): refresh checklists on restart, warn on missing tools, preselect role agents`

---

### Task 9: Verifier rubric and Done-when findings

**Files:**
- Modify: `internal/verify/brief.go` (nl-7 T4), `internal/verify/manager.go` (nl-7 T7 start, T8 completion)
- Test: `internal/verify/brief_test.go`, `internal/verify/manager_complete_test.go`

**Interfaces:**
- Consumes: `quality.ParseTaskKit`, `quality.DoneWhenFindingPrefix`, `todo.DoneRef`.
- Produces: `BriefInput` gains `DoneWhen []string` and `Rubric []RubricItem`; `type RubricItem struct { Path, Body string }`.
- Rules:
  - `Start` parses the worker's task text (already read for `BriefInput.Task`) with `ParseTaskKit`, reads each checklist path with `os.ReadFile` capped at 16 KiB (serve runs on the session host; unreadable paths are skipped and named in the brief as "not readable").
  - `BuildBrief` adds, after the to-do list: `## Review rubric` with each checklist as `### <path>` plus its body; and when `DoneWhen` is non-empty, `## Done-when conditions` listing them with the exact instruction: ``For every condition that does not hold in this snapshot, write a finding `## [REQUIRED] Done-when not met: <condition>` explaining what is missing.``
  - On completion, in the same `Todos.Mutate` that adds finding to-dos: when any REQUIRED finding's title starts with `DoneWhenFindingPrefix` and the `Ref == todo.DoneRef` item is `done`, set it back to `pending` (author verifier). A clean pass changes nothing.

- [ ] **Step 1: Write the failing tests**

```go
func TestBuildBriefIncludesRubricAndDoneWhen(t *testing.T) // Rubric [{"/r/.aiman/checklists/go.md","- [ ] wrap errors"}], DoneWhen ["tests added"] →
                                                           // contains "## Review rubric", "- [ ] wrap errors", "Done-when not met: <condition>", "tests added"
func TestBuildBriefOmitsEmptyKit(t *testing.T)             // no "## Review rubric", no "## Done-when conditions"
func TestStartReadsKitFromWorkerTask(t *testing.T)         // worker .aiman_task.md with kit blocks and a real checklist file → brief written to the reviewer worktree contains its body
func TestCompleteReopensDoneItem(t *testing.T)             // worker list has done item in state done; review "## [REQUIRED] Done-when not met: tests added" → item pending, UpdatedBy verifier
func TestCompleteLeavesDoneItemOnCleanPass(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./internal/verify/ -run 'Rubric|OmitsEmptyKit|ReadsKit|DoneItem'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/verify/` → PASS.
- [ ] **Step 5: Commit** `feat(verify): review against the session's checklists and Done-when conditions`

---

### Task 10: Offer `V` on sessions with commits ahead of base

**Files:**
- Modify: `internal/domain/session.go:102-113` (`GitStatus.AheadOfBase int`), `internal/infra/git/manager.go:1085` (new method beside `GetGitStatus`), `internal/ui/dashboard.go:1124-1150` (`fetchGitStatus`), nl-2's `hintLine` (`internal/ui/a1_prompt.go`)
- Test: `internal/infra/git/manager_test.go`, `internal/ui/a1_prompt_test.go`

**Interfaces:**
- Consumes: nl-1 `Session.BaseBranch`; nl-7 `reviewerRunning`, `SessionRoleReviewer`, the `V` key.
- Produces: `func (m *Manager) CommitsAheadOfBase(ctx context.Context, remote domain.RemoteExecutor, path, base string) (int, error)`: one `Execute` of `git -C <p> rev-list --count "$(git -C <p> rev-parse -q --verify 'origin/<base>' || echo '<base>')"..HEAD`; empty `base` → `0, nil`. `func offerVerify(s domain.Session, st domain.GitStatus, reviewing bool) bool`: `s.Role != reviewer && !reviewing && st.AheadOfBase > 0`.
- Rules: `fetchGitStatus` fills `status.AheadOfBase` after `GetGitStatus` when `s.BaseBranch != ""` (errors leave 0). With list focus, `hintLine` starts with `V verify · ` when `offerVerify` holds for the selected session; nothing else on screen changes (noise budget). `V` keeps working on every session (nl-7).

- [ ] **Step 1: Write the failing tests**

```go
func TestCommitsAheadOfBase(t *testing.T)   // local executor, temp repo: base "main", two commits on a branch → 2; base "" → 0; unknown base → error
func TestHintLineOffersVerify(t *testing.T) // selected session AheadOfBase 2 → hint starts "V verify · "; 0, reviewer role, or reviewing → unchanged
```

- [ ] **Step 2: Run** `go test ./internal/infra/git/ ./internal/ui/ -run 'CommitsAheadOfBase|OffersVerify'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/infra/git/ ./internal/ui/` → PASS.
- [ ] **Step 5: Commit** `feat(ui): offer V on sessions with commits ahead of their base`

---

### Task 11: Skill text, docs, release check

**Files:**
- Modify: `internal/aimanskill/SKILL.md` ("Your to-do list" section from nl-1 T16), `AGENTS.md` (Configuration: `repos.<slug>.done_when/requires/agent_overrides`, `roles`, `integrations.jira.acceptance_criteria_field`; Session Creation Flow: kit step), `README.md` (checklists, Done when, requires, roles)

- [ ] **Step 1:** Add to the skill, exactly: ``The last item, `Done-when met`, belongs to aiman. Tick it only when every condition in the "Done when" section of `.aiman_task.md` holds. Before finishing, work through each checklist that file lists.`` (`aimanskill.EnsureOnHost` rewrites the skill once per version, which is the one prompt-prefix change this plan makes to a shared file.)
- [ ] **Step 2:** Document the config block from Global Constraints, checklist frontmatter (`applies_to`), the any-depth rule for slash-free globs, push-only sync of `~/.aiman/checklists/`, and precedence `agent_defaults` < `repos.<slug>.agent_overrides` < `roles.<role>`.
- [ ] **Step 3: Run** `make ci` → green apart from the known Ollama test; `graphify update .`.
- [ ] **Step 4: Commit** `docs: describe the session quality kit`

---

## Spec deviations

1. **Kit computation is not a serve method.** The spec says "serve lists the checklists". `PrepareSession` runs in whichever process creates or restarts the session (laptop over SSH, or serve locally), so `BuildQualityKit` runs there through the `RemoteExecutor`. Both paths produce the same kit; no new socket method is needed for it.
2. **"Changed files" is covered by matching tracked plus untracked files** (`git ls-files --cached --others --exclude-standard`), a superset of the agent's edits, so restart needs no separate diff pass.
3. **"Fixed for the life of a session" is per agent process.** The Done-when block is written once at create and never rewritten. The checklist block is recomputed only at restart and agent switch, which start a new prompt prefix anyway, and is written only when it changed.
4. **The `Done-when met` item exists only when there is at least one condition.** The spec says the list "always" ends with it; a session with no acceptance criteria and no `repos.<slug>.done_when` has nothing to tick. Ad-hoc, no-repo and reviewer sessions get no kit.
5. **Repository sessions without a JIRA issue now get a `.aiman_task.md`** (kit only) and a short `KitPrompt`. Today the file is written only for issue sessions (`internal/infra/skills/engine.go:200-205`).
6. **JIRA acceptance criteria are not in the client today.** Added: an optional `integrations.jira.acceptance_criteria_field` custom field, else list items under an "Acceptance criteria"/"Definition of done" heading in the description.
7. **`requires` is one flat list with an `mcp:` prefix** for MCP servers. MCP presence is a config-file key check per agent (Claude, Codex, Cursor, Gemini/agy); other agents report "cannot check". Running `claude mcp list` was rejected: it health-checks every server and can exceed the 30 s budget.
8. **The wizard warning is on the summary screen**, where the agent (needed for the MCP check) is known.
9. **`roles.worker` also covers ordinary sessions** (empty role), not only nl-9 fan-out children. A role's model and effort apply only when the role names no agent or names the one being launched. `roles.verifier.agent` is the first reviewer candidate, ahead of `verify.agents`.
10. **Role agents preselect, not force.** The wizard's agent picker starts on the role's agent; an explicit choice always wins.
11. **`V` is "offered" as a hint-line prefix** (`V verify · `), the only noise-budget-safe place. "Ahead of base" needs a new `GitStatus.AheadOfBase`, because `GitStatus.Ahead` counts against the upstream (`internal/infra/git/manager.go:1107-1111`).
12. **New fields live on nl-8's `RepoBootstrap`** rather than a renamed `RepoConfig`, to keep nl-8's names. The type name now undersells it; rename later if it bothers anyone.
13. **`todo.ensure_done` does not steer.** It is the only aiman-authored add that skips the steering note, because the task file already explains the item.
14. **User checklist push is add/update only.** Deleting a local checklist leaves the remote copy, so files created on the remote by hand are never removed.
15. **Code reality vs spec:** `MarshalServeConfig` takes no `host` yet (`config.go:52`); this plan uses nl-5's `MarshalServeConfig(host)`. `Config.Repos`, `Session.Role`, `todo.*` socket handlers and `internal/verify` do not exist yet; they come from nl-8, nl-7 and nl-1 T13.

## Open questions

- Should `Done-when met` also be added for sessions without conditions, with a built-in default (`tests pass`, `changes committed`)? This plan says no (deviation 4).
- Should deleting a checklist on the laptop delete the pushed copy? This plan says no (deviation 14).
