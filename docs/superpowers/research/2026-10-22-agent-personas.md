# Reusable agent personas: evidence, prior art, and a design for aiman

Date: 2026-10-22. Scope: the last ~12 months of research and tooling, plus older papers where they are still the main evidence.

**Verdict.** The "you are a world expert in X" sentence does not reliably improve coding output on current models. It is roughly neutral, sometimes slightly negative, and longer identity text tends to hurt more. What improves coding-agent output is the *content* a persona file can carry: domain checklists, the right tools and MCP servers, a definition of done, a review rubric, examples, and a well-chosen model/effort. aiman should therefore treat a persona as a **portable bundle of task context and launch settings**, not as an identity prompt. The name ("EVM security auditor") is a handle for routing and humans; the body is a checklist.

Labels used below: **[Evidence]** is a published result or official documentation. **[Practitioner]** is reported experience or vendor guidance without controlled measurement. **[Opinion]** is my recommendation.

---

## 1. Does persona prompting improve coding-agent output?

### 1.1 Research

| Study | Setting | Finding |
|---|---|---|
| Zheng et al., *When "A Helpful Assistant" Is Not Really Helpful* (EMNLP Findings 2024) [arXiv 2311.10054](https://arxiv.org/abs/2311.10054v2) | 162 roles, 4 model families, 2,410 factual questions | **[Evidence]** Personas in system prompts did not improve aggregate accuracy vs no persona; effects were none or small negative. Best-persona-per-question helps in hindsight, but automatic persona selection was no better than random, so the per-question effect is "largely random". |
| Basil, Shapiro, Mollick et al., *Prompting Science Report 4: Playing Pretend* (Wharton GAIL, Dec 2025) [arXiv 2512.05858](https://arxiv.org/pdf/2512.05858), [GAIL page](https://gail.wharton.upenn.edu/research-and-insights/playing-pretend-expert-personas/) | 6 models, GPQA Diamond and MMLU-Pro, in-domain experts, off-domain experts, low-knowledge personas | **[Evidence]** Expert personas did not reliably improve accuracy; most conditions were statistically indistinguishable from baseline (Gemini 2.0 Flash on MMLU-Pro the main exception). Low-knowledge personas ("toddler", "layperson") often *reduced* accuracy. Authors recommend task-specific instructions over persona assignment; personas may still be useful for tone. |
| Hu, Rostami, Thomason (USC), *Expert Personas Improve LLM Alignment but Damage Accuracy* (PRISM, preprint Mar 2026) [arXiv 2603.18507](https://arxiv.org/abs/2603.18507), [The Register coverage](https://www.theregister.com/2026/03/24/ai_models_persona_prompting/) | MMLU, a generative benchmark incl. coding, 3 safety benchmarks | **[Evidence]** Personas help alignment-type tasks (a "Safety Monitor" persona raised JailbreakBench refusals from 53.2% to 70.9%) but hurt pretraining-knowledge tasks: MMLU 68.0% with expert persona vs 71.6% base; coding scores dropped 0.65 points on a 10-point scale. Longer persona descriptions did the most damage. Co-author guidance: generic personas like "expert full-stack developer" are probably unnecessary; detailed project requirements may help. |
| Kim, Yang, Jung, *Persona is a Double-edged Sword* (IJCNLP Findings 2025) [arXiv 2408.08631](https://arxiv.org/abs/2408.08631) | 12 reasoning datasets | **[Evidence]** Role-play prompts degraded Llama 3 reasoning on 7 of 12 datasets. Their fix (Jekyll & Hyde) runs persona and neutral prompts and lets an evaluator pick; LLM-generated, instance-specific personas were more stable than handcrafted ones. Takeaway: the gain comes from ensembling plus selection, not from the persona. |
| Kong et al., *Better Zero-Shot Reasoning with Role-Play Prompting* (NAACL 2024) [arXiv 2308.07702](https://arxiv.org/abs/2308.07702v2) | ChatGPT-era models, 12 reasoning benchmarks | **[Evidence]** Positive: e.g. AQuA 53.5% to 63.8%. Authors attribute it to role-play acting as an implicit chain-of-thought trigger. **[Opinion]** Current reasoning models and agent harnesses already reason before acting, so this mechanism has little left to add in 2026. |
| Guo et al., *Personality-Guided Code Generation* (ACL 2025) [aclanthology 2025.acl-long.54](https://aclanthology.org/2025.acl-long.54) | 7 LLMs, 4 code datasets | **[Evidence]** Task-matched *personality traits* (not "expert" titles) improved pass rates in 23 of 28 model/dataset pairs, up to +12.9%. Single-function benchmarks, not agentic repo work, and mostly pre-2025 models. No study I found compares this to plain task instructions on the same agentic benchmark. |

### 1.2 Vendor and practitioner guidance

- **[Practitioner]** Anthropic's prompting guide still recommends giving Claude a role in the system prompt because it "focuses Claude's behavior and tone" ([Prompting best practices](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices)). Its example is one plain sentence ("a helpful coding assistant specializing in Python"), not a superlative backstory. Read this as a scoping device, not an accuracy booster.
- **[Practitioner]** Anthropic's context-engineering guidance pushes the other way from identity text: give the minimal set of high-signal tokens, at the "right altitude" between brittle if-then rules and vague exhortation, and add instructions in response to observed failures ([Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)).
- **[Practitioner]** The structural benefit of "role agents" in Claude Code, Codex, Copilot and Cursor is **context isolation and tool scoping**: a subagent gets its own context window, a narrower tool list, and its own model ([Claude Code subagents](https://code.claude.com/docs/en/sub-agents), [Codex subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents)). That is a mechanical effect, independent of the role sentence.
- **[Practitioner]** Multi-agent setups cost a lot of tokens: Anthropic reported its multi-agent research system used about 15x the tokens of a chat interaction ([How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)). Persona-per-worker fan-out inherits this cost.
- **[Practitioner]** Security vendors packaging domain expertise for agents ship *procedures and tool integrations*, not identities. Trail of Bits' Claude Code marketplace offers `building-secure-contracts`, `entry-point-analyzer`, `static-analysis` (CodeQL/Semgrep), `variant-analysis` and similar, and warns that AI security checks are incomplete and error-prone ([trailofbits/skills](https://github.com/trailofbits/skills)).

### 1.3 What actually drives quality

**[Evidence + Opinion]** Ranked by how strongly the evidence supports it for agentic coding:

1. **Verification loops and tools.** Tests, fuzzers, static analysers, a reproducible failing case. These turn opinions into checkable facts. An "EVM expert" without `forge` and `slither` on the host is just a confident narrator.
2. **Domain checklists and failure catalogues.** Concrete, enumerable concerns (read-only reentrancy, ERC-4626 inflation, EIP-712 replay) are exactly the "detailed requirements" the PRISM authors say help, and they are what an expert reviewer would actually check.
3. **Definition of done and output contract.** What the agent must produce (PoC test per High finding, root-cause note, regression test) and the format a downstream consumer parses (the verifier's findings marker).
4. **Project context.** Architecture, conventions, invariants (`.aiman_task.md`, `.aiman_context.md`, AGENTS.md).
5. **Model and effort choice.** A bigger model or higher effort on hard review is a real lever; aiman already has `agent_defaults`.
6. **Independent second opinion.** A different model family as reviewer reduces correlated errors (**[Opinion]**, consistent with the Jekyll & Hyde ensembling result).
7. **The identity sentence.** Neutral to slightly negative for accuracy; mildly useful for tone and scope. Keep it to one plain line or drop it.

---

## 2. Prior art: how tools package roles today

| Tool | Format and location | Fields that matter | Notes |
|---|---|---|---|
| **Claude Code subagents** | Markdown + YAML frontmatter in `.claude/agents/` (project) or `~/.claude/agents/` (user); also `--agents` JSON, managed settings, plugins ([docs](https://code.claude.com/docs/en/sub-agents)) | `name`, `description` (required); `tools`, `disallowedTools`, `model`, `effort`, `permissionMode`, `maxTurns`, `skills` (full text preloaded), `mcpServers`, `hooks`, `memory`, `isolation: worktree`, `initialPrompt` | Unknown fields are ignored. Subagents get the body as their prompt, *not* the full Claude Code system prompt. Running one as the main agent (`--agent`) replaces the default system prompt, which drops Claude Code's built-in engineering guidance. |
| **Claude Code output styles** | Markdown in `.claude/output-styles/` ([docs](https://code.claude.com/docs/en/output-styles)) | `keep-coding-instructions` (default false) | Changes tone/format of the main session. A custom style drops coding guidance unless that flag is true. Tone-only; wrong tool for expertise. |
| **Agent Skills (SKILL.md)** | Folder with `SKILL.md` + optional `scripts/`, `references/`, `assets/`; spec at [agentskills.io](https://agentskills.io/specification) | `name`, `description` required; `license`, `compatibility`, `metadata`, `allowed-tools` optional | Progressive disclosure: only name+description sit in context until invoked. Published as an open standard Dec 2025; reported adoption by Codex, Copilot, Cursor, Gemini CLI, Junie and others ([adoption summary](https://codex.danielvaughan.com/2026/05/05/agent-skills-open-standard-portable-skills-codex-cli-cross-agent/)). The most portable unit available. |
| **OpenAI Codex custom agents** | One TOML file per agent in `~/.codex/agents/` or `.codex/agents/` ([docs](https://learn.chatgpt.com/docs/agent-configuration/subagents)) | `name`, `description`, `developer_instructions` required; `model`, `model_reasoning_effort`, feature switches, `skills.config` | `sandbox_mode`, `approval_policy`, `mcp_servers` in the agent file do **not** override the parent; MCP must be configured on the parent. Agents are spawned when asked by name or by AGENTS.md/skill instructions. |
| **GitHub Copilot custom agents** | `.github/agents/<name>.agent.md` (repo, org, enterprise levels) ([config reference](https://docs.github.com/en/copilot/reference/custom-agents-configuration), [CLI concepts](https://docs.github.com/en/enterprise-cloud@latest/copilot/concepts/agents/copilot-cli/about-custom-agents)) | `description` required; `name`, `tools` (aliases `read`, `edit`, `search`, `execute`, `agent`, `web`, `todo`), `model`, `mcp-servers`, `disable-model-invocation`, `user-invocable`, `metadata` | Body up to 30,000 characters. Selectable from the CLI agent picker. |
| **Cursor subagents** | Markdown + frontmatter in `.cursor/agents/` or `~/.cursor/agents/`; project wins on name clash ([forum thread](https://forum.cursor.com/t/feature-request-global-user-level-custom-agents/164036)) | `name`, `description`, `model`, `readonly` | Reported bugs: nested markdown under `.cursor/agents/` is mis-detected as agents; new project agents sometimes missing from the Task enum ([1](https://forum.cursor.com/t/nested-markdown-files-inside-cursor-agents-are-incorrectly-detected-as-subagents/166296), [2](https://forum.cursor.com/t/committed-project-custom-subagents-missing-from-task-enum-invalid-enum-on-cursor-3-12-17/166135)). |
| **Kilo Code / Roo Code modes** | Legacy `.kilocodemodes` / `custom_modes.yaml`; the CLI-based Kilo now uses agent Markdown files and migrates old modes ([Kilo custom modes](https://kilo.ai/docs/customize/custom-modes)) | `slug`, `roleDefinition` (prepended to system prompt), `whenToUse` (used by the Orchestrator for routing), `customInstructions`, `groups` (tool groups, `fileRegex` edit restrictions) | `groups` + `fileRegex` is a good idea worth copying: a reviewer persona cannot edit source. |
| **BMAD-METHOD** | `src/bmm/agents/*.agent.yaml` (v6): metadata, persona, menu ([docs](https://docs.bmad-method.org/cs/explanation/named-agents/), [agents overview](https://www.mintlify.com/bmad-code-org/BMAD-METHOD/reference/agents/overview)) | Named roles (Mary analyst, Winston architect, Amelia dev, Quinn QA, John PM, Bob SM) | Value is in the *workflow artifacts* passed between roles (PRD, architecture, stories), not the names. |
| **CrewAI** | `config/agents.yaml` with `role`, `goal`, `backstory`; newer per-agent JSONC ([docs](https://docs.crewai.com/v1.14.2/en/concepts/agents)) | `{topic}` interpolation from kickoff inputs | Backstory is exactly the long identity text the evidence says to avoid for accuracy work. |
| **Community libraries** | [VoltAgent/awesome-claude-code-subagents](https://github.com/VoltAgent/awesome-claude-code-subagents) (100+ agents), [wshobson/agents](https://github.com/wshobson/agents) (reported ~190 agents in plugins, plus skills and commands) | Mostly Claude subagent markdown | **[Opinion]** Quality varies widely; many files are long identity prose plus generic bullet lists. Large libraries also degrade routing: every installed agent's `description` competes for the delegation decision. |
| **Rules (Continue, Cursor rules, AGENTS.md)** | `.continue/rules/*.md`, `.cursor/rules/*.mdc`, `AGENTS.md` ([Continue rules](https://docs.continue.dev/customize/deep-dives/rules)) | `globs`, `alwaysApply`, `description` | Glob-scoped activation is the useful idea: a Solidity checklist only when `*.sol` is touched. |

**What good persona files have in common [Opinion, from reading the above]:** a precise `description` (when to use it; this is what routing sees), a short mission line, explicit tool scope, a workflow, a checklist, an output contract, and references kept out of the always-loaded body. Bad ones are long, superlative, generic ("write clean, maintainable code"), and assume tools that are not installed.

**Portability [Evidence + Opinion].** SKILL.md is the only format read natively by most of aiman's vendors. Native agent files (Claude, Codex, Copilot, Cursor, Kilo) all share the shape "frontmatter with name/description/model/tools + markdown body", so one source can be rendered to each, but field semantics differ (Codex ignores per-agent MCP and sandbox; Copilot tool aliases differ from Claude tool names; Claude's `--agent` replaces the system prompt). Grok, Muse, Pi and DeepCode have no documented agent-file format I could verify; for those the only portable path is a skill or a prompt preamble.

---

## 3. Design recommendations for aiman [Opinion]

### 3.1 Principles

1. **Persona = bundle, not identity.** Checklist, tools, rubric, launch settings. One plain mission line at most. No superlatives ("genius", "world's leading").
2. **One source file, rendered per vendor.** aiman owns a superset format; it generates SKILL.md and native agent files. Users never hand-maintain five copies.
3. **Portable path first, native path as an optimisation.** Every vendor gets the persona through files aiman already controls (worktree file + skill). Native subagent files are used only where they add something real (tool restriction, separate model, context isolation for an in-session reviewer).
4. **Byte-stable text.** No interpolation inside the persona body, so the prefix caches across turns, restarts and fan-out workers.
5. **Tools are part of the persona contract.** A persona declares required binaries/MCP servers and aiman checks them on the host before launch.

### 3.2 Format: `PERSONA.md`

A directory per persona, mirroring the Agent Skills layout so references load on demand:

```
<root>/personas/evm-security-auditor/
  PERSONA.md            # frontmatter + body (body target <= ~1,500 tokens)
  references/           # loaded only when the agent asks (checklists, past findings)
  scripts/              # optional helpers (e.g. run-static-analysis.sh)
```

Frontmatter fields:

| Field | Type | Purpose |
|---|---|---|
| `name` | slug | Identifier; also the generated skill/agent name (`aiman-persona-<name>`). |
| `description` | string | When to use it. Feeds routing (Claude/Codex/Copilot descriptions, Kilo `whenToUse`) and the wizard picker. |
| `version` | int | Bump on change; stored on the session so a restart can say "persona changed since launch". |
| `kind` | `worker` \| `reviewer` \| `both` | Reviewer personas are eligible for `aiman verify`; they default to read-only. |
| `applies_to` | globs | Auto-suggest in the wizard and auto-select a reviewer from the diff (`**/*.sol`, `foundry.toml`). |
| `agent.prefer` | list of agent binaries | Ordered preference; first installed one wins. |
| `agent.models` | map binary to model | Overrides `agent_defaults[binary].model` for this persona. |
| `agent.effort` | string | Overrides `agent_defaults[binary].effort`. |
| `tools.require` / `tools.optional` | binary names | Checked on the remote (`command -v`) at create; missing required tools block or warn. |
| `tools.allow` / `tools.deny` | abstract tool classes (`read`, `edit`, `execute`, `web`) | Mapped per vendor: Claude `tools`, Copilot aliases, Kilo `groups`, Codex feature switches. |
| `edit_scope` | globs | Kilo-style `fileRegex`: reviewers get `[]` (no edits) or `[".aiman_review.md"]`. |
| `mcp` | list of server names | Must be configured on the host; Codex needs them on the parent session. |
| `skills` | list of skill names | Preloaded (Claude `skills:`) or named in the preamble for other vendors. |
| `todos.seed` | list of strings | Optional to-dos added at create with author `aiman` (see 3.5). |
| `review.severities` | ordered list | Severity scale the verifier parser accepts. |
| `review.required_at` | severity | Findings at or above this become REQUIRED worker to-dos. |

Body sections (fixed headings so aiman can extract them): `## Mission`, `## Workflow`, `## Checklist`, `## Definition of done`, `## Review rubric`, `## References`. The verifier brief pulls `## Review rubric` and `## Checklist`; worker sessions get the whole body.

Precedence (first match wins): repo `.aiman/personas/` in the worktree, then user `~/.aiman/personas/` on the laptop (pushed to the remote the same way the bundled skill is ensured on the host), then personas bundled with aiman. The shared context store is the wrong home: it holds notes that change, and personas are versioned configuration that should be reviewed like code. Context-store notes may *reference* a persona (`persona: evm-security-auditor`) but should not contain one.

### 3.3 Applying a persona per vendor

Persisted state: `Session.Persona` and `Session.PersonaVersion`, COALESCE-protected in SQLite like the other identity fields (AGENTS.md gotcha 4), and carried through `session.register`.

At **session create** (`FlowManager.CreateSession`), for every vendor:

1. Resolve the persona and run the tool check. Required tool missing: stop with a message naming it, unless the user overrides in the summary screen.
2. Resolve launch settings with this precedence: explicit wizard choice, then persona `agent.*`, then `agent_defaults`, then vendor default.
3. Write `.aiman_persona.md` into the worktree (body only, "DO NOT COMMIT" header, covered by the existing `.aiman_*` ignore glob).
4. Install the rendered persona as a skill `aiman-persona-<name>` in user-level skill dirs on the host (same mechanism as `internal/aimanskill`), so references load on demand and the agent can re-read it after compaction.
5. Prepend one line to the one-line initial prompt: `Read .aiman_persona.md, then .aiman_task.md.` Keep the persona out of `.aiman_task.md` so the task file can change without touching the persona prefix.

Optional native rendering (only where it buys something):

| Vendor | Native target | When to use it |
|---|---|---|
| Claude Code | `~/.claude/agents/aiman-<name>.md` with `tools`, `model`, `effort`, `skills`, `mcpServers` | For **reviewer** personas callable *inside* a worker session (the worker delegates "have aiman-evm-security-auditor check this diff"). Do not launch workers with `claude --agent`, which replaces Claude Code's system prompt and loses its engineering guidance. |
| Codex | `~/.codex/agents/aiman_<name>.toml` with `developer_instructions`, `model`, `model_reasoning_effort` | Same in-session reviewer use. MCP must already be on the parent. |
| Copilot CLI | `.github/agents/aiman-<name>.agent.md` is repo-scoped, so prefer the user-level location if the CLI exposes one; otherwise skip | Repo-scoped files risk being committed. |
| Cursor CLI | `~/.cursor/agents/aiman-<name>.md` (top-level file only; references outside that dir because of the nested-file bug) | In-session reviewer. |
| Kilo | Agent markdown, map `tools.allow` and `edit_scope` to permission rules | Edit-scope enforcement for reviewers. |
| Grok, Muse, Pi, DeepCode | none verified | Portable path only. |

Write native files to user-level dirs on the host with an `aiman-` prefix, never into the worktree, for the same reason the bundled skill lives in `$HOME`.

At **restart** and **agent switch** (`restartSession`): re-resolve the persona (repo version may have changed), re-render for the new vendor, re-apply persona model/effort for the new binary, and add the same `Read .aiman_persona.md` line to the handoff prompt. If `PersonaVersion` changed, say so in the handoff ("persona updated: re-read it"). The worktree file is rewritten only if its content changed.

**Changing persona mid-session** should be a restart, not a steering note. Injecting a new persona into a live conversation contradicts the earlier one and invalidates the cached prefix.

### 3.4 Composition with the verifier (W5)

- A reviewer persona supplies the brief's rubric and checklist; the verifier keeps owning the output contract (findings marker, REQUIRED/OPTIONAL) so `ParseReview` stays vendor- and persona-independent. Map persona severities to REQUIRED with `review.required_at`.
- Reviewer selection: `verify.reviewer_persona` in config, else the highest-priority `kind: reviewer|both` persona whose `applies_to` matches the diff, else a generic reviewer. Several matches: run the most specific one, not all of them (cost).
- Reviewer sessions get `tools.deny: [edit]` and `edit_scope: [".aiman_review.md"]` rendered natively where supported, plus a sentence in the brief for vendors without enforcement.
- Keep cross-vendor selection as the verifier already plans: persona `agent.prefer` is a preference applied *after* "different vendor from the worker".

### 3.5 Composition with to-dos (W1)

- `todos.seed` adds items at create with author `aiman`, which nl-1 already treats as non-steering for the creating write. Keep seeds to 1–3 process steps that are always true for the persona ("Run `forge test` and `slither .` before declaring done"). The checklist itself should not become to-dos; a 40-item to-do list is noise.
- Verifier findings already become worker to-dos; persona rubrics make those findings consistent across reviews, which also makes the existing duplicate-finding check work better.

### 3.6 Composition with spec fan-out (W8)

- The planner may tag each to-do with a persona (`- [ ] Add ERC-4626 vault  {persona: evm-security-auditor}` or a `persona` field on `TodoItem` once the shape is settled). `spec.fanout` validates that the persona exists on the host and that its required tools are installed before creating any child.
- Default: children inherit the planner's persona. Planner personas are rarely the right worker persona, so the wizard should prompt.
- Fan-out cost scales with workers times turns; persona text is a small part of it if kept under ~1,500 tokens and byte-stable (see 3.7).

### 3.7 Cache and token cost

- **[Evidence]** Prompt caches match on exact prefixes. Anthropic caches in the order tools, system, messages, with cache reads billed at a fraction of base input ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)); OpenAI caches automatically for long prompts on exact prefix match ([OpenAI prompt caching](https://platform.openai.com/docs/guides/prompt-caching)).
- **[Opinion]** Consequences for aiman:
  - Put the persona at the start of the session (first file read, or native agent prompt) and never re-inject it per turn. Steering notes append at the end and leave the prefix intact.
  - No timestamps, session IDs, branch names or issue keys in persona text. Those belong in `.aiman_task.md`. Byte-identical persona text across fan-out workers on the same vendor and account maximises shared prefix hits.
  - Skills cost only name+description until invoked; prefer `references/` over a long body.
  - Preloading skills into Claude subagents (`skills:`) injects their *full* text; list only what the persona always needs.
  - A persona that changes the model forfeits the cache of the session it replaces; that is acceptable at restart, not mid-task.
  - Budget: body <= ~1,500 tokens, description <= ~300 characters, at most ~5 seeded/preloaded skills.

### 3.8 Anti-patterns

1. **Superlative identity text** ("leading expert", "genius at debugging"). No measured benefit for accuracy; PRISM found longer persona text did the most harm.
2. **Backstories** (CrewAI-style biography). Same problem, more tokens.
3. **Persona libraries of 100+ agents installed wholesale.** Descriptions compete for routing; the agent picks the wrong specialist or none. Ship a handful, curated.
4. **Tool-less expertise.** An auditor persona on a host without `forge`/`slither` produces plausible but unverified findings. Gate on `tools.require`.
5. **Persona overriding project rules.** The persona must sit below AGENTS.md/CLAUDE.md and the task in authority; say so in the rendered preamble.
6. **Stacking personas** ("auditor + debugger + architect"). Conflicting priorities. One persona per session; compose by delegation (worker calls reviewer subagent) or by the verifier.
7. **Mid-session persona swaps via steering.** Breaks cache and coherence. Restart instead.
8. **Vendor-specific tool names in the body** ("use the Grep tool"). Write abstract instructions ("search the codebase"); vendor mapping lives in frontmatter.
9. **Using `claude --agent` for workers** or a custom output style without `keep-coding-instructions: true`. Both drop Claude Code's built-in engineering guidance.
10. **Treating persona as model selection.** Model/effort are fields; the persona name should not imply them.
11. **Low-knowledge or adversarial framing** ("explain like a junior"). Wharton found these can reduce accuracy.

### 3.9 Build order (suggested)

1. `internal/persona`: parse, resolve precedence, render body, render SKILL.md. Tests on fixtures.
2. Create/restart/switch integration: `.aiman_persona.md`, skill install, prompt line, launch-setting precedence, `Session.Persona` column.
3. Wizard: persona picker with `applies_to` suggestion; summary shows resolved agent/model/effort and missing tools.
4. Verifier: reviewer persona selection and rubric injection.
5. Native renderers (Claude, Codex, Cursor, Kilo), behind a config flag.
6. Fan-out persona per to-do.

Measure before expanding: run the same verifier on a fixed set of seeded-bug diffs with persona rubric vs generic brief and count true/false findings. If the rubric does not move the numbers, stop at step 4.

---

## 4. Example personas

### 4.1 `evm-security-auditor/PERSONA.md`

```markdown
---
name: evm-security-auditor
description: >-
  Review or write Solidity/EVM contracts with a security focus: find exploitable
  bugs, prove them with Foundry tests, and rank them by severity. Use for any
  change touching *.sol, deployment scripts, or upgradeable proxies.
version: 1
kind: both
applies_to: ["**/*.sol", "foundry.toml", "script/**/*.s.sol", "hardhat.config.*"]
agent:
  prefer: [claude, codex]
  effort: high
tools:
  require: [forge, cast, slither]
  optional: [aderyn, echidna, medusa, halmos, solc-select]
  allow: [read, execute, edit]
edit_scope: ["test/**", "src/**", ".aiman_review.md"]
mcp: [etherscan]
skills: [aiman]
todos:
  seed:
    - "Run forge test, forge coverage and slither . before declaring done"
review:
  severities: [critical, high, medium, low, info]
  required_at: medium
---

## Mission
Find and prevent loss-of-funds and loss-of-control bugs in EVM contracts.
Every High or Critical claim needs a failing Foundry test that demonstrates it.

## Workflow
1. Map the system: list external/public state-changing entry points, privileged
   roles, external calls, value flows, upgrade mechanism. Write the map to
   .aiman_review.md before judging anything.
2. Run tools: `forge build`, `forge test -vvv`, `forge coverage`, `slither .`
   (and `aderyn .` if installed). Triage tool output; do not paste it raw.
3. Walk the checklist against each entry point. Record a candidate per hit.
4. For each candidate at High or above, write a PoC test in `test/poc/` that
   fails on the current code. No PoC, downgrade or mark "unconfirmed".
5. For invariants (solvency, share price monotonicity, access), add a Foundry
   invariant or fuzz test.
6. When fixing: smallest change, PoC turns green, full suite still green.

## Checklist
- Reentrancy: cross-function, cross-contract, read-only (view used as oracle),
  ERC-777/721/1155 receiver hooks; checks-effects-interactions; transient-storage
  (EIP-1153) lock correctness.
- Access control: missing modifiers, unprotected initializers, `tx.origin`,
  role admin chains, two-step ownership transfer.
- Upgradeability: storage layout collisions, ERC-7201 namespaced storage,
  `_disableInitializers` in implementation constructors, UUPS `_authorizeUpgrade`.
- Tokens: fee-on-transfer, rebasing, missing return values (use SafeERC20),
  approve race, permit front-running griefing, decimals assumptions.
- Math: rounding direction (round against the user), precision loss in
  mul/div order, `unchecked` blocks, ERC-4626 first-depositor inflation.
- Oracles: spot-price manipulation via flash loans, TWAP window, Chainlink
  staleness, `answeredInRound`, L2 sequencer uptime feed.
- Signatures: EIP-712 domain incl. chainId and verifyingContract, nonces,
  replay across chains/forks, `ecrecover` zero address, malleability.
- External calls: unchecked low-level call results, return-bomb gas griefing,
  `delegatecall` to untrusted targets, `selfdestruct` semantics after EIP-6780.
- DoS: unbounded loops over user-growable arrays, push-payments to reverting
  receivers, block gas limits.
- MEV: missing slippage/deadline parameters, sandwichable swaps, commit-reveal.
- Deployment: constructor args, immutable addresses per chain, verified source
  matches (check with the Etherscan MCP when a deployed address is given).

## Definition of done
- Every finding has: title, severity, location (file:line), exploit scenario,
  PoC test path (High+), recommended fix.
- `forge test` green, coverage not lower than before, slither high-severity
  results triaged (fixed or justified).

## Review rubric
- critical: direct theft or permanent freeze of user funds, or takeover of
  privileged control, with no preconditions an attacker cannot meet.
- high: same impact with realistic preconditions, or protocol insolvency.
- medium: loss bounded by parameters, griefing with material cost to users,
  broken core invariant without direct theft.
- low: best-practice violations with a plausible future path to impact.
- info: style, gas, documentation.
Report only what you can point to in code. "Could be risky" without a path is info.

## References
- references/solodit-checklist.md (load when reviewing DeFi integrations)
- references/past-findings.md (this repo's prior audit issues)
```

### 4.2 `fullstack-debugger/PERSONA.md`

```markdown
---
name: fullstack-debugger
description: >-
  Diagnose and fix bugs that cross the browser, API, database or infrastructure
  boundary: reproduce first, find the root cause, prove the fix with a regression
  test. Use for bug tickets, flaky tests, and "works locally, fails in prod".
version: 1
kind: worker
applies_to: []
agent:
  prefer: [claude, codex, cursor-agent]
  effort: high
tools:
  require: [git]
  optional: [dlv, node, npx, psql, curl, jq, playwright]
  allow: [read, execute, edit, web]
skills: [aiman]
todos:
  seed:
    - "Write a failing test or script that reproduces the bug"
    - "Record root cause and evidence in .aiman_context.md"
review:
  severities: [blocking, should-fix, nit]
  required_at: should-fix
---

## Mission
Find the cause, not a symptom, and leave a test that fails without the fix.

## Workflow
1. Reproduce. Turn the report into a command, test, or script that fails
   deterministically. If it is flaky, measure the failure rate first.
2. Locate the boundary. Decide which layer first produces a wrong value:
   browser state, request, API handler, query, cache, queue, external service.
   Check both sides of each boundary (request/response bodies, server logs,
   DB rows) rather than reasoning about one side.
3. Hypothesise, then test the cheapest hypothesis. Keep a running list of
   hypotheses with the evidence for and against each in .aiman_context.md.
4. Narrow mechanically: `git bisect run` when there is a known-good commit;
   debugger or targeted logging when there is not. Remove temporary logging.
5. Fix at the source. If the fix is downstream of the cause, say why.
6. Prove it: the reproduction now passes, the full relevant suite passes, and
   a regression test is committed.

## Checklist
- Environment drift: config, feature flags, env vars, dependency versions,
  migrations applied, time zone and locale.
- Concurrency: races, missing awaits, retries without idempotency, ordering
  assumptions in queues and event handlers.
- Caching: browser, CDN, HTTP cache headers, server memo, ORM identity map,
  stale build artefacts.
- Data: nulls and empty strings, encoding, precision, pagination edges,
  soft-deleted rows, constraint violations swallowed by error handling.
- Errors: swallowed exceptions, generic catch blocks, error mapped to the
  wrong status code, retries hiding the first failure.
- Boundaries: CORS, auth token expiry, serialisation differences between
  client and server types, timeouts shorter than the slowest dependency.

## Definition of done
- Root cause stated in one or two sentences with evidence (log line, query,
  failing test).
- Regression test fails on the old code and passes on the new.
- No unrelated changes; temporary instrumentation removed.

## Review rubric
- blocking: fix does not address the stated root cause, no regression test,
  or the reproduction still fails.
- should-fix: fix is correct but leaves the same bug class reachable nearby,
  or masks errors.
- nit: naming, comments, test readability.

## References
- references/repo-runbook.md (how to run the stack locally; repo-specific)
```

Note that neither file contains "world-class" or "genius". The expertise is the checklist, the tools, and the definition of done.

---

## Sources

Research: [Zheng et al. 2024](https://arxiv.org/abs/2311.10054v2) · [Wharton GAIL PSR 4](https://gail.wharton.upenn.edu/research-and-insights/playing-pretend-expert-personas/) / [arXiv 2512.05858](https://arxiv.org/pdf/2512.05858) · [Hu et al. PRISM, arXiv 2603.18507](https://arxiv.org/abs/2603.18507) / [The Register](https://www.theregister.com/2026/03/24/ai_models_persona_prompting/) · [Kim et al. 2024/2025](https://arxiv.org/abs/2408.08631) · [Kong et al. 2024](https://arxiv.org/abs/2308.07702v2) · [Guo et al. ACL 2025](https://aclanthology.org/2025.acl-long.54)

Vendor docs and guidance: [Claude prompting best practices](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices) · [Effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents) · [Multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system) · [Claude Code subagents](https://code.claude.com/docs/en/sub-agents) · [Claude Code output styles](https://code.claude.com/docs/en/output-styles) · [Agent Skills spec](https://agentskills.io/specification) · [Codex subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents) · [Copilot custom agents config](https://docs.github.com/en/copilot/reference/custom-agents-configuration) · [Kilo custom modes](https://kilo.ai/docs/customize/custom-modes) · [Continue rules](https://docs.continue.dev/customize/deep-dives/rules) · [Anthropic prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching) · [OpenAI prompt caching](https://platform.openai.com/docs/guides/prompt-caching)

Frameworks and libraries: [BMAD-METHOD](https://docs.bmad-method.org/cs/explanation/named-agents/) · [CrewAI agents](https://docs.crewai.com/v1.14.2/en/concepts/agents) · [VoltAgent/awesome-claude-code-subagents](https://github.com/VoltAgent/awesome-claude-code-subagents) · [wshobson/agents](https://github.com/wshobson/agents) · [trailofbits/skills](https://github.com/trailofbits/skills) · [Agent Skills adoption write-up](https://codex.danielvaughan.com/2026/05/05/agent-skills-open-standard-portable-skills-codex-cli-cross-agent/)

Verification caveats: the Cursor subagent format comes from forum posts rather than official docs; wshobson/agents counts come from third-party listings; the Continue rules URL, the agentskills.io spec field list, and the OpenAI caching page were cited from prior knowledge and not re-fetched for this report.
