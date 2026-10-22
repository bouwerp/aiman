# W7 Faster, Cheaper Starts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** New sessions are ready to work sooner and burn fewer tokens: per-repo worktree bootstrap, an optional RTK install with agent hooks, a background graphify build, and no rewrites of unchanged scaffolding files that sit in an agent's cached prompt prefix.

**Architecture:** Bootstrap and graphify live in `internal/usecase` and run inside `FlowManager.CreateSession`, which both the laptop TUI and serve's `session.create` (`internal/server/handlers_session.go:547-658`) call, so one change covers both create paths. Remote shell snippets are pure builders in small infra packages (`internal/infra/graphify`, `internal/infra/rtk`, `internal/infra/remotesvc`) so they can be tested as strings and, where it matters, run under real bash through `local.Executor`. Cache hygiene is two helpers with one contract: `remotefile.WriteIfChanged` (one `sha256sum` round trip, then write only on mismatch) for writers that go through a `RemoteExecutor`, and nl-1's `fsutil.WriteIfChanged` for serve-side local writers. Every scaffolding writer adopts one of them, pinned by an AST guard test.

**Tech Stack:** Go 1.26, Bubble Tea v2, bash on the remote, RTK (`rtk-ai/rtk`), graphify CLI.

**Spec:** `docs/superpowers/specs/2026-10-22-aiman-next-level-design.md` (sections Constraints, Anti-goals "Rewriting files…", W7).

**Depends on:** nl-1 (`FlowManager.resolveWorktree` from Task 3, `fsutil.WriteIfChanged` from Task 9); nl-5 (`MarshalServeConfig(host)`); nl-7 (`SessionConfig.PreparedWorktree`, skipped by `shouldBootstrap`); nl-4 (W3 usage view, `$`, `usageHost.extra`), which Task 7 fills with `rtk gain`; nl-3a for the `.aiman_handover.md` writer that Task 6 audits.

**RTK facts used here (checked against `rtk-ai/rtk` docs on 2026-10-22):**
- Install: `curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/master/install.sh | sh`. Installs to `$HOME/.local/bin` (override `RTK_INSTALL_DIR`), no sudo, Linux x86_64/aarch64 and macOS.
- Name collision: `cargo install rtk` can install "Rust Type Kit"; the docs' check for the right binary is that `rtk gain` works.
- Hooks (user-global): Claude Code `rtk init --global`; Codex `rtk init --global --codex`; Cursor `rtk init --global --agent cursor`; Gemini `rtk init --global --gemini`; Copilot CLI `rtk init --global --copilot`; Pi `rtk init --global --agent pi`. Antigravity and Kilo Code are project-scoped only; Grok, Muse Code, Deep Code, Ageni are not supported.
- Non-interactive: `--auto-patch` approves overwriting a modified managed file; telemetry is off by default and `RTK_TELEMETRY_DISABLED=1` blocks it regardless of the `rtk init` consent prompt.
- Savings: `rtk gain --all --format json` prints `{"summary":{"total_commands","total_input","total_output","total_saved","avg_savings_pct"},"daily":[…],…}`. Plain `rtk gain` may open a live dashboard when the RTK proxy runs, so never call it without `--format`.

## Global Constraints

- Go 1.26; Bubble Tea v2 (`charm.land/bubbletea/v2`), Bubbles v2, Lip Gloss v2 only. Tests build key events with `pressKey`/`pressRune` (`internal/ui/keystrokes_test.go`).
- `make ci` green before every commit. `TestOllamaIntelligenceSummariseSession` is a known failure.
- Laptop SSH `Execute` has a 30 s timeout; nothing in the create path may block on a long-running command. Long work runs detached with all fds redirected.
- Both terminal backends (tmux, pty) must work.
- New and touched scaffolding files keep the `.aiman_*` "DO NOT COMMIT" header and ignore mechanism (`domain.AimanGeneratedFileGlob`, `internal/infra/git/manager.go:220-245`) and are written only when content changes.
- Config shape (exact):

```yaml
repos:
  owner/name:
    copy: [.env.local]
    symlink: [node_modules]
    setup: "make deps"
```

- graphify runs as `nice graphify update .` in the background, after the agent starts, only when `graphify` is present on the remote.

## Review Focus

