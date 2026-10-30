# Debugging a PR while reviewing it: tools for Go, TypeScript and React (late October 2026)

Researched 2026-10-30 for aiman review mode
(`docs/superpowers/specs/2026-10-30-review-mode-design.md`).

**Bottom line.** You can already step through a PR with its diff on screen.
The cheapest way: put the review worktree's `HEAD` at the merge-base and its files
at the PR head, so every editor gutter marks the PR's lines while you debug. Then
reach the remote host with VS Code Remote-SSH, or with any DAP client over an aiman
tunnel. On the agent side, two maintained MIT MCP servers can drive Delve and
js-debug headless on Linux. They are `go-delve/mcp-dap-server`, for Go, and
`debugmcp/mcp-debugger`, for Go and Node. Changed-line coverage, from `diff-cover`
or a small Go intersect, tells the agents which changed lines a test runs and where
a breakpoint will hit. No tool I found marks changed lines inside the debugger view
or restricts stepping to changed code. aiman would have to build that.

## How the facts were checked

- Repository activity, stars, licences and latest releases come from the GitHub API
  (`gh api repos/...`), read on 2026-10-30. npm versions and dates come from
  `registry.npmjs.org`, also read on 2026-10-30.
- Feature claims come from project READMEs, docs or changelogs. Where only a
  directory site or a secondary source said something, it is marked
  **[unconfirmed]**.
- "Pushed" means the date of the repository's last push, not of its last release.

---

## 1. Debuggers that agents drive

| Tool | Languages | Breakpoints, stepping, variables | Headless on remote Linux | Clients | Licence and cost | Activity |
|---|---|---|---|---|---|---|
| **go-delve/mcp-dap-server** | Go via Delve DAP; GDB is mentioned for core dumps | Yes. Line and function breakpoints, `continue` with run-to, step over/in/out, `context` (stack and variables), `evaluate`, `set-variable`, `disassemble`. 13 tools in all. | Yes. stdio only, no HTTP listener. | Any stdio MCP client: `claude mcp add mcp-dap-server <bin>`. Codex works the same way. | MIT, free | No releases. Pushed 2026-09-23. 105 stars. Owned by the Delve org. Needs Go 1.24.4 or later. |
| **debugmcp/mcp-debugger** | Python, JS/TS (bundled js-debug), Go (Delve), Rust, Java, .NET, C/C++, Ruby, COBOL, Dart | Yes. 29 tools, including `set_breakpoint`, `step_over`, `get_variables` and `evaluate_expression`. The README calls Go and JS "full step-through debugging". | Yes. stdio, or Streamable HTTP bound to `127.0.0.1:3001` by default. A `doctor` command checks toolchains. Go is disabled in its Docker image, so run it through npx on the host. | Claude Code (install script plus a bundled `skills/debugging`), and any MCP client | MIT, free | v0.25.1, 2026-09-30. Pushed 2026-10-29. 184 stars. Needs Node 22 or later. |
| **microsoft/DebugMCP** | Any DAP adapter. The VS Code extension covers Go, JS/TS, Python and more. | Yes. Breakpoints, logpoints, step in/over/out, variables, evaluate. | The extension runs inside VS Code and works under Remote-SSH (`extensionKind: workspace`). A standalone `debugmcp` CLI, in preview, needs no IDE. | The README names Copilot, Claude Code (`claude mcp add --transport http ... http://localhost:3001/mcp`), Codex, Cursor and others. | MIT, free | Pushed 2026-10-25. 577 stars. Extension 2.4.4. npm `debugmcp` 0.1.3, 2026-10-01. |
| jasonjmcghee/claude-debugs-for-you | Any language VS Code can debug | Breakpoints, stepping, evaluate | Needs VS Code open | MCP over stdio or SSE | MIT | v0.1.2, 2025-12-20. Low activity. |
| dwisiswant0/delve-mcp | Go, wrapping the Delve CLI. Also `replay` (rr) and `core`. | Yes | Yes | MCP | MIT | v0.1.0, 2025-03-28. One commit. Treat as dormant. |
| ctagard/dap-mcp, ztripez/mcp-dap | Bridges for any DAP adapter | Yes **[from directory listings]** | Yes | MCP | MIT | Pushed 2025-11-30 and 2026-02-24. 11 stars and 0 stars. Dormant. |
| **ChromeDevTools/chrome-devtools-mcp** | Browser JS | **No JS breakpoints or stepping.** It has `evaluate_script`, console messages (with source-mapped stacks), network, performance traces, 14 heap-snapshot tools, screenshots and DOM snapshot. | `--headless`, `--isolated`. It can also connect to a running Chrome over WebSocket. | Claude Code, Codex, Copilot and others | Apache-2.0, free | v1.12.0, 2026-10-22. About 54k stars. Chrome 149 (2026-06-02) declared the server stable. |
| **microsoft/playwright-mcp** | Browser | No JS stepping. It has `browser_evaluate`, console and network. `--caps=devtools` adds tracing, recording, highlight and resume. | `--headless`. `--port` for HTTP. Docker is headless Chromium only. | Any MCP client | Apache-2.0, free | v0.0.83, 2026-09-28. About 39k stars. |

