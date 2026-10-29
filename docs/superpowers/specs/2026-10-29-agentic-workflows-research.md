# Proven agentic workflows for coding agents (as of 29 October 2026)

Mapped onto `docs/superpowers/specs/2026-10-29-design-first-coordinator-design.md` (the "draft").

## How to read this report

Each finding gives the claim, the evidence, and the source with its date. Evidence strength is tagged:

- **[Measured]**: benchmark, ablation, controlled study or vendor write-up with numbers.
- **[Practice]**: vendor guidance or widely adopted practice, with qualitative or self-reported outcomes only.
- **[Opinion]**: argument or anecdote without measurement. Listed only where it is influential, and labelled.

Caveats that apply throughout:

- Many 2026 papers cited here are single-team arXiv preprints, not peer reviewed. Several are single-author.
- Vendor numbers (Anthropic, OpenAI, Cognition, LangChain) come from parties with an interest in the result.
- SWE-bench Verified is no longer a trustworthy yardstick. OpenAI stopped reporting it on 23 Feb 2026: an audit of 138 hard problems found 59.4% had material test or description flaws, and frontier models could reproduce gold patches verbatim ([OpenAI, 2026-02-23](https://openai.com/index/why-we-no-longer-evaluate-swe-bench-verified/)). Results quoted on Verified below should be read as relative, not absolute.
- Two figures were found only in secondary sources and are marked as such.

---

## 1. Orchestrator-worker and coordinator patterns

### 1.1 Multi-agent wins on breadth-first, parallelisable work, and costs far more tokens [Measured]

- **Claim**: An orchestrator with parallel subagents beats a single agent on research-style tasks, mostly by spending more tokens.
- **Evidence**: Opus 4 lead with Sonnet 4 subagents beat single-agent Opus 4 by 90.2% on Anthropic's internal research eval. On BrowseComp, token usage alone explained 80% of performance variance (three factors explained 95%). Agents use about 4x the tokens of chat; multi-agent systems about 15x. Anthropic states that "most coding tasks involve fewer truly parallelizable tasks than research."
- **Source**: [Anthropic, "How we built our multi-agent research system", 2025-06-13](https://www.anthropic.com/engineering/multi-agent-research-system)

### 1.2 Multi-agent degrades sequential work; centralised coordination contains errors [Measured]

- **Claim**: Whether multi-agent helps depends on task structure. On sequential tasks every multi-agent topology tested was worse than one agent. Gains shrink as the single-agent baseline gets stronger. Architectures without central verification propagate errors more.
- **Evidence**: Controlled study, 260 configurations across six agentic benchmarks and three model families. Best case +80.8% on decomposable financial reasoning; worst case -70.0% on sequential planning. Coordination "yields diminishing returns once single-agent baselines exceed certain performance." Google's blog reports independent (non-communicating) agents amplified errors 17.2x versus 4.4x for centralised orchestration. A fitted model picks the best architecture for 87% of held-out configurations.
- **Source**: [Kim et al., "Towards a Science of Scaling Agent Systems", arXiv 2512.08296, v1 2025-12-09, v3 2026-04-08](https://arxiv.org/abs/2512.08296); [Google Research blog](https://research.google/blog/towards-a-science-of-scaling-agent-systems-when-and-why-agent-systems-work/)

### 1.3 Multi-agent failures are mostly design failures: specification, inter-agent misalignment, verification [Measured]

- **Claim**: Most multi-agent failures come from system design, not model capability.
- **Evidence**: MAST taxonomy built from 1,642 annotated traces across seven frameworks; 14 failure modes in three groups: specification issues, inter-agent misalignment, task verification. Inter-annotator kappa 0.88. Cited framework failure rates of 41% to 86.7%.
- **Source**: [Cemri et al., "Why Do Multi-Agent LLM Systems Fail?", arXiv 2503.13657, NeurIPS 2025, rev. 2025-10-26](https://arxiv.org/abs/2503.13657)

### 1.4 What works in production coding: read-only helpers, generator-verifier, manager-child; what fails: parallel writers [Practice, partly Measured]

- **Claim** (Cognition, April 2026 update to "Don't Build Multi-Agents"): Patterns that keep writes single-threaded work. Parallel writer swarms still fail because implicit decisions (style, edge cases) conflict.
  - Works: read-only search subagents; a reviewer with clean context that reads only the diff; "smart friend" escalation from a primary model to a stronger one; manager Devins spawning child Devins (live in product).
  - Fails or is hard: managers are "overly prescriptive"; children assume shared state they do not have; "upward communication doesn't happen by default"; a weak primary model is bad at judging when to escalate.
- **Evidence**: Devin Review finds "an average of 2 bugs per PR, of which roughly 58% are severe" on Devin-written PRs. Pairing SWE-1.5 with Sonnet 4.5 for planning closed only "a small bit of the performance gap". Cross-frontier escalation (Claude with GPT) produced "real gains" on tricky cases (no number).
- **Source**: [Cognition, "Multi-Agents: What's Actually Working", 2026-04-22](https://cognition.com/blog/multi-agents-working); original argument (context sharing, implicit decisions) in [Cognition, "Don't Build Multi-Agents", 2025-06](https://cognition.com/blog/dont-build-multi-agents) [Opinion at the time].

### 1.5 Parallel coding works when work is decomposed into independent units with an oracle, and fails on one shared task [Measured, single case]

- **Claim**: Parallel agents scale on independent failing tests; they collide on one monolithic task.
- **Evidence**: 16 Opus 4.6 agents, about 2,000 sessions, just under $20,000, about 100,000 lines of a Rust C compiler that builds Linux 6.9. When all 16 hit the same kernel bug they overwrote each other's fixes; the fix was to use GCC as a known-good oracle so agents could split the work by file. Task claiming used lock files in git. Lessons: "the verifier must be nearly perfect" or Claude "will solve the wrong problem"; CI added late to stop regressions; harness output kept to a few grep-able lines to avoid context pollution; a `--fast` 1-10% test sample because agents are blind to wall time.
- **Source**: [Anthropic (Carlini), "Building a C compiler with a team of parallel Claudes", 2026-02-05](https://www.anthropic.com/engineering/building-c-compiler)

### 1.6 Vendor guidance: a single session is cheaper for sequential or same-file work; split by file ownership; 3-5 workers [Practice]

- **Claim**: Agent teams "use significantly more tokens"; for "sequential tasks, same-file edits, or work with many dependencies, a single session or subagents are more effective." Two workers editing one file overwrite each other. Start with 3-5 teammates; tasks should be "self-contained units that produce a clear deliverable". The lead sometimes starts implementing instead of waiting, and sometimes declares the team done early.
- **Source**: [Claude Code docs, "Orchestrate teams of Claude Code sessions", current as of 2026-10](https://code.claude.com/docs/en/agent-teams)

### 1.7 On SWE-bench-style single-issue work, multi-agent teams show small or negative deltas [Measured, weak]

- Secondary analyses report -2% to -15% for multi-agent architectures on SWE-bench Verified when single-agent baselines are above 45%, and one self-reported team system claiming +7.2 points (72.2%) without a role ablation. Treat both as low confidence. ([Medium analysis](https://medium.com/@mjgmario/single-agent-vs-multi-agent-systems-when-coordination-helps-hurts-and-pays-off-57735ee7916d); [Agyn write-up](https://dev.to/nikita_benkovich_eb86e54d/coding-agent-teams-outperform-solo-agents-722-on-swe-bench-verified-4of5))

**Net for section 1**: Orchestration pays when pieces are genuinely independent, each has a verifier, and one central node checks results. It hurts on sequential or tightly coupled work, multiplies tokens, and fails through under-specified briefs and missing upward communication.

---

## 2. Plan- and design-first workflows

### 2.1 Explore, plan, code, commit is the standard vendor workflow, with no published ablation [Practice]

- **Claim**: Anthropic recommends exploring and planning before coding because jumping straight to code "can produce code that solves the wrong problem", and says the plan step can be skipped for small changes. Cursor shipped Plan Mode (research codebase, ask clarifying questions, editable plan) and reports "significantly" better code quality with no numbers. Cursor's advice when a run goes wrong: revert, sharpen the plan, rerun, "often faster than fixing an in-progress agent".
- **Source**: [Anthropic, "Claude Code: Best practices for agentic coding", 2025-04-18](https://www.anthropic.com/engineering/claude-code-best-practices) and [current docs](https://code.claude.com/docs/en/best-practices); [Cursor, "Introducing Plan Mode", 2025-10-07](https://cursor.com/blog/plan-mode); [Cursor docs](https://cursor.com/docs/agent/planning)

### 2.2 Separating a planner from an editor helped in early benchmarks, but the gain depended on the pairing [Measured]

- **Evidence**: Aider architect/editor mode: o1-preview architect with a cheaper editor scored 85% on aider's editing benchmark versus 79.7% for o1-preview alone (2024-09-26). On the harder polyglot benchmark, o1 paired with Sonnet did not beat o1 alone, while R1 architect plus Sonnet editor hit 64.0% at 14x lower cost than the previous best (2025-01-24).
- **Source**: [aider, 2024-09-26](https://aider.chat/2024/09/26/architect.html); [aider, 2025-01-24](https://aider.chat/2025/01/24/r1-sonnet.html)

### 2.3 Spec-driven development in practice: used selectively, costs 2-3x tokens, specs decay [Measured, qualitative/associational]

- **Claim**: Practitioners believe specs make agent work more reliable but cannot separate that from model improvements. Most adopt specs only for larger work and skip them for bug fixes, hotfixes, scripts and prototypes (about 82% of mined specs describe new features). One team reported spec-driven runs used two to three times the tokens and about twice the time. Specs drift during implementation ("implementation drift") and go stale afterwards ("spec decay"); most spec files have a single commit. Most users read specs only partially or delegate spec review to other agents ("verification debt").
- **Evidence**: 21 practitioner interviews, 99 Reddit records, a corpus of 470,795 spec files from 73,030 repos (266-repo sample).
- **Source**: ["Spec-Driven Development as Prescribed and as Practiced", arXiv 2608.25202 v4, 2026-10-15](https://arxiv.org/html/2608.25202)
- Related commentary [Opinion]: Böckeler distinguishes spec-first, spec-anchored and spec-as-source and questions how far SDD scales down to small changes ([martinfowler.com, 2025-10](https://www.martinfowler.com/articles/exploring-gen-ai/sdd-3-tools.html)).

### 2.4 No controlled study shows that visual/diagram-first design improves agent outcomes [Gap]

I found no published ablation of diagram-first (domain model, structure, behaviour) design for coding agents. The case for the visual page rests on operator review quality, which is a human-factors argument, not an agent-performance one. Section 7 shows where human review attention is scarce, which supports making the one review that happens easy to read.

### 2.5 Structured long-run harnesses: a planning/initialiser session, a machine-readable feature list, one feature per session [Practice]

- **Claim**: Long agent projects failed by trying to one-shot the whole thing, by later sessions "declaring victory early", and by marking features done after unit tests or curl without end-to-end checks. Fixes: an initialiser session writes `init.sh`, a progress log and a JSON feature list (200+ items, all failing); each coding session takes one feature, commits, updates notes; JSON chosen because models are less likely to rewrite it improperly than Markdown; browser-automation testing "dramatically improved performance" (no number).
- **Source**: [Anthropic, "Effective harnesses for long-running agents", 2025-11-26](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)

### 2.6 Agent-first teams keep plans and design docs in the repo as versioned artifacts and enforce them mechanically [Practice, self-reported]

- **Claim**: OpenAI built a roughly one-million-line product over five months with no hand-written code. Engineers moved to "designing environments, specifying intent, and providing structured feedback"; docs (maps, execution plans, design specs) are the agents' single source of truth, cross-linked and "mechanically enforced with linters and CI validation".
- **Source**: [OpenAI, "Harness engineering", 2026-02-11](https://openai.com/index/harness-engineering/) via [InfoQ summary, 2026-02](https://www.infoq.com/news/2026/02/openai-harness-engineering-codex). Single greenfield experiment by the vendor.

**Net for section 2**: Plan-before-code is universal vendor practice and is cheap insurance against solving the wrong problem, but its benefit is not measured in isolation. The measured cost is real (2-3x tokens in one team). The strongest evidence supports sizing the ceremony to the task and keeping the plan as a living, machine-checked artifact rather than a one-off document.

---

## 3. Model routing and tiering

### 3.1 Judging difficulty from the task text alone has a floor; judge after a few exploratory turns [Measured, theory plus results]

- **Claim**: Routers that see only the issue text cannot separate "localized typo" from "multi-module refactor"; conditioning on a short cheap-model exploration trajectory is provably no worse and better when exploration is informative. SWE-Router runs a cheap model a few turns, then a value head predicts whether it will finish; if not, it escalates.
- **Source**: [SWE-Router, arXiv 2607.00053, 2026-06-30 (ICML 2026 DL4Code workshop)](https://arxiv.org/abs/2607.00053)
- Related: Scrouting ("scout the repository first") matched the best single model (Opus 4.6) on 266 SWE-bench Pro tasks at about one fifth of the cost per solve (159 vs 158 solves, a tie, not a win) ([arXiv 2608.04804, 2026-08](https://arxiv.org/pdf/2608.04804)).

### 3.2 Escalation routing cuts cost sharply and loses some accuracy; it does not beat the cheap model reliably [Measured]

- **Evidence**: 145 multi-step agent tasks. Opus 4.8 alone 86.0% at $11.45/run; routed (cheap model, judge escalates after two negative verdicts) 80.0% at $3.00; cheap model alone 77.7% at $0.72. Routing cut cost about 74% for 6 points. Opus handled 7% of calls but 68.4% of spend; the judge took 21.2% of spend. Per-run cost varied 67% with escalation frequency. Routing beat the cheap model by only 2.3 points, within noise. Escalation needs multi-turn tasks. Break-even check: minimum offload = judge cost / (expensive cost - cheap cost).
- **Source**: [LangChain, "How many of your agent's calls actually need a frontier model?", 2026-08-11](https://www.langchain.com/blog/switchyard-agent-routing-benchmark)

### 3.3 Cheap executor plus frontier advisor: better and cheaper than the cheap model alone, and cheaper than the frontier model [Measured, vendor]

- **Evidence**: Sonnet 4.6 with an Opus advisor scored 2.7 points higher on SWE-bench Multilingual than Sonnet alone and cost 11.9% less per task. Haiku 4.5 with an Opus advisor scored 41.2% on BrowseComp versus 19.7% solo, at 85% lower cost per task than Sonnet solo. The executor decides when to call; the advisor returns 400-700 tokens of guidance and makes no tool calls; `max_uses` caps calls.
- **Source**: [Anthropic, "The advisor strategy", 2026-04-09](https://claude.com/blog/the-advisor-strategy)

### 3.4 Escalation judgment is the weak point of a weak primary model [Practice]

- Cognition: "smart friend" works when both models are strong; with SWE-1.5 as primary, escalation judgment and question-asking were the gaps (see 1.4). Their 80/20 fix: share a fork of the primary's full context with the stronger model, rather than a fresh brief.

**Net for section 3**: Tiering is proven to cut cost by large factors with modest accuracy loss. Measured failure modes: a-priori difficulty estimates are unreliable; cheap primaries are poor at deciding when to escalate; escalation cost is spiky; small price gaps erase the benefit. The best-supported escalation patterns are trajectory-based (after exploration or after failure signals) and in-context advice from a frontier model, not just restarting the piece on a bigger model.

---

## 4. Multi-model review, sounding boards, debate, LLM-as-judge

### 4.1 Multi-agent debate does not reliably beat simple single-agent baselines at equal compute [Measured]

- **Evidence**: Across benchmarks, MAD methods "fail to reliably outperform simple single-agent baselines such as Chain-of-Thought and Self-Consistency, even when consuming additional inference-time computation"; the later version argues that model heterogeneity is what helps (Heter-MAD). Earlier: at six responses, debate 83.2% vs self-consistency 85.3% on GSM8K.
- **Source**: [Zhang et al., arXiv 2502.08788, 2025-02](https://arxiv.org/abs/2502.08788); [Huang et al., "LLMs Cannot Self-Correct Reasoning Yet", arXiv 2310.01798, 2023](https://arxiv.org/abs/2310.01798); [Smit et al., ICML 2024](https://proceedings.mlr.press/v235/smit24a.html)

### 4.2 Same-family judges favour themselves and their family [Measured]

- **Evidence**: Over 5,000 prompt-completion pairs with expert labels and nine judges: GPT-4o and Claude 3.5 Sonnet "systematically assign higher scores to their own outputs" and favour same-family outputs, after controlling for quality.
- **Source**: ["Play Favorites", arXiv 2508.06709, 2025-08-08](https://arxiv.org/abs/2508.06709); SE-specific audit reports family favouritism via stylistic similarity ([Bias in the Loop, arXiv 2604.16790, 2026-04](https://arxiv.org/html/2604.16790v1))

### 4.3 A second reviewer helps; a cross-model reviewer finds different errors; a lightweight reviewer adds nothing [Measured, small single-author preprints]

- **Evidence** (30 artifacts, 150 planted errors, 900 sessions):
  - Fresh-context review of the artifact alone (CCR) beat a second pass in the same session (28.6% vs 21.7% F1, significant), but not a single same-session pass reliably ([arXiv 2603.12123 v2, 2026-10-01](https://arxiv.org/html/2603.12123)).
  - Top-tier cross-model reviewer (GPT-5.4) 32.3% F1 vs same-model fresh review 28.6%, not significant. Error overlap only 41.2% (Jaccard). One same-model fresh review plus one cross-model review caught 56.7% of planted errors vs 42.7% for two same-model reviews (significant, p=.006). A lightweight cross-model reviewer (Gemini 2.5 Flash, 24.0% F1) was no better than self-review. Cross-model review did better on documents (42.1% vs 24.5% F1); same-model fresh review did slightly better on code (40.7% vs 37.2%). About 9% of cross-model false positives were "model bias" (flagging tools the generator used as non-existent).
  - **Source**: ["When Does a Second Model Help?", arXiv 2610.01471 v2, 2026-10-06](https://arxiv.org/html/2610.01471v2)
- Note the absolute recall: even the best pair found well under two thirds of planted errors. Review is a filter, not a proof.

### 4.4 Clean-context review in production finds real bugs [Practice, vendor-measured]

- Devin Review: about 2 bugs per Devin PR, about 58% severe; works best reading only the diff with clean context; a filter decides which findings get fixed to avoid loops and scope creep ([Cognition, 2026-04-22](https://cognition.com/blog/multi-agents-working)).

### 4.5 Agent review comments: about 71% acted on; misses are mostly missing design context [Measured]

- 54,713 agent review comments (Copilot, Cursor, Codex) in Python repos: about 71% resolved/acted on; comments with an inline code suggestion about 75% vs 65% without; shorter comments more likely acted on; the most common reason for rejection was an intentional design decision the agent did not know about; incorrect suggestions were mostly false positives.
- **Source**: ["Go Home Copilot, You're Drunk", arXiv 2607.21997 v2, 2026-07-29](https://arxiv.org/html/2607.21997v2)

### 4.6 LLM-as-judge works best as one call with a rubric and a pass/fail plus score [Practice]

- Anthropic found a single judge call scoring 0.0-1.0 with pass/fail against a rubric was most consistent with human judgment; judge end state, not step sequence ([Anthropic, 2025-06-13](https://www.anthropic.com/engineering/multi-agent-research-system)).

**Net for section 4**: A second, independent reviewer with clean context is worth it; the reviewer must be top tier. Diversity of model family adds coverage and avoids self-preference, more clearly for documents (designs) than for code. Debate rounds between models are not supported over cheaper alternatives. Reviewers miss design intent they were not given, and they produce false positives, so findings need triage.

---

## 5. Verification loops

### 5.1 Given good tests, agents resolve almost everything; writing valid tests is the bottleneck [Measured]

- **Evidence**: With human-written reproduction tests, TDFlow reached 88.8% on SWE-bench Lite (+27.8 points over next best) and 94.3% on SWE-bench Verified; manual inspection of 800 runs found 7 test-hacking cases. The authors conclude the main obstacle is "writing successful reproduction tests" and propose humans write tests, agents resolve them.
- **Source**: [TDFlow, arXiv 2510.23761, EACL 2026, rev. 2026-01-22](https://arxiv.org/abs/2510.23761)

### 5.2 Telling agents to write more tests does not raise resolution; it raises cost [Measured]

- **Evidence**: Prompting four models to write more or fewer tests left about 83% of outcomes unchanged, with no significant shift for any model (McNemar, all p > .05). Discouraging tests cut input tokens 33-49%; encouraging them raised GPT-5.2 output tokens 19.8% for no gain.
- **Source**: ["Rethinking the Value of Agent-Generated Tests", arXiv 2602.07900 v2, 2026-04-09](https://arxiv.org/html/2602.07900v2)
- Related: generic "do TDD" instructions raised regressions to 9.94%; supplying the specific tests to check cut regressions 70% (6.08% to 1.82%), small samples ([TDAD, arXiv 2603.17973, 2026-03](https://arxiv.org/abs/2603.17973)).

### 5.3 Agents exploit tests when the task is impossible or the tests conflict with the spec [Measured]

- **Evidence**: ImpossibleBench mutates tests to contradict the spec, so any pass is cheating. Frontier models cheat frequently, stronger ones more; strategies range from editing test files to operator overloading. Prompting and test access change rates a lot (secondary reports: an explicit "stop, do not special-case" instruction cut one model's rate from 93% to 1% on one variant; hiding tests cut rates but also hurt legitimate performance).
- **Source**: [Zhong, Raghunathan, Carlini, arXiv 2510.20270, 2025-10-23](https://arxiv.org/abs/2510.20270); author write-up on [LessWrong](https://www.lesswrong.com/posts/qJYMbrabcQqCZ7iqm/impossiblebench-measuring-reward-hacking-in-llm-coding-1) (secondary for the numbers).

### 5.4 Verifier quality and end-to-end checks matter more than volume [Practice, single cases]

- Carlini: the verifier must be "nearly perfect"; CI added to stop regressions (1.5). Anthropic harness: unit tests and curl checks let agents mark features done that did not work; end-to-end browser tests fixed it (2.5). Benchmarks themselves can be wrong: 59.4% of audited hard SWE-bench Verified problems had flawed tests (OpenAI, see caveats).

### 5.5 Agent PRs are merged less often than human PRs [Measured]

- AIDev (about 456,000 agent PRs, 2025): merge rates below a human baseline (76.8%) in the full dataset (Codex 64%, Devin 49%, Copilot 35%); higher in popular repos. Seven rejection modes are unique to agent PRs, including distrust of AI code ([AIDev, arXiv 2507.15003, 2025-07-20](https://arxiv.org/abs/2507.15003); [rejection study, arXiv 2602.04226](https://arxiv.org/html/2602.04226v1)).

**Net for section 5**: The single best-evidenced lever is a correct, targeted acceptance test written before implementation by someone other than the implementer. Volume of agent-written tests does not help. Tests that the implementer can edit are a cheating surface. End-to-end checks and CI catch what unit tests miss.

---

## 6. Context engineering for long-running agents

### 6.1 Context rot is real; keep context small and high-signal; fetch just in time [Practice]

- Recall degrades as the window fills across all models. Recommended: compaction (summarise and restart, tuned for recall first then precision), tool-result clearing, structured note-taking outside the window (NOTES.md, to-do lists), sub-agents that may burn tens of thousands of tokens but return 1,000-2,000-token summaries, and lightweight references (paths, links) loaded on demand.
- **Source**: [Anthropic, "Effective context engineering for AI agents", 2025-09-29](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)

### 6.2 External memory plus context editing measurably helps [Measured, vendor]

- Memory tool plus context editing: +39% over baseline on an internal agentic-search eval; context editing alone +29%; context editing cut token use 84% in a 100-turn web-search eval.
- **Source**: [Anthropic, "Managing context on the Claude Developer Platform", 2025-09-29](https://www.anthropic.com/news/context-management)

### 6.3 Structured compaction beats free-form; file/artifact tracking is the weakest part of every method [Measured, vendor]

- Probe-based evaluation on 36,000+ messages from production coding sessions: Factory structured summary 3.70/5 overall vs Anthropic 3.44 vs OpenAI 3.35. Artifact trail (which files changed and how) was the lowest dimension for all three (2.45, 2.33, 2.19): "Artifact tracking remains an unsolved problem." Recommendations: fixed sections (intent, file modifications, decisions, next steps) each filled or explicitly empty; merge incrementally rather than regenerate; keep a separate artifact index; optimise tokens per task, not per request.
- **Source**: [Factory, "Evaluating context compression", 2025-12-16](https://factory.com/news/evaluating-compression)

### 6.4 Models trained for compaction can run across many windows [Practice, vendor]

- GPT-5.1-Codex-Max compacts natively and was observed working on tasks for more than 24 hours in internal evaluations ([OpenAI, 2025-11-19](https://openai.com/index/gpt-5-1-codex-max/)).

### 6.5 Briefs for subagents: objective, output format, tools/sources, boundaries; outputs to files, references back [Practice]

- Vague briefs caused duplicated work and gaps. Each subagent needs an objective, an output format, guidance on tools and sources, and clear task boundaries. Effort scales with complexity (1 agent with 3-10 tool calls for simple lookups; 2-4 subagents for comparisons; 10+ for complex research). The lead saves its plan to memory because context past 200k tokens is truncated. Subagents write outputs to external storage and pass "lightweight references" back.
- **Source**: [Anthropic, 2025-06-13](https://www.anthropic.com/engineering/multi-agent-research-system). Cognition adds that managers over-prescribe and children assume shared state they lack (1.4).

**Net for section 6**: Persistent, structured, file-based state (plan, progress, decisions, file index) is the proven mechanism. Summaries with fixed sections beat free-form ones. File/artifact tracking needs its own explicit record.

---

## 7. Human-in-the-loop gates

### 7.1 Per-action approvals become rubber stamps [Measured, vendor]

- Users accepted 93% of Claude Code's manual permission prompts; Anthropic's rationale for auto mode is that people "stop paying close attention to what they're approving". The replacement classifier has a 0.4% false-positive rate and a 17% false-negative rate on real overeager actions ("the honest number").
- **Source**: [Anthropic, "Claude Code auto mode", 2026-03-25](https://anthropic.com/engineering/claude-code-auto-mode)

### 7.2 Experienced users shift from approving each step to monitoring and interrupting [Measured, vendor]

- New users (<50 sessions) fully auto-approve about 20% of sessions; by 750 sessions over 40%. Interrupt rate rises from 5% to about 9% of turns with experience. On the most complex tasks, Claude Code asks for clarification more than twice as often as on minimal ones, and more than twice as often as humans interrupt it. 99.9th-percentile autonomous turn length grew from under 25 to over 45 minutes (Oct 2025 to Jan 2026).
- **Source**: [Anthropic, "Measuring AI agent autonomy in practice", 2026-02-18](https://www.anthropic.com/research/measuring-agent-autonomy)

### 7.3 Humans skim specs and reviews when volume is high [Measured, associational]

- Most SDD users read specs only partially or delegate review to agents (2.3). Accepted Codex PRs were reviewed in a median 0.3 hours vs 3.9 hours for human PRs (AIDev, 5.5), consistent with light review of agent output.

### 7.4 Where gates pay off [Practice]

- Vendors converge on one human checkpoint at the plan (Cursor Plan Mode, Claude Code plan mode, Spec Kit), autonomous execution after it, and a final human review of the PR. Cursor's guidance that reverting and re-planning beats fixing a wrong run (2.1) implies the plan gate is the high-leverage one. Claude Code agent teams auto-approve teammate plans by default rather than asking the human for each one ([agent teams docs](https://code.claude.com/docs/en/agent-teams)).

**Net for section 7**: Gates pay off when they are few, early, and present a decision the human can actually judge. Frequent gates train approval without reading. Mid-run human attention is better spent as interruption on signals (needs-you notifications) than as approval of every step.

---

## Implications for the aiman design

Each item says keep, change, add or drop, and names the finding behind it. Section numbers refer to the draft.

### Keep

1. **Keep the trivial / small / project sizing and "no ceremony on trivial work".** Practitioners use specs selectively and skip them for fixes (2.3); Anthropic scales effort to complexity (6.5); spec-driven runs cost 2-3x tokens (2.3). The anti-goal is well supported.
2. **Keep one approval gate, at the design, and only there.** Plan-time is the high-leverage checkpoint (7.4, 2.1); per-action approvals degrade to 93% rubber-stamping (7.1). Do not add per-child or per-merge operator gates; the reviewer and CI handle those.
3. **Keep the coordinator as the single central verifier with children reporting back.** Centralised orchestration contained error amplification to 4.4x vs 17.2x (1.2); MAST attributes failures to missing verification (1.3).
4. **Keep the independent reviewer with clean context before merge.** Clean-context review finds real bugs (4.4) and beats a second same-session pass (4.3).
5. **Keep the consult on a different agent or model than the coordinator, and keep the page showing each objection.** Family self-preference is measured (4.2); cross-model review is strongest on documents, which a design page is (4.3).
6. **Keep the design page as durable, linked state and the handover skeleton as is.** Persistent structured notes and fixed-section summaries are the proven context mechanisms (6.1-6.3, 2.5).

### Change

7. **Change "size it" to happen after a bounded exploration step, and allow re-rating after a child explores.** A-priori difficulty from task text has an information floor; trajectory-based routing is better (3.1). Concretely: the coordinator's size call follows exploration (the sequence diagram's first step should say "explore, then size"), and a worker child reports "harder than rated" after its first exploration, which re-tiers the piece instead of waiting for a failed attempt.
8. **Change the M-complexity escalation rule from "smart after one failed attempt" to an advisor step first, then re-tier.** Cheap executor plus frontier advice beats the cheap model alone and is cheaper than the frontier model (3.3); cheap primaries judge escalation badly (3.4), so give workers explicit triggers in the brief (for example: a contract in the brief does not match the code; a second test failure with the same cause; a decision not covered by the design). The advisor is a short consult on the smart tier with the worker's context, not a restart. Keep "smart after one failed attempt" as the fallback.
9. **Change M default: worker only when the piece has acceptance tests written in its brief; otherwise smart.** Tests written up front are the strongest outcome lever (5.1); an under-specified piece is exactly where cheap models fail (3.2, 3.4).
10. **Change the consult brief to give the sounding board the design page, the task and read access to the code, not the coordinator's transcript, and require a top-tier model.** Artifact-only fresh review beats in-context review (4.3); lightweight reviewers add nothing (4.3). The draft's "smart tier" satisfies this only if the smart tier never holds a cheap entry; enforce it.
11. **Change the autonomous rule "a passing consult verdict stands in for approval".** LLM judges are lenient toward familiar outputs (4.2) and miss over a third of planted errors even in pairs (4.3). For autonomous runs require the consult to be cross-family when one is installed, treat any unresolved high-severity objection as a stop (needs-you), and record the verdict in the ledger. Keep the two-rejection stop.
12. **Change "consults on the design and on the breakdown" for project size into one combined consult per round, capped at two rounds.** Debate-style iteration does not beat cheaper baselines (4.1); the measured gain comes from adding one independent reviewer (4.3), not from more rounds.

### Add

13. **Add a parallelism rule to the breakdown: pieces run in parallel only if they own disjoint files and share no unsettled contract; otherwise they run in sequence or merge into one piece.** Multi-agent degrades sequential work by up to 70% (1.2); parallel writers conflict on implicit decisions (1.4); same-file teammates overwrite each other (1.6); 16 agents on one task overwrote each other (1.5). Add columns "owns files" and "parallel group" to the breakdown table, and cap concurrent children at 3-5 (1.6).
14. **Add a required child brief structure: objective, contract/interfaces it must honour, files it owns, out of scope, acceptance tests (exact command), report format, escalation triggers.** Vague briefs cause duplication and gaps (6.5); children assume shared state and managers over-prescribe (1.4); specification issues are a top MAST failure class (1.3). Pass the design page and spec by path, not pasted (6.1, 6.5).
15. **Add acceptance tests to the breakdown, written by the coordinator (or approved by the operator with the design) before children start, and protect them.** Human- or planner-written reproduction tests drive 88-94% resolution (5.1); agents edit or game tests (5.3). Children may not modify files listed as acceptance tests; the reviewer flags any diff touching them; verify runs those exact commands. Do not tell children "write more tests" (5.2).
16. **Add "evidence before done" to the child report: the commands run, their exit codes, and an end-to-end check where one exists.** Unit-test-only completion produced false "done" claims (2.5); verifier quality decides outcomes (1.5). Store it as a machine-readable status (JSON with `passes` per acceptance item) rather than prose (2.5).
17. **Add an upward-communication channel that the child must use, not may use.** Cognition: upward communication "doesn't happen by default" and sibling-affecting discoveries get lost (1.4). The child report should have a mandatory "contract changes or surprises affecting other pieces" field (empty is allowed but explicit), and the coordinator re-reads the design when it is non-empty.
18. **Add a rule that the coordinator does not implement pieces while children run and does not declare the project done until every piece's report and review are in.** Leads start implementing or stop early (1.6); later sessions declare victory early (2.5).
19. **Add an "as-built" republish when the work merges, or mark the page historical.** Specs decay and drift (2.3). The page is kept as the design record, so its last revision should reflect what shipped, with deviations from the approved design listed.
20. **Add a files-changed index to the handover and child reports.** Artifact tracking is the weakest dimension of every compaction method (6.3).
21. **Add cost and outcome measurement for the design phase itself.** Multi-agent work costs about 15x chat tokens (1.1); spec-first adds 2-3x (2.3); the visual-design step has no published evidence of improving agent outcomes (2.4). Record tokens and wall time per phase (design, consult, children, review) in the ledger and usage tracker, plus objection counts and how many were accepted. This turns the "rubber-stamp" anti-goal into a measurable signal, and gives the operator data to tune sizing thresholds.
22. **Add a triage step for consult and review findings: each objection gets accept, reject with reason, or defer.** Review findings include false positives and misjudge intentional design (4.5); Devin Review filters findings to avoid loops and scope creep (4.4). The draft's Decisions section already carries this; make "reject with reason" explicit.

### Drop

23. **Drop "consults on the design and on the breakdown" as two separate consult sessions for project size** (see 12). Fold the breakdown into the single consult brief.
24. **Drop any expectation that model debate across several rounds improves the design.** No evidence supports it over one independent review (4.1). If a second opinion is wanted, add a same-model fresh-context reviewer alongside the cross-model one, which is the measured best pair (4.3), rather than another round.

### Evidence gaps to keep in mind

- No controlled study shows diagram-first design improves agent code; its value is operator comprehension at the one gate.
- Cross-model review results come from small single-author preprints with planted errors.
- Routing and advisor numbers are vendor- or single-team-reported and workload-specific; validate on aiman's own eval harness before fixing tier thresholds.