1. **A `symlink:` entry for a directory the repo ignores with a trailing slash** (`node_modules/` in `.gitignore`). Git does not apply a directory pattern to a symlink, so the link would show as untracked and an agent could commit it. Bootstrap adds each symlink path to the repo's `info/exclude`. Test: `TestBootstrapSymlinkIsExcludedFromGit` (Task 1).
2. **A slow `setup:` (minutes of `npm ci`)** must not hold the create call past the 30 s SSH budget or block the agent launch. Test: `TestBootstrapSetupReturnsImmediately` runs `setup: "sleep 5"` through `local.Executor` and asserts `Execute` returns in under 2 s (Task 1).
3. **A `copy:`/`symlink:` path that is absolute, contains `..`, or is missing in the main checkout.** Absolute and `..` entries are rejected at config validation; a missing source is skipped with a progress note, and an existing destination is never overwritten. Tests: `TestBootstrapRejectsEscapingPaths`, `TestBootstrapSkipsMissingAndExisting` (Task 1).
4. **A worktree without graphify-out in `.gitignore`.** Running graphify must not leave `graphify-out/` as untracked noise in the agent's `git status`. Test: `TestGraphifyScriptExcludesOutput` runs the script under bash in a temp repo and asserts `git status --porcelain` is empty afterwards (Task 4).
5. **A file whose content is unchanged but whose remote copy is missing or unreadable** (`sha256sum` prints nothing, or the remote has no `sha256sum`). That must count as "changed" and write, never as "unchanged". Test: `TestWriteIfChangedWritesWhenHashUnavailable` (Task 5).

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/infra/config/repos.go` (create) | `RepoBootstrap`, `Config.Repos`, lookup, validation |
| `internal/usecase/worktree_bootstrap.go` (create) | bootstrap script builder and applier |
| `internal/usecase/flow_manager.go` (modify) | call bootstrap after worktree setup; graphify after launch; setters |
| `internal/infra/graphify/graphify.go` (create) | probe and background-update shell snippets |
| `internal/infra/skills/engine.go` (modify) | task-file graphify line; `WriteIfChanged` adoption |
| `internal/infra/remotefile/remotefile.go` (create) | `WriteIfChanged` |
| `internal/infra/remotefile/guard_test.go` (create) | AST guard over scaffolding writers |
| `internal/infra/rtk/rtk.go` (create) | install script, `Gain`, `ParseGain`, `FetchGain` |
| `internal/infra/remotesvc/remotesvc.go`, `internal/domain/daemon.go` (modify) | `RTK=` probe line, `Daemon.RTKVersion` |
| `internal/ui/agent_api.go` (modify) | `t` installs RTK; RTK line in the detail area |
| `cmd/aiman/main.go`, `cmd/aiman/serve.go`, `cmd/aiman-trigger/main.go` (modify) | pass `cfg.Repos` |

---

### Task 1: Repo bootstrap config and applier

**Files:**
- Create: `internal/infra/config/repos.go`, `internal/infra/config/repos_test.go`
- Modify: `internal/infra/config/config.go:20-64` (`Config.Repos`, `ServeConfig.Repos`, `MarshalServeConfig` copies it)
- Create: `internal/usecase/worktree_bootstrap.go`, `internal/usecase/worktree_bootstrap_test.go`

**Interfaces:**
- Produces (config):
  - `type RepoBootstrap struct { Copy []string \`yaml:"copy,omitempty"\`; Symlink []string \`yaml:"symlink,omitempty"\`; Setup string \`yaml:"setup,omitempty"\` }`
  - `Config.Repos map[string]RepoBootstrap \`yaml:"repos,omitempty"\``; same field on `ServeConfig`, copied by `MarshalServeConfig` so serve-side creates see it.
  - `func (c *Config) RepoBootstrapFor(repo string) (RepoBootstrap, bool)`: exact key first, then case-insensitive.
  - `func (b RepoBootstrap) Validate() error`: every `Copy`/`Symlink` entry must be non-empty, relative, and `filepath.Clean`-ed without a leading `..` element; error names the offending entry.
- Produces (usecase):
  - `type BootstrapResult struct { Copied, Linked, Skipped []string; SetupLog string }` (`SetupLog` empty when no setup ran).
  - `func bootstrapScript(mainRepo, worktree string, b config.RepoBootstrap) string`
  - `func ApplyWorktreeBootstrap(ctx context.Context, remote domain.RemoteExecutor, mainRepo, worktree string, b config.RepoBootstrap) (BootstrapResult, error)`: validates, runs the script in one `Execute`, parses its output lines `COPIED <p>`, `LINKED <p>`, `SKIPPED <p>`, `SETUP_LOG <abs path>`.

Script behaviour (one bash script, every path single-quoted with `shellQuote`):
- For each copy `p`: if `$main/p` exists and `$wt/p` does not, `mkdir -p "$(dirname "$wt/p")" && cp -a "$main/p" "$wt/p"`, print `COPIED p`; else print `SKIPPED p`.
- For each symlink `p`: same existence rules, `ln -s "$main/p" "$wt/p"`, print `LINKED p`, and append the line `/p` to `"$(git -C "$wt" rev-parse --git-common-dir)/info/exclude"` when that exact line is absent.
- Setup: `gd=$(git -C "$wt" rev-parse --absolute-git-dir)`; `log="$gd/aiman-setup.log"`; `(cd "$wt" && nohup setsid bash -lc <setup> >"$log" 2>&1 </dev/null &)`; print `SETUP_LOG $log`. The log lives in the worktree's git dir, so it is never committed and needs no ignore rule.

- [ ] **Step 1: Write failing tests**

```go
// internal/infra/config/repos_test.go
func TestRepoBootstrapForIsCaseInsensitive(t *testing.T) // key "Owner/Name" found by "owner/name"
func TestRepoBootstrapValidate(t *testing.T)             // "/etc/passwd", "../x", "a/../../x", "" → error; ".env.local", "web/node_modules" → nil
func TestMarshalServeConfigCarriesRepos(t *testing.T)    // MarshalServeConfig("h") (nl-5's signature) YAML has repos["o/n"].setup == "make deps"
```

```go
// internal/usecase/worktree_bootstrap_test.go — real bash via local.NewExecutor, temp dirs with `git init` + `git worktree add`
func TestBootstrapCopiesAndLinks(t *testing.T)            // .env.local copied (content equal, not a symlink); node_modules is a symlink to main's
func TestBootstrapSkipsMissingAndExisting(t *testing.T)   // missing source → Skipped; existing dest with other content untouched → Skipped
func TestBootstrapSymlinkIsExcludedFromGit(t *testing.T)  // main .gitignore has "node_modules/"; after bootstrap `git -C wt status --porcelain` is empty
func TestBootstrapSetupReturnsImmediately(t *testing.T)   // Setup "sleep 5; echo done": call returns < 2s; SetupLog under wt git dir; log contains "done" within 10s
func TestBootstrapRejectsEscapingPaths(t *testing.T)      // Copy ["../x"] → error, no Execute call (fake remote records 0 commands)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra/config/ ./internal/usecase/ -run 'RepoBootstrap|MarshalServeConfigCarriesRepos|Bootstrap'`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement config types and `worktree_bootstrap.go` per Interfaces.**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/infra/config/ ./internal/usecase/ -run 'RepoBootstrap|Bootstrap|ServeConfig'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/config/ internal/usecase/worktree_bootstrap*.go
git commit -m "feat(create): per-repo worktree bootstrap (copy, symlink, background setup)"
```

---

### Task 2: Run bootstrap in CreateSession (laptop and serve create paths)

**Files:**
- Modify: `internal/usecase/flow_manager.go:21-46` (field + setter), the call site of nl-1's `resolveWorktree` in `CreateSession` (bootstrap right after it returns), `:357` (prompt note joined after `config.InitialPrompt`)
- Modify: `cmd/aiman/main.go:150`, `cmd/aiman/serve.go:77`, `cmd/aiman-trigger/main.go:63-70`
- Test: `internal/usecase/flow_manager_test.go`

**Interfaces:**
- Consumes: `ApplyWorktreeBootstrap`, `config.Config.RepoBootstrapFor`, `infraGit.ComputeMainRepoPath(sshMgr.GetRoot(), config.Repo.Name)` (`internal/infra/git/manager.go:894`), `infraGit.ReportProgress`, `joinPrompt` (`flow_manager.go:52`).
- Produces:
  - `func (m *FlowManager) SetRepoBootstrap(repos map[string]config.RepoBootstrap)`
  - `func shouldBootstrap(config domain.SessionConfig) bool`: true only for a worktree this call created (`config.Repo.Name` set and not "No Repository", and none of `AdHoc`, `AttachExisting`, `ReuseWorkspace`, `PreparedWorktree`); `ExistingBranch` and the default new-worktree case are true.
  - `func bootstrapPromptNote(setup, logPath string) string` returning exactly: ``Worktree setup (`<setup>`) is running in the background; its log is <logPath>. Check it has finished before building or running tests.``

Rules: `CreateSession` calls `ApplyWorktreeBootstrap` right after `resolveWorktree` (nl-1 Task 3) when `shouldBootstrap(config)`. It never runs for `AttachExisting`, `ReuseWorkspace`, ad-hoc, no-repo or prepared (nl-7 reviewer) worktrees. Bootstrap errors are reported with `ReportProgress("Worktree bootstrap failed: …")` and do not fail the create. The prompt note is joined onto `sendKeysPrompt` with `joinPrompt` after `config.InitialPrompt` only when `SetupLog != ""`. Wiring: each `NewFlowManager` call site follows with `flow.SetRepoBootstrap(cfg.Repos)`.

- [ ] **Step 1: Write failing tests** (`flow_manager_test.go` has no fake git manager, so the decision is a pure function, as in nl-1 Task 3)

```go
func TestShouldBootstrap(t *testing.T)    // table: default new worktree → true; ExistingBranch → true; AttachExisting, ReuseWorkspace, AdHoc, PreparedWorktree, "No Repository", "" → false
func TestBootstrapPromptNote(t *testing.T) // exact string above
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/usecase/ -run 'ShouldBootstrap|BootstrapPromptNote'`
Expected: FAIL.

- [ ] **Step 3: Implement the setter, the call, the note, and the three wiring sites.**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/usecase/ ./cmd/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/usecase/flow_manager.go internal/usecase/flow_manager_test.go cmd/aiman/main.go cmd/aiman/serve.go cmd/aiman-trigger/main.go
git commit -m "feat(create): apply repo bootstrap in CreateSession for laptop and serve creates"
```

---

### Task 3: RTK infra (install script, probe line, `rtk gain`)

**Files:**
- Create: `internal/infra/rtk/rtk.go`, `internal/infra/rtk/rtk_test.go`
- Modify: `internal/domain/daemon.go:13-22` (`RTKVersion string`)
- Modify: `internal/infra/remotesvc/remotesvc.go:234-278` (`ProbeScript`: for `KindServe` echo `RTK=` before `---LOGS---`), `:292-330` (`ParseProbe` reads `RTK`)
- Modify: `internal/infra/remotesvc/remotesvc_test.go`

**Interfaces:**
- Produces:
  - `const InstallURL = "https://raw.githubusercontent.com/rtk-ai/rtk/master/install.sh"`
  - `type HookTarget struct { Binary string; Args []string }` and `var HookTargets = []HookTarget{{"claude", []string{"--global"}}, {"codex", []string{"--global", "--codex"}}, {"cursor-agent", []string{"--global", "--agent", "cursor"}}, {"gemini", []string{"--global", "--gemini"}}, {"copilot", []string{"--global", "--copilot"}}, {"pi", []string{"--global", "--agent", "pi"}}}`
  - `func InstallScript() string`: `set -e`; PATH prefixed with `$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.cargo/bin:/usr/local/bin`; `export RTK_TELEMETRY_DISABLED=1`; `export RTK_EDITION=oss` (the Apache-2.0 edition; decided by Claude, see Open questions); install via `curl -fsSL <InstallURL> | sh` only when `command -v rtk` fails; verify with `rtk gain --all --format json >/dev/null 2>&1 </dev/null` else print `RTK_WRONG_BINARY=1` and `exit 3`; print `RTK_VERSION=<rtk --version | head -1>`; for each target whose binary is on PATH run `rtk init <args> --auto-patch </dev/null` and print `RTK_HOOK=<binary> ok` or `RTK_HOOK=<binary> failed`.
  - `type InstallReport struct { Version string; Hooks map[string]bool; WrongBinary bool }`; `func ParseInstall(out string) InstallReport`.
  - `type Gain struct { Commands, InputTokens, OutputTokens, SavedTokens int64; SavingsPct float64 }`
  - `func ParseGain(raw []byte) (Gain, error)` reading `summary.total_commands|total_input|total_output|total_saved|avg_savings_pct`.
  - `type Executor interface { Execute(ctx context.Context, cmd string) (string, error) }`
  - `func FetchGain(ctx context.Context, ex Executor) (g Gain, installed bool, err error)`: runs `export PATH="$HOME/.local/bin:$PATH"; command -v rtk >/dev/null 2>&1 || { echo RTK_MISSING; exit 0; }; rtk gain --all --format json </dev/null`; `RTK_MISSING` → `installed=false, err=nil`. This is the function the nl-4 usage view calls with the remote's `*ssh.Manager`.
  - `Daemon.RTKVersion`: `ParseProbe` sets it from `RTK=<version>`; `RTK=missing` → `""`.

- [ ] **Step 1: Write failing tests**

```go
func TestInstallScriptShape(t *testing.T)            // contains InstallURL, "RTK_TELEMETRY_DISABLED=1", "RTK_EDITION=oss", "--auto-patch", "rtk init --global --codex", never "sudo"; bash -n accepts it
func TestParseInstall(t *testing.T)                  // "RTK_VERSION=rtk 0.40.1\nRTK_HOOK=claude ok\nRTK_HOOK=codex failed" → Version "rtk 0.40.1", Hooks{claude:true, codex:false}
func TestParseGain(t *testing.T)                     // doc sample → Commands 300, SavedTokens 1200000, SavingsPct 80.0
func TestFetchGainMissing(t *testing.T)              // fake executor returns "RTK_MISSING\n" → installed=false, err=nil
// remotesvc_test.go
func TestProbeServeReportsRTK(t *testing.T)          // ProbeScript(KindServe) contains "echo RTK=" before "---LOGS---"; ParseProbe("…RTK=rtk 0.40.1\n---LOGS---") → RTKVersion "rtk 0.40.1"; "RTK=missing" → ""
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra/rtk/ ./internal/infra/remotesvc/ -run 'Install|Parse|Gain|RTK'`
Expected: FAIL.

- [ ] **Step 3: Implement `internal/infra/rtk` and the probe changes.** Before finishing, install with `RTK_EDITION=oss` on a scratch machine, confirm the result is the Apache-2.0 build (`rtk --version` or the licence file it installs), run `rtk init --help` and confirm `--auto-patch` is accepted by every `HookTargets` flavour; if one rejects it, drop the flag for that target and note it in the commit message.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/infra/rtk/ ./internal/infra/remotesvc/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/rtk/ internal/infra/remotesvc/ internal/domain/daemon.go
git commit -m "feat(rtk): install script with agent hooks, probe line and rtk gain parser"
```

---

### Task 4: graphify background build and task-file mention

**Files:**
- Create: `internal/infra/graphify/graphify.go`, `internal/infra/graphify/graphify_test.go`
- Modify: `internal/usecase/flow_manager.go:466-486` (`finaliseCreate` starts the update before model detection)
- Modify: `internal/infra/skills/engine.go:333-418` (`writeTaskFile` adds one guardrail line when graphify is available)
- Modify: `internal/infra/skills/engine_test.go` (new cases)

**Interfaces:**
- Produces:
  - `const pathPrefix = "export PATH=\"$HOME/.local/bin:$HOME/bin:$HOME/.cargo/bin:/usr/local/bin:$PATH\"; "`
  - `func ProbeCommand() string` → prints `GRAPHIFY=1` when `command -v graphify` succeeds.
  - `func Available(ctx context.Context, ex interface{ Execute(context.Context, string) (string, error) }) bool`
  - `func BackgroundUpdateScript(worktree string) string`: exit 0 unless `graphify` is on PATH and `git -C "$wt" rev-parse` succeeds; if `git -C "$wt" check-ignore -q graphify-out` fails, append `/graphify-out/` to `"$(git -C "$wt" rev-parse --git-common-dir)/info/exclude"` when absent; then `(cd "$wt" && nohup nice graphify update . >"$(git -C "$wt" rev-parse --absolute-git-dir)/aiman-graphify.log" 2>&1 </dev/null &)`.
  - `const TaskFileLine = "- **Code graph.** `graphify` is installed and a graph of this repository is being built in the background into `graphify-out/`. For codebase questions run `graphify query \"<question>\"` first; use `graphify path \"A\" \"B\"` for relationships and `graphify explain \"X\"` for one concept.\n"` (inserted right after the "Shared context" guardrail at `engine.go:385`).
- Consumes: `finaliseCreate` calls `sshMgr.Execute(ctx, graphify.BackgroundUpdateScript(session.WorktreePath))`, ignoring errors beyond a `ReportProgress` line, for both backends (it is shared by the tmux path and `launchPTYSession`).

- [ ] **Step 1: Write failing tests**

```go
// graphify_test.go — real bash, temp git repo, fake `graphify` script on PATH that writes graphify-out/graph.json and exits
func TestGraphifyScriptRunsDetached(t *testing.T)      // local executor returns < 2s even when the fake sleeps 5s first
func TestGraphifyScriptExcludesOutput(t *testing.T)    // after the fake finishes, `git status --porcelain` is empty
func TestGraphifyScriptNoopWithoutBinary(t *testing.T) // PATH without graphify: exit 0, no graphify-out, no exclude line
// engine_test.go
func TestWriteTaskFileMentionsGraphifyWhenPresent(t *testing.T) // mockRemote answers ProbeCommand with "GRAPHIFY=1" → content contains "graphify query"
func TestWriteTaskFileOmitsGraphifyWhenAbsent(t *testing.T)     // default mockRemote → no "graphify"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra/graphify/ ./internal/infra/skills/ -run 'Graphify'`
Expected: FAIL.

- [ ] **Step 3: Implement the package, the `finaliseCreate` call and the task-file line.** Teach `mockRemote.Execute` (`engine_test.go:31-44`) an opt-in `graphify bool` field that answers the probe.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/infra/graphify/ ./internal/infra/skills/ ./internal/usecase/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/graphify/ internal/infra/skills/engine.go internal/infra/skills/engine_test.go internal/usecase/flow_manager.go
git commit -m "feat(create): background graphify update after agent start and task-file pointer"
```

---

### Task 5: `remotefile.WriteIfChanged`

**Files:**
- Create: `internal/infra/remotefile/remotefile.go`, `internal/infra/remotefile/remotefile_test.go`

**Interfaces:**
- Produces:
  - `type Writer interface { Execute(ctx context.Context, cmd string) (string, error); WriteFile(ctx context.Context, path string, content []byte) error }`
  - `func WriteIfChanged(ctx context.Context, w Writer, path string, content []byte) (changed bool, err error)`: runs `sha256sum -- '<path>' 2>/dev/null || true`; if the first field equals `hex(sha256(content))` returns `false, nil` without writing; otherwise calls `WriteFile` and returns `true`. An `Execute` error, empty output, or a malformed hash counts as changed.
  - Local writers use nl-1's `fsutil.WriteIfChanged(path, content, mode)`; this package adds no local variant.

- [ ] **Step 1: Write failing tests**

```go
func TestWriteIfChangedSkipsIdentical(t *testing.T)            // fake Execute returns "<sha> /p\n": WriteFile not called, changed=false
func TestWriteIfChangedWritesOnDifference(t *testing.T)        // different sha → WriteFile called once
func TestWriteIfChangedWritesWhenHashUnavailable(t *testing.T) // Execute returns "" and, separately, an error → WriteFile called, changed=true
func TestWriteIfChangedRealBash(t *testing.T)                  // local.NewExecutor: first call writes, second identical call does not change mtime
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra/remotefile/`
Expected: FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/infra/remotefile/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/infra/remotefile/
git commit -m "feat(cache): compare-before-write helper for remote and local files"
```

---

### Task 6: Cache-hygiene audit of every scaffolding writer

**Files (writers found with `grep -rn 'WriteFile(ctx' internal cmd`, verified 2026-10-22):**
- Modify: `internal/infra/skills/engine.go:92` (bundled skill), `:417` (`.aiman_task.md`), `:469` (`.aiman_session_summary.md`), `:506,542,580,618,657,695` (`.aiman_prompt` for prompt skills)
- Modify: `internal/contextstore/files.go:536-555` (`remoteWriter` at :536 gains `Execute`; `WriteSessionPack` at :540 writes `.aiman_context.md`)
- Modify: `internal/usecase/context.go:104` (`PutSnapshotContext` note file)
- Modify: `internal/infra/skills/deepcode_install.go:41` (hook launcher; skip the `chmod` when unchanged)
- Modify: `internal/usecase/session_terminal.go:64` (`WriteMuseSessionID`, `.aiman_session_id` read by Muse hooks)
- Modify: `internal/agenthook/merge.go:18-55` (`upsertJSONFile`: when `mutate` reports a change but the marshalled bytes equal the file, return `ActionCurrent` without writing)
- Verify, change only if needed: the W1 `.aiman_todo.md` renderer (`todo.WriteRendered`) and the W6a `.aiman_handover.md` writer already use nl-1's `fsutil.WriteIfChanged` (`grep -rn 'aiman_todo\|aiman_handover' internal`).
- Already compliant, no change: `internal/aimanskill/install.go:67-91` (`EnsureFile`), `internal/agenthook/merge.go:57-106` (`writeJSONFile`, `writeTextFile`), `internal/agenthook/script.go:38-60` (`ensureReporter`).
- Create: `internal/infra/remotefile/guard_test.go`

**Interfaces:**
- Consumes: `remotefile.WriteIfChanged`, `fsutil.WriteIfChanged` (nl-1).
- Produces: `TestScaffoldingWritersCompareFirst`, an AST test that parses every non-test `.go` file under `internal/infra/skills`, `internal/contextstore`, `internal/usecase`, `internal/server` and `internal/todo` (when present), finds calls whose selector is `WriteFile` on a receiver other than the `os` package, and fails unless the enclosing function is in this allowlist (transient `/tmp` or config files, not prompt-prefix files): `DeliverInitialPrompt`, `SendPrompt`, `DeliverInitialPromptPTY`, `CaptureRestartSessionSummaryPTY`, `CreateSessionOnRemote`, `CreatePTYSession`, `SendSessionPrompt`, `ApplyKiloAllowEnv`, `EnsureClaudeWorkspaceTrusted`, `WriteIfChanged`. The failure message names the file, line and function and says to use `remotefile.WriteIfChanged`.

- [ ] **Step 1: Write the guard test and per-writer tests**

```go
func TestScaffoldingWritersCompareFirst(t *testing.T) // as above; fails today on engine.go, files.go, context.go, deepcode_install.go, session_terminal.go
// internal/infra/skills/engine_test.go — mockRemote gains sha256 answers for files it already holds
func TestPrepareSessionTwiceWritesOnce(t *testing.T)  // same issue and agent: second PrepareSession issues 0 WriteFile calls for .aiman_task.md and the skill files
// internal/contextstore/files_test.go
func TestWriteSessionPackUnchangedSkipsWrite(t *testing.T)
// internal/agenthook/install_test.go
func TestEnsureOnHostSecondRunWritesNothing(t *testing.T) // all results ActionCurrent; every file's mtime unchanged
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra/remotefile/ ./internal/infra/skills/ ./internal/contextstore/ ./internal/agenthook/ -run 'CompareFirst|Twice|Unchanged|SecondRun'`
Expected: FAIL (guard lists the writers above).

- [ ] **Step 3: Switch each listed writer to `WriteIfChanged`** (keep its existing `mkdir -p`; skip follow-up `chmod` when unchanged) and fix `upsertJSONFile`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/...`
Expected: PASS except the known Ollama test.

- [ ] **Step 5: Commit**

```bash
git add internal/
git commit -m "fix(cache): scaffolding writers leave unchanged files untouched"
```

---

### Task 7: RTK on the Agent API page, docs

**Files:**
- Modify: `internal/ui/agent_api.go:64-107` (key `t`), `:109-197` (RTK line + footer hint)
- Modify: `internal/ui/remote_services.go` (add `rtkInstallCmd` next to `remoteServiceOpCmd`)
- Modify: `internal/ui/dashboard.go` (route `rtkInstallMsg` in `Update` next to `serviceOpMsg`)
- Modify: `internal/ui/usage_view.go` (nl-4: `fetchUsageRollupCmd` also runs `rtk.FetchGain` per host and fills `usageHost.extra`)
- Test: `internal/ui/agent_api_test.go` (create if absent)
- Modify: `README.md` (Agent API keys, `repos:` bootstrap, graphify, RTK), `AGENTS.md` (Remote serve key table gains `t`; Configuration gains `repos`)

**Interfaces:**
- Consumes: `rtk.InstallScript`, `rtk.ParseInstall`, `runRemoteScript` (`remote_services.go:106`), `probeRemoteServiceCmd`, `Daemon.RTKVersion`, `m.showToast`.
- Produces:
  - `type rtkInstallMsg struct { host string; report rtk.InstallReport; err error }`
  - `func rtkInstallCmd(cfg *config.Config, host string) tea.Cmd` (runs under `remotesvc.OpTimeout`, then the model re-probes `KindServe`).
  - Key `t` on the Agent API page (free: the page binds `esc q up k down j i s ctrl+r c u ctrl+k r`). Footer hint becomes `i install/enable  s restart  c reload  u update  t install RTK  r probe  ctrl+k stop  esc back`.
  - Detail line under the selected remote: `RTK: <version>` or `RTK: not installed (t to install)`.
  - Toasts: success `✓  RTK <version> on <host> · hooks: claude, codex` (hooks that reported ok, in `HookTargets` order); wrong binary `⚠️  another "rtk" is on PATH on <host> (Rust Type Kit?)`; any failed hook adds ` · failed: <names>` and makes the toast an error.
  - `func rtkGainLine(g rtk.Gain, installed bool) string`: `rtk: <saved> tokens saved (<pct>%) over <n> commands` (tokens via nl-4's `usage.FormatTokens`); `""` when not installed. The `$` view sets `usageHost.extra` from it (the per-host dim line nl-4 reserved).

- [ ] **Step 1: Write failing tests**

```go
func TestAgentAPIKeyTStartsRTKInstall(t *testing.T)  // pressRune('t') on viewStateAgentAPI with one remote returns a non-nil cmd and sets m.loadingNext/m.state like other ops
func TestRTKInstallMsgToasts(t *testing.T)           // report{Version:"rtk 0.40.1", Hooks:{claude:true,codex:false}} → toast "✓  RTK rtk 0.40.1 on h · hooks: claude · failed: codex", isError true
func TestAgentAPIShowsRTKLine(t *testing.T)          // daemon RTKVersion "" → render contains "RTK: not installed (t to install)"
func TestRTKGainLine(t *testing.T)                   // Gain{SavedTokens:1200000, SavingsPct:80, Commands:300} → "rtk: 1.2M tokens saved (80%) over 300 commands"; installed false → ""
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ui/ -run 'AgentAPI|RTKInstall|RTKGain'`
Expected: FAIL.

- [ ] **Step 3: Implement the key, command, message routing, render line and docs.**

- [ ] **Step 4: Run the full gate**

Run: `make ci`
Expected: PASS except the known `TestOllamaIntelligenceSummariseSession`.

- [ ] **Step 5: Commit, then refresh the graph**

```bash
git add internal/ui/ README.md AGENTS.md
git commit -m "feat(ui): install RTK and its agent hooks from the Agent API page"
graphify update .
```

---

## Spec deviations

1. **`setup:` runs in the background, not inline.** The spec says bootstrap runs "after `git worktree add` in `CreateSession`". Laptop creates go through SSH `Execute` with a 30 s cap, and a dependency install routinely exceeds that. Setup runs detached with its log in the worktree's git dir, and the initial prompt tells the agent where the log is.
2. **Bootstrap applies only to worktrees this create made.** Attach, workspace reuse, ad-hoc and no-repo sessions are skipped; the spec does not say.
3. **Symlinks and `graphify-out/` are added to the repo's `info/exclude`.** Not in the spec; needed because a directory ignore pattern does not match a symlink, and graphify writes into the worktree.
4. **The graphify pointer reaches only JIRA sessions.** `.aiman_task.md` is written only when there is an issue (`internal/infra/skills/engine.go:200-205`); ad-hoc sessions get no task file. Extending the skill text instead would change a cached file on every host and was left out.
5. **RTK hooks cover six agents, not "the installed agents".** RTK has user-global hooks only for Claude Code, Codex, Cursor, Gemini, Copilot and Pi. Antigravity and Kilo Code are project-scoped in RTK and Grok, Muse Code, Deep Code and Ageni are unsupported, so those are skipped.
6. **`rtk gain` needs `--all --format json`**, and plain `rtk gain` can open a live dashboard. Task 7 runs `rtk.FetchGain` over SSH (a shell command, not a serve method, so it does not go through `aiman rpc`) and renders it in nl-4's reserved `usageHost.extra` line.
7. **"Context" in the cache-hygiene list is read as `.aiman_context.md` and snapshot notes pushed into a worktree session**, not every `contextstore.Files.Put`, whose notes carry an updated timestamp and are not in an agent's prompt prefix.

## Open questions

- `--auto-patch` is documented for the Pi/OMP flavours of `rtk init`; Task 3 Step 3 verifies the other flavours accept it before shipping.
- RTK ships under BUSL-1.1 by default with an Apache-2.0 edition (`RTK_EDITION=oss`). **Decided by Claude (open to change):** the install uses the Apache-2.0 edition. Task 3 Step 3 confirms `RTK_EDITION=oss` is honoured by the install script and that the OSS build supports the same `rtk init` flavours and `rtk gain --format json`.