Notes:

- **Node without MCP.** js-debug ships a standalone DAP server, `js-debug-dap-v1.140.0.tar.gz`
  in microsoft/vscode-js-debug release v1.140.0 (2026-09-23). Any DAP client can drive it.
  `node --inspect` on its own speaks CDP. mcp-debugger can attach to a local Node
  process and opens its inspector for you.
- **Go without MCP.** An agent can run Delve directly. `dlv test` or `dlv exec` takes an
  `--init` script holding `break file:line`, `on <bp> print expr`, `continue` and
  `exit`. That records variable values at chosen lines with no MCP layer. It is a
  documented Delve feature; I did not re-check the docs this session. Delve v1.27.2
  was released 2026-09-09 (MIT).
- **Security.** The mcp-dap-server README warns that a debugger can launch programs,
  read secrets and change or kill the debuggee. It advises a single trusted user and
  a least-privileged account. A PR under review is untrusted code, so this applies.
- **Running a browser on the remote.** Both browser MCP servers run headless Chromium
  on the remote host and need no display. Neither can pause on a line of JavaScript.
  Stepping through React code needs js-debug, which handles browser targets too, or a
  Replay recording (section 2).

## 2. Replay and time-travel debugging

| Tool | Languages | Status and direction | Platform | Licence and cost |
|---|---|---|---|---|
| **rr + `dlv replay`** | Go, and native code generally. `dlv replay <trace>` replays an rr trace with full Delve stepping, including reverse. | rr 5.10.0 released 2026-10-15; pushed 2026-10-29. 5.10.0 fixes kernels built with `CONFIG_PROC_MEM_FORCE_PTRACE=y`, such as Debian 13. | Linux only. Needs hardware performance counters: Intel Nehalem or later, some AMD Zen (some need a kernel-module workaround), some AArch64. Works in KVM and VMware guests only when the PMU is exposed; not under Xen. Many cloud VMs do not expose it. A software-counters fork exists **[unconfirmed maturity]**. | MIT-style (GitHub shows NOASSERTION). Free. |
| **Replay.io** | Browser JS and React. Node through `@replayio/node`, which is alpha, built on Node 16, and last published 2024-04-03 (**treat as dead**). | The company now builds for agents. Replay MCP is a hosted server (`https://dispatch.replay.io/nut/mcp`) with virtual breakpoints, variables, call stacks, React render analysis and Playwright step analysis on recordings. There are setup posts for Claude Code and Codex (2026-06-02) and Replay QA (2026-05 and 2026-06). Latest blog post 2026-10-06. The 2024 "new direction" post kept the human DevTools. | Recording uses Replay Chromium (`replayio record <url>`), the `@replayio/playwright` reporter (5.0.5, 2026-09-01) or a Chrome extension. Recordings upload to Replay's cloud. **No self-hosting documented.** Linux headless recording through Playwright is implied by the CI docs **[OS support not stated on the page read]**. | Proprietary SaaS. Third parties list Free, Individual about $20/month and Team about $200/month **[unconfirmed; no official price page read]**. |
| **Undo (UDB / LiveRecorder)** | C/C++, Rust, Go, Java, Kotlin (product page) | AI integration in 2026: an MCP server for UDB, plus an `ai` command that runs Claude Code. Raised $37M (SiliconAngle, 2026-06-15, via Wikipedia **[secondary]**). Docs name Claude Code, Codex, Cursor and Copilot. **Go support for the AI/MCP path is not stated [unconfirmed].** | Linux | Commercial. "AI features are not enabled in every license." Ask sales. |
| **Wallaby.js time-travel debugger** | JS/TS **inside tests** (Jest, Vitest, Mocha, node:test and others) | Active. wallaby-cli 0.9.0 published 2026-10-07. Forward and back stepping, runtime values, editing during a session. | VS Code, JetBrains, or a standalone browser UI. A CLI with agent skills is in beta. | Commercial; price not published on pages read **[unconfirmed]** |
| Node and TS in general | None found | No maintained general time-travel debugger for Node outside Replay (browser) and Wallaby (tests). rr can record the `node` binary but cannot show JavaScript-level state. | n/a | n/a |

## 3. Tools that show the PR diff while a person debugs

**VS Code GitHub Pull Requests extension.** v0.172.0 was released 2026-10-21; the
changelog already lists 0.174.0. MIT, free.

- It checks out a PR, or since 0.140.0 checks it out into a worktree, and opens
  multi-diff editors. It supports "changes since last review", gutter comment drafts
  and "Checkout on Codespace".
- The setting `githubPullRequests.quickDiff` (default `false`, needs a reload) shows
  quick-diff gutter marks for the changed lines of a checked-out PR. It first appeared
  as an experimental setting in 0.58.0.
- Debugging is ordinary VS Code debugging. Gutter marks stay visible while you step,
  because they are editor decorations.
- It works under Remote-SSH. The extension runs on the workspace side; the changelog
  mentions remote-window fixes.
- **Unconfirmed:** whether it recognises a PR checked out by aiman as a detached
  `HEAD`. Its "Pull requests checked out using `gh pr checkout` are recognized" entry
  suggests it keys on branch configuration.

**JetBrains GoLand and WebStorm 2026.2.** After checking out a PR, the IDE enters
Review mode: "pink markers in the gutter indicate the changes and the comments that
are available for review". You can comment from the gutter. The help page I read
documents this for GitLab merge requests; the 2026.2 GitHub page covers the PR
timeline and commenting. The debugger is the normal IDE debugger, so the markers
should stay visible while stepping **[not stated in docs]**.

- Remote: JetBrains Gateway 2026.2 (Linux backends only) or the Toolbox App. Remote
  development is an Ultimate or paid-IDE feature (2026.1 docs).
- Cost: commercial licence.
- The IDE debugger shows variable values inline.

**GitHub Codespaces.** It opens a PR in a codespace with the PR extension already
installed. Free allowance: 120 core-hours per month on free accounts and 180 on Pro.
Beyond that, $0.18/hour for 2 cores up to $2.88/hour for 32 cores (GitHub billing
docs). It is not useful to aiman, which already has its own host and worktree.

**Any git-aware editor, after a "soft-reset" worktree.** With `HEAD` set to the
merge-base and the files at the PR head, the editor's built-in changed-line gutter
(VS Code SCM decorations, JetBrains, gitsigns) marks exactly the PR's lines. It needs
no extension and works over Remote-SSH and while debugging. gitsigns.nvim can also
diff against any ref directly (`:Gitsigns change_base <rev>`), so the soft reset is
not needed there. Pair it with nvim-dap and Delve for a terminal-only setup. This is
a well-known gitsigns feature that I did not re-check this session.

**Marking changed lines inside the debugger, or stepping only through changed
code.** **No tool found.** The closest built-in features are:

- js-debug `skipFiles` and "just my code"
- JetBrains "step into my code"
- Delve's `stepout` with breakpoints placed only on changed lines

Building it would be simple. Take the PR's changed hunks, set breakpoints or
logpoints on those lines only, and use "continue" as "next changed line".

## 4. Which changed lines tests actually run, and inline runtime values

Changed-line ("patch") coverage:

- **diff-cover** (Bachmann1234/diff_cover). v11.0.0, 2026-10-27. Apache-2.0. Python.
  It reads Cobertura, LCOV, JaCoCo or Clover reports plus `git diff <base>...HEAD`,
  and reports uncovered changed lines, as HTML, Markdown or JSON, with a fail-under
  option.
  - Go: convert `go test -coverprofile` output with **gocover-cobertura**
    (v1.5.0, 2026-05-18, MIT).
  - TS: **c8** (v12.0.0, 2026-07-14, ISC), **Vitest** `@vitest/coverage-v8`
    (5.0.5, 2026-10-15) or nyc (v18.0.0, 2026-02-22) produce LCOV.
- **Go-native options:**
  - vearutop/gocovdiff: MIT, last release 2023, pushed 2025-06. Mostly dormant.
  - dr-dobermann/covercheck: LGPL-2.1, pushed 2026-07, 0 stars.
  - seriousben/go-patch-cover: no licence file, release 2022.

  None is a strong dependency. The intersect takes about 50 lines of Go:
  `golang.org/x/tools/cover.ParseProfiles` plus the hunks from
  `.aiman_review/diff.patch`.
- **Coverage of a running program, not just tests.** Go 1.20 and later build an
  instrumented binary with `go build -cover`. With `GOCOVERDIR=dir`, it writes
  coverage you can convert with `go tool covdata textfmt`. For Node,
  `NODE_V8_COVERAGE=dir` plus c8 does the same. This shows which changed lines a real
  run executed. These are standard toolchain features I did not re-check this session.
- `monocart-coverage-reports` (3.2.0, 2026-10-28, MIT) merges V8 coverage from
  Playwright browser runs with Node coverage. That gives React component code coverage
  from E2E runs.

Inline runtime values:

- **Wallaby.js** shows values and coverage inline in the editor for tests. Its MCP
  server and agent skills (`wallaby_runtimeValues`, `wallaby_failingTests`, coverage
  per line) give agents runtime values at a code location. The MCP server needs
  Wallaby already running in an editor or in standalone mode. The CLI and skills are a
  free beta; cloud and container tokens may become paid. Commercial.
- **Quokka.js**, by the same vendor, is a scratchpad with inline values. Pro is
  commercial. Pricing was not confirmed.
- **Console Ninja**, by the same vendor, shows `console.log` and runtime errors
  inline. Its npm stub dates from 2024; it ships as an editor extension.
- **Go:** no Quokka- or Wallaby-style tool found. The closest are the inline variable
  values in VS Code (`debug.inlineValues`) and GoLand during a debug session.

## 5. React specifics

- **React DevTools standalone.** `react-devtools` 8.0.0 was published 2026-09-09
  (MIT). It listens on `localhost:8097`. The app loads
  `<script src="http://localhost:8097">`, or `import 'react-devtools'` before
  `react-dom`.
  - Environment variables `HOST`, `PORT`, `KEY` and `CERT` (for HTTPS) control the
    server.
  - `REACT_DEVTOOLS_CLIENT_HOST`, `REACT_DEVTOOLS_CLIENT_PORT` and
    `REACT_DEVTOOLS_CLIENT_USE_HTTPS` set what the served client script connects to,
    for use behind a proxy or tunnel.
  - So the app can run on the remote host while DevTools runs on the laptop, with
    port 8097 forwarded.
- **Agent-readable React state.** `react-devtools-cdt-mcp` 0.1.0, published
  2026-09-09, is official and lives in the react/react repository. It is an
  experimental page library that registers React tools with chrome-devtools-mcp 1.3
  and later through the third-party-tools API (`--categoryExperimentalThirdParty=true`).
  - The tools include `react_get_component_tree` (stable uids), component inspection
    (props and hooks) and profiling.
  - You import `react-devtools-cdt-mcp/register` before React, in development only.
  - Related upstream PRs are still landing: facade BigInt normalisation, #37603, open.
- **Replay MCP** also does React tree, render and "wasted render" analysis, but only
  on cloud recordings.
- Third-party React MCP servers (skylarbarrera/react-devtools-mcp with 1 star,
  agent-debug-mcp) are small. Skip them.
- **Component-level tracing.** `<Profiler onRender>` gives commit timings. The
  DevTools profiler shows "why did this render". `react-devtools-cdt-mcp` exposes
  profiling to agents.
- **Debugging a React app served from the remote worktree:**
  1. Run the dev server (Vite, Next) on the remote host, bound to 127.0.0.1.
  2. Forward its port to the laptop: an aiman tunnel, `ssh -L`, or VS Code
     Remote-SSH auto-forwarding.
  3. Open it in the laptop's Chrome. The React DevTools extension and the Sources
     panel work normally, and source maps resolve to the served sources.
  4. For breakpoints from the IDE, use VS Code's js-debug "chrome" launch
     configuration against the forwarded URL, with `webRoot` mapped to the Remote-SSH
     workspace.
  5. Agents use headless Chromium on the remote host itself: chrome-devtools-mcp or
     Playwright MCP for state and console, and js-debug through mcp-debugger when
     they need real breakpoints.

---

## Implications for aiman review mode

The context that shapes the options:

- **Where the code runs.** Review mode has a worktree `<repo>@pr-<n>` on the remote
  host, detached at the PR head, plus `.aiman_review/diff.patch` and `files.txt`.
  Sessions already carry `Tunnels` (laptop↔remote port forwards), and mutagen mirrors
  the worktree to `~/.aiman/work/<ID>` on the laptop. The toolchains (Go, Node) live on
  the remote host, so execution should stay there.
- **rr on this host.** The host this research ran on has an Intel Xeon E5-2667 v3
  (Haswell, which rr supports), runs on bare metal, and has
  `kernel.perf_event_paranoid=3` (the Debian default). rr would need that lowered to 1
  or `CAP_PERFMON`. Neither rr nor dlv is installed; node and go are.

### (a) Operator steps through the PR with the diff visible

Ranked by value for the effort.

1. **A "diff-visible" review worktree. High value, small effort.**
   - Create the review worktree, then `git reset --soft $(git merge-base origin/<base> <head>)`.
     `HEAD` now sits at the merge-base and the files stay at the PR head. Every
     editor's changed-line gutter then marks exactly the PR's lines, during debugging
     too, with no extension. That includes VS Code over Remote-SSH, GoLand over
     Gateway, gitsigns, and the mutagen mirror on the laptop.
   - `review refresh` reapplies it.
   - Cost: `git status` shows the PR as uncommitted. The review prompt must say so,
     and nothing may commit in that worktree, which review mode already forbids.
   - Option: record the base SHA in `meta.json` instead of resetting, and leave the
     gutter base to editors that support any ref (gitsigns `change_base`, the VS Code
     PR extension `quickDiff`).
2. **One-key "open in editor" plus debug tunnels. High value, medium effort.**
   - From the review session, aiman prints or opens
     `vscode://vscode-remote/ssh-remote+<host><worktree>`. On request, it starts a
     headless DAP server on the remote host and adds a tunnel:
     - Go: `dlv dap --listen=127.0.0.1:<p>` or `dlv test --headless`.
     - Node: `node --inspect=127.0.0.1:<p>`.
     - React: the dev server port plus 8097 for React DevTools.
   - Write a `.vscode/launch.json` into the worktree with attach configs for those
     ports. The operator then presses F5 in VS Code. The same tunnel serves GoLand's
     "Go Remote" config or nvim-dap.
3. **Breakpoints on changed lines. Medium value, medium effort.**
   - aiman writes `.vscode/launch.json` plus a breakpoint list (VS Code cannot import
     breakpoints from a file; a DebugMCP or extension call can set them), or a Delve
     `--init` file. That puts a breakpoint on the first line of each changed hunk on
     the brief's reading order.
   - "Continue" then walks the operator through the changed code in reading order.
     This approximates the missing "step only through changed code" feature.
4. **The VS Code GitHub Pull Requests extension with `githubPullRequests.quickDiff`.**
   Low effort, medium value, but its recognition of aiman's detached-`HEAD` checkout
   is unconfirmed. Recommend it as optional in the brief, behind option 1.
5. **Not worth it now.**
   - Codespaces: duplicates the host aiman already has.
   - JetBrains Gateway as the default: licence cost, but supported for operators who
     already own it.
   - rr replay for the operator: perf-counter setup and Linux only. Revisit if flaky,
     timing-dependent PRs become common.

### (b) Reviewing agents execute changed paths and put real values in the brief

Ranked by value for the effort.

1. **Changed-line coverage as a verification step. Highest value, small effort.**
   - The primary runs the PR's tests with coverage:
     - Go: `go test -coverprofile`.
     - TS: `vitest --coverage` or c8 producing LCOV.
   - aiman (or the skill) intersects the coverage with `.aiman_review/diff.patch`.
     That needs a small Go helper `aiman review coverage`, or `diff-cover` through
     gocover-cobertura.
   - Output per hunk: lines executed / changed, and the tests that executed them.
   - The brief gains "changed but never executed by tests" lines. These are strong
     candidates for `question` findings, and they tell the agent where breakpoints
     will actually hit.
   - No MCP is needed, all of it is deterministic, and it is easy to eval with a fake.
2. **Value capture on changed lines with no MCP. High value, small to medium effort.**
   - Go: generate a Delve `--init` script (`break f.go:N`, `on N print <locals>`,
     `continue`) for the covering test from step 1, and run `dlv test` headless. Paste
     the values into the evidence of the matching finding or brief item.
   - Node: js-debug logpoints through mcp-debugger, or `node --inspect` with a short CDP
     script.
   - This fits the spec's rule "verified: reproduced, or tested, or else a question".
     Real values at the changed line count as evidence.
3. **Interactive debugging MCP for the agents. Medium value, small effort to enable.**
   - Install `@debugmcp/mcp-debugger` (Go and Node, stdio, `doctor`, actively
     released) on the remote host, and register it for review sessions only.
   - `go-delve/mcp-dap-server` is the leaner Go-only alternative from the Delve org.
     It has no tagged releases, so pin a commit.
   - Run both under the session user. The reviewed PR is untrusted code, so keep the
     review-session sandbox rules and never expose the HTTP transport off localhost.
   - aiman's `agenthook` setup already writes per-agent config, so this is config
     work. Add an eval that a review session's agent config lists the server.
4. **React state for UI PRs. Medium value, medium effort.**
   - chrome-devtools-mcp 1.12, headless on the remote host, against the worktree's dev
     server gives console, network, screenshots and `evaluate_script`.
   - Add `react-devtools-cdt-mcp` (experimental, development import) for the component
     tree, props and hooks.
   - The agents then quote real props and state for changed components. Only enable it
     when the repo has a dev server command (`repos.<slug>.setup`).
5. **Time travel. Low priority.**
   - Replay MCP is the most capable agent time-travel debugger for React. But
     recordings go to Replay's cloud, it is paid, and Node support is dead. That
     conflicts with private repos, so leave it opt-in per repo at most.
   - rr plus `dlv replay` (free, Linux) suits Go flakes, but needs
     `perf_event_paranoid` ≤ 1 on the host.
   - Undo is commercial; its Go support on the AI path is unconfirmed.

**Recommended first slice.** Ship (a)1 and (b)1 together. The worktree's gutter shows
the PR in any editor. The brief gets a per-hunk "executed by tests" table that both
the operator and the agents navigate by. Then add (a)2 for tunnels and `launch.json`,
and (b)2 for Delve or js-debug value capture. Leave MCP debugger servers, React MCP
and time travel as opt-in extras.

## Sources (read 2026-10-30)

- go-delve/mcp-dap-server: https://github.com/go-delve/mcp-dap-server ; https://pkg.go.dev/github.com/go-delve/mcp-dap-server
- debugmcp/mcp-debugger: https://github.com/debugmcp/mcp-debugger (README, v0.25.1 2026-09-30)
- microsoft/DebugMCP: https://github.com/microsoft/DebugMCP
- jasonjmcghee/claude-debugs-for-you: https://github.com/jasonjmcghee/claude-debugs-for-you
- dwisiswant0/delve-mcp: https://github.com/dwisiswant0/delve-mcp
- ztripez/mcp-dap (https://glama.ai/mcp/servers/ztripez/mcp-dap), ctagard/dap-mcp (GitHub API metadata)
- Chrome DevTools MCP: https://github.com/ChromeDevTools/chrome-devtools-mcp (releases; `docs/tool-reference.md`); https://developer.chrome.com/blog/new-in-devtools-149 (2026-06-02)
- Playwright MCP: https://github.com/microsoft/playwright-mcp (README); https://playwright.dev/mcp/introduction
- vscode-js-debug releases: https://github.com/microsoft/vscode-js-debug/releases (v1.140.0, 2026-09-23)
- Delve: https://github.com/go-delve/delve (v1.27.2, 2026-09-09); `dlv replay`: https://github.com/go-delve/delve/blob/master/Documentation/usage/dlv_replay.md
- rr: https://github.com/rr-debugger/rr (5.10.0, 2026-10-15); hardware requirements as mirrored at https://gitee.com/jonaszhou/rr
- Replay: https://replay.io/blog/a-new-direction (2024-07-24); https://www.replay.io/blog (posts up to 2026-10-06); https://docs.replay.io/basics/replay-mcp/overview ; https://docs.replay.io/basics/getting-started/record-your-app ; https://docs.replay.io/reference/replay-runtimes/replay-node ; pricing (third-party, unconfirmed): https://pricingsaas.com/companies/replay
- Undo: https://docs.undo.io/UndoAI.html ; https://undo.io/resources/time-travel-ai-code-assistant/ ; https://en.wikipedia.org/wiki/Undo_(company)
- VS Code GitHub PR extension: https://github.com/microsoft/vscode-pull-request-github (CHANGELOG.md, package.json `githubPullRequests.quickDiff`, v0.172.0 2026-10-21)
- GoLand 2026.2: https://www.jetbrains.com/help/go/2026.2/work-with-github-pull-requests.html ; https://www.jetbrains.com/help/go/2026.2/work-with-gitlab-merge-requests.html ; remote dev FAQ https://www.jetbrains.com/help/rider/2026.2/FAQ_about_remote_development.html ; https://www.jetbrains.com/help/idea/2026.1/faq-about-remote-development.html
- Codespaces: https://docs.github.com/en/enterprise-cloud@latest/codespaces/developing-in-a-codespace/using-github-codespaces-for-pull-requests ; https://docs.github.com/ja/billing/managing-billing-for-github-codespaces/about-billing-for-codespaces
- diff-cover: https://github.com/Bachmann1234/diff_cover (v11.0.0 2026-10-27); gocover-cobertura: https://github.com/boumenot/gocover-cobertura ; gocovdiff: https://github.com/vearutop/gocovdiff ; covercheck: https://pkg.go.dev/github.com/dr-dobermann/covercheck ; c8: https://github.com/bcoe/c8 ; nyc: https://github.com/istanbuljs/nyc ; npm registry entries for vitest, @vitest/coverage-v8, monocart-coverage-reports
- Wallaby: https://wallabyjs.com/docs/features/time-travel-debugger.html ; https://wallabyjs.com/docs/features/mcp/ ; https://wallabyjs.com/blog/wallaby-mcp.html
- React DevTools: https://github.com/react/react/tree/main/packages/react-devtools (README; npm 8.0.0 2026-09-09); react-devtools-cdt-mcp: https://github.com/react/react/tree/main/packages/react-devtools-cdt-mcp (npm 0.1.0 2026-09-09); https://github.com/react/react/pull/36597 ; https://azukiazusa.dev/en/blog/react-devtools-cdt-mcp
