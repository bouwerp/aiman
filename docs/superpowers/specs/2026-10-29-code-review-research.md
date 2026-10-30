# Code review best practices for an AI-orchestrated "review mode" (research as of 29 Oct 2026)

Scope: evidence on human code review, AI code review, multi-model review, and human-in-the-loop review, gathered to design aiman's review-mode session. Each finding gives a claim, the evidence, and a source with its date.

Flags:
- **[vendor]**: reported by the vendor about its own product, not independently verified.
- **[single]**: one source only; no corroboration found.
- **[secondary]**: I saw the number only in a summary, not the primary text.

Bottom line: human review mostly produces understanding and maintainability, not bug-catching. AI review is useful only when it is quiet and verified; noise is the dominant failure. A second, independent reviewer raises recall more than it raises precision, so it needs a dedupe-and-verify step after it. Explanations alone do not stop humans from rubber-stamping; making the human commit to a judgment before seeing the AI's verdict does.

---

## 1. What reviews actually find, and the goals that matter

**1.1 Reviews are less about defects than people expect; understanding is the core activity.**
- Evidence: Microsoft study (observation of 17 developers, surveys of developers and managers, 570 coded comments). Finding defects is the top *stated* motivation, but outcomes skew to knowledge transfer, team awareness and alternative solutions. The authors conclude that "code and change understanding is the key aspect of code reviewing" and that tools do not support it.
- Source: Bacchelli & Bird, "Expectations, Outcomes, and Challenges of Modern Code Review", ICSE 2013. https://www.microsoft.com/en-us/research/?p=164195 (May 2013)

**1.2 At Google the primary purposes are education, maintaining norms, gatekeeping and accident prevention; defect finding is "welcomed but not the only focus".**
- Evidence: 12 interviews, 44 survey responses, logs of 9M reviewed changes. Original motive was making code understandable to other developers. Median change is 24 lines; ~90% touch fewer than 10 files; median reviewer count is 1; median total review latency is under 4 hours (under 1 hour to first feedback for small changes). Comments per change grow with size, peaking at 12.5 for ~1,250-line changes.
- Source: Sadowski et al., "Modern Code Review: A Case Study at Google", ICSE-SEIP 2018. https://research.google/pubs/modern-code-review-a-case-study-at-google/ (2018)

**1.3 Only about 15% of review comments point at a possible defect; most concern evolvability.**
- Evidence: Microsoft analysis reports ~15% of comments indicate a possible defect, fewer still a blocking one. Mäntylä & Lassenius (2009) found roughly three quarters of defects found in review are evolvability (maintainability) defects, not functional ones. The Microsoft paper's own title overstates this: reviewed code is still reported as about half as likely to introduce defects as unreviewed code.
- Sources: Czerwonka, Greiler, Tilford, "Code Reviews Do Not Find Bugs", ICSE-SEIP 2015, https://www.microsoft.com/en-us/research/?p=168182 (2015). Mäntylä & Lassenius, "What Types of Defects Are Really Discovered in Code Reviews?", IEEE TSE 2009 (75:25 split from my reading of the paper; **[secondary]** in this session). Counterpoint: https://entropicthoughts.com/code-reviews-do-find-bugs.

**1.4 About a third of human review comments are not useful; usefulness rises with reviewer familiarity.**
- Evidence: classifier over 1.5M Microsoft comments. 34.5% of comments not useful. A reviewer's share of useful comments climbs sharply in their first year and then plateaus; prior experience with the file matters.
- Source: Bosu, Greiler, Bird, "Characteristics of Useful Code Reviews", MSR 2015. https://www.microsoft.com/en-us/research/?p=168510 (2015). 34.5% figure **[secondary]** via https://arxiv.org/abs/2307.00692v2.

**1.5 Confusion is common and its top causes are missing rationale and unfamiliarity with the existing code.**
- Evidence: survey plus comment analysis. 30 reasons, 14 impacts, 13 coping strategies. Confusion delays merge, lowers review quality and increases discussion. The main coping strategy is asking the author for information.
- Source: Ebert et al., "Confusion in Code Reviews", SANER 2019. https://aserebre.win.tue.nl/SANER2019Felipe.pdf (2019)

**1.6 Understanding is now the bottleneck for AI-written code.** Developers who delegate code generation to AI understand it worse; those who use AI to ask conceptual questions do not.
- Evidence: RCT, 52 developers learning a new library. AI group scored 50% vs 67% on a comprehension quiz (d=0.74, p=0.01), largest gap on debugging questions. Within the AI group, delegating patterns scored 24–39%; patterns that asked follow-up and conceptual questions scored 65–86%.
- Source: Anthropic, "How AI assistance impacts the formation of coding skills". https://www.anthropic.com/research/AI-assistance-coding-skills (29 Jan 2026). **[single]** and **[vendor]** (Anthropic research, not peer reviewed); small sample, immediate test only.

Implication: the goals that matter most are (a) the team understanding the change, (b) design/maintainability, (c) catching consequential defects. Defect-catching is where AI adds most; understanding is where a human must stay in the loop.

---

## 2. Structure: order, rubrics, severity, taxonomy, size and speed

**2.1 Order: intent and design first, then the core files, then the rest.**
- Evidence: Google's reviewer guide: read the description and decide if the change should exist; review the main logical files first and send major design comments immediately "since they may make the rest of the review unnecessary"; then the rest in a logical sequence; tests first can clarify intent.
- Source: Google Engineering Practices, "Navigating a CL in review". https://google.github.io/eng-practices/review/reviewer/navigate.html (living doc, accessed Oct 2026). Practitioner guidance, not a study.

**2.2 File position biases what humans find; alphabetical order is arbitrary.**
- Evidence: 219,476 PRs from 138 Java projects plus an experiment. For one defect type, reviewers had 64% lower odds of finding it when its file was last rather than first.
- Source: Fregnan et al., "First Come First Served: The Impact of File Position on Code Review", ESEC/FSE 2022. https://arxiv.org/abs/2208.04259 (Aug 2022). Follow-ups: https://arxiv.org/abs/2506.10654 (2025), https://arxiv.org/pdf/2609.22610 (Sep 2026).
- Related: Baum, Schneider, Bacchelli (EMSE 2019) found working-memory capacity relates to finding delocalized defects and a clear benefit of small changes, but could **not** reliably show that a particular ordering improves efficiency. https://www.zora.uzh.ch/entities/publication/245eb643-3ebd-476c-a613-8097ff6d9582 (2019)

**2.3 Reviewing tests first finds more test-code defects, at a cost to maintainability findings in production code.**
- Evidence: controlled experiment; same proportion of production defects found, more test defects, fewer production maintainability issues.
- Source: Spadini et al., "Test-Driven Code Review", ICSE 2019. https://pure.tudelft.nl/ws/files/46651094/main.pdf (2019)

**2.4 Size and speed limits: ~200–400 LOC per review, ≤ ~500 LOC/hour, ≤ ~60 minutes per session.**
- Evidence: SmartBear/Cisco study, 2,500 reviews and 3.2M LOC. Defect density found drops sharply past ~400 LOC and above ~500 LOC/hour; effectiveness drops after about an hour.
- Source: SmartBear, "Best Practices for Code Review" https://smartbear.com/learn/code-review/best-practices-for-peer-code-review/ and the case study write-up https://agileconnection.com/article/largest-case-study-code-reviews-ever (2006). **[vendor]**; thresholds vary between 300–500 LOC/hour across retellings; primary data not public.
- Corroboration: Google's median change of 24 lines with sub-4-hour latency (1.2); Baum 2019 benefit of small changes (2.2).

**2.5 Label comment severity so authors know what blocks.**
- Evidence: Google uses "Nit:", "Optional/Consider:", "FYI:". Conventional Comments defines labels (praise, nitpick, suggestion, issue, todo, question, thought, chore, note) with decorations (blocking, non-blocking, if-minor).
- Sources: https://google.github.io/eng-practices/review/reviewer/comments.html (accessed Oct 2026); https://conventionalcomments.org/ (accessed Oct 2026). Convention, not experiment.

**2.6 Rubrics/taxonomies raise AI reviewer precision more than prompt tricks do.**
- Evidence: Atlassian's ablation: review guidelines added ~5 points of location accuracy; persona, chain-of-thought, PR and Jira context each 1–3 points. ByteDance's BitsAI-CR is built on a taxonomy of review rules plus a filter stage and reports 75% precision.
- Sources: RovoDev Code Reviewer, ICSE-SEIP 2026, https://arxiv.org/html/2601.01129v2 (Jan 2026) **[vendor]**; BitsAI-CR, https://arxiv.org/abs/2501.15134 (Jan 2025) **[vendor]**.

**2.7 Speed matters, and nudges help.**
- Evidence: Microsoft's Nudge randomized trial on 147 repos cut resolution time of overdue PRs by 60% (8,500 PRs). Meta's nudge study cut time in review by 6.8%.
- Sources: https://arxiv.org/abs/2011.12468v5 (TOSEM 2023); https://2022.esec-fse.org/details/fse-2022-research-papers/26/Using-Nudges-to-Accelerate-Code-Reviews-at-Scale (2022).

---

## 3. How reviewers build understanding

**3.1 Understanding the change is the reviewer's main need, and tools under-serve it** (see 1.1, 1.5). Missing rationale is the top confusion cause.

**3.2 Untangled (decomposed) changes help reviewers.**
- Evidence: controlled experiment on decomposing tangled changes into cohesive parts; decomposition reduced wrongly reported issues and helped understanding of the change rationale (details from abstract; full text blocked in this session).
- Source: Di Biase et al., "The effects of change decomposition on code review — a controlled experiment", PeerJ CS 2019. https://arxiv.org/abs/1805.10978 / https://pmc.ncbi.nlm.nih.gov/articles/PMC7924728/ (2019). **[secondary]** for the direction of effects.

**3.3 AI-written PR descriptions correlate with faster review and higher merge rates.**
- Evidence: 18,256 PRs using Copilot for PRs; less review time (reported 19.3 hours on average) and higher merge likelihood. Observational, so selection effects are possible. Developers often edit the generated text.
- Source: Xiao et al., "Generative AI for Pull Request Descriptions", FSE 2024. https://arxiv.org/abs/2402.08967 (Feb 2024).

**3.4 Developers prefer an AI summary up front, especially for large or unfamiliar PRs.**
- Evidence: field study at WirelessCar comparing AI-led (review generated up front) vs on-demand assistance. AI-led was preferred, conditional on codebase familiarity and PR severity; automatic summarization of complex PRs was valued; false positives and trust were the main concerns.
- Source: "Rethinking Code Review Workflows with LLM Assistance", ESEM 2025. https://arxiv.org/abs/2505.16339 (May 2025). Small industrial sample.

**3.5 Tour-style tooling exists and is being productized: grouping related hunks and explaining them in a reading order.**
- Evidence: Devin Review groups logically related diffs, orders and explains them, detects moved/copied code, flags bugs, and offers chat on a change.
- Source: CodeZine report on the early release, https://codezine.jp/news/detail/23079 (Jan 2026). **[vendor]**, **[secondary]**, no measured outcomes published.

**3.6 There is no controlled evidence yet that an LLM explanation-first step improves human defect detection or comprehension.**
- Evidence: the closest study (eye tracking, 32 practitioners, crossover) found that labelling code as LLM-generated raised fixation time (up to ~60% on a 25-line region) but did **not** change scan thoroughness; it did not measure detection.
- Source: "Same Scrutiny, More Time", ASE 2026 preprint. https://arxiv.org/html/2606.26505v1 (25 Jun 2026).
- Gap: this is a design bet for aiman, to be measured, not an established result. The supporting indirect evidence is 1.1, 1.5, 1.6 and 3.2–3.4.

---

## 4. AI code review: precision, noise and what works

### 4.1 Field outcomes (how often comments get acted on)

| System | Metric | Value | Date | Source | Flag |
|---|---|---|---|---|---|
| Google ML comment resolution | reviewer comments resolved by applying ML edit | ~5–7.5% | 2023–24 | https://research.google/blog/resolving-code-review-comments-with-ml/ | vendor |
| Qodo PR-Agent in industry | automated comments resolved | 73.8%; PR closure time rose 5h52m → 8h20m | Dec 2024 | https://arxiv.org/abs/2412.18531 | peer-reviewed (ICSE-SEIP 2025) |
| Atlassian RovoDev | code resolution rate | 38.7% vs 44.5% for human comments; median cycle time −31%; human comments/PR −36% | Jan 2026 | https://arxiv.org/html/2601.01129v2 | vendor (peer-reviewed SEIP) |
| OpenAI Codex review | comments leading to a code change | 52.7% overall; 46% on Codex PRs vs 53% on human PRs; comments on 36% of Codex PRs | 1 Dec 2025 | https://alignment.openai.com/scaling-code-verification/ | vendor |
| GitHub Copilot review | reviews with actionable feedback | 71% (29% silent), ~5.1 comments/review, +8.1% positive feedback after agentic redesign | 5 Mar 2026 | https://github.blog/ai-and-ml/github-copilot/60-million-copilot-code-reviews-and-counting/ | vendor |
| Cursor Bugbot | flagged bugs resolved by merge | 52% → 70%+; resolved bugs/PR 0.2 → ~0.5 | 15 Jan 2026 | https://cursor.com/blog/building-bugbot | vendor |
| Anthropic Code Review | PRs with substantive comments; findings marked incorrect | 16% → 54%; <1% marked incorrect; 84% of >1,000-line PRs get findings (avg 7.5) | 9 Mar 2026 | https://claude.com/blog/code-review | vendor |
| Graphite Diamond | downvote rate; acceptance | <4% downvoted; ~52% accepted | 2025 | https://ai.engineer/talks/TswQeKftnaw-ai-powered-entomology-lessons-from-millions-ai | vendor, speaker-reported |

**4.2 Independent studies show far lower uptake than vendors report, and noise is the main cause.**
- Evidence A: 54,791 agent comments in 342 Python repos. Resolution rates: Copilot 72.9%, Cursor 67.2%, Codex 54.8%, but ~93% of *unresolved* comments got no reply at all. Among discussed rejections, "intentional design decision" (24%) and "incorrect suggestion" (14%) dominate. Inline code suggestions were the strongest predictor of acceptance (75.5% vs 64.6%, OR ≈1.62); longer, explanation-heavy comments did slightly worse.
  Source: "Go Home Copilot, You're Drunk", https://arxiv.org/html/2607.21997v2 (Jul 2026).
- Evidence B: 31,073 CodeRabbit review/feedback pairs across 239 repos: 36.4% accepted, 7.3% discussed, 56.3% rejected. Rejections were mainly invalid suggestions (false positives, redundant, out-of-scope) and misalignment with developer intent.
  Source: "Is Agentic Code Review Helpful?", https://arxiv.org/abs/2607.03316 (Jul 2026). A secondary summary gives false positives 43.3% and design trade-offs 23.7% of rejections **[secondary]**.
- Evidence C: MSR 2026 study of the AIDev dataset: PRs reviewed only by agents merged 45.2% vs 68.4% for human-only; 60% of agent-only PRs had a signal ratio of 30% or less; 12 of 13 agents averaged below 60% signal. A cited prior study found human comments addressed ~60% of the time vs 0.9–19.2% for agents. Correlational.
  Source: "From Industry Claims to Empirical Reality", https://arxiv.org/html/2604.03196v1 (Apr 2026).

**4.3 Precision beats recall for trust; silence is a valid output.**
- Evidence: OpenAI explicitly accepts lower recall for higher precision, framing each comment as P(correct)·benefit − verification cost − P(incorrect)·false-alarm cost. GitHub: "Silence is better than noise"; 29% of reviews post nothing. Google's ML edits: serving-time filters "traded quantity for quality" and raised real acceptance.
- Sources: OpenAI (1 Dec 2025), GitHub (5 Mar 2026), Google (May 2023) as in 4.1. All **[vendor]**, but three vendors agree independently.

**4.4 A separate verification/filter stage is the most effective noise reducer; actionability filters beat factual judges.**
- Evidence: Atlassian's ablation: an actionability classifier (ModernBERT trained on ~50k comments labelled by whether they led to a change) added ~15–20 points; an LLM factual-correctness judge had minimal effect. ByteDance uses RuleChecker → ReviewFilter for 75% precision. Anthropic verifies each candidate bug before posting. Bugbot v1 used 8 passes with randomized diff order, majority voting, then a validator model.
- Sources: https://arxiv.org/html/2601.01129v2 (Jan 2026) **[vendor]**; https://arxiv.org/abs/2501.15134 (Jan 2025) **[vendor]**; https://claude.com/blog/code-review (Mar 2026) **[vendor]**; https://cursor.com/blog/building-bugbot (Jan 2026) **[vendor]**.

**4.5 Execution beats argument: repo access and running code reduce false alarms; running tests corrects LLM over-rejection.**
- Evidence A: OpenAI found repo-wide access plus code execution caught more critical issues with fewer false alarms than diff-only review **[vendor]** (1 Dec 2025).
- Evidence B: LLM reviewers over-reject correct code, and asking for explanations and fixes makes it worse (GPT-4o false-negative rate on HumanEval 26.2% with a yes/no prompt vs 73.2% with judgment+explanation+fix). A fix-guided verification filter that runs original and proposed fix against tests cut average false negatives from 54.8% to 16.3% (HumanEval), 69.0% to 28.9% (MBPP), 51.0% to 24.0% (QuixBugs).
  Source: Jin & Chen, https://arxiv.org/html/2603.00539v1 (28 Feb 2026). Benchmark tasks, not real PRs.

**4.6 Agentic review (tool use, context gathering) outperforms single-shot diff review.**
- Evidence: Bugbot's largest gains came from moving to a fully agentic design (fall 2025); GitHub's agentic architecture gave +8.1% positive feedback; OpenAI's repo access result (4.5).
- Sources: as above, all **[vendor]**.

**4.7 Missing context causes most wrong comments; more context gives only minor gains.**
- Evidence: Atlassian: missing language/framework/version context produced incorrect or non-actionable comments; adding more context helped only slightly. "Go Home Copilot": agents struggle with project-specific context, producing rejected design suggestions.
- Sources: https://arxiv.org/html/2601.01129v2; https://arxiv.org/html/2607.21997v2.

**4.8 What developers accept vs ignore.**
- Accept: concrete, actionable fixes, especially with an inline code suggestion (4.2A); consequential logic and maintainability issues (GitHub, 4.3); comments grouped by pattern rather than repeated (GitHub clustering).
- Ignore or reject: false positives; comments that contradict an intentional design decision; redundant or out-of-scope remarks; long explanations without a rule or benefit (4.2A/B).
- Thumbs up/down are rarely used; resolution at merge time is the usable metric (Atlassian, Cursor).

---

## 5. Multi-reviewer and multi-model review

**5.1 More independent passes raise recall substantially; precision is the weak point.**
- Evidence: SWR-Bench (1,000 verified PRs). Aggregating 10 self-runs raised F1 by up to 43.7% and recall by 118.8% (Gemini 2.5 Flash), with precision remaining the open problem. Different LLMs overlapped little in what they found.
- Source: https://arxiv.org/abs/2509.01494 (Sep 2025, rev. Jun 2026).

**5.2 A second model family helps when it is top-tier; a third reviewer adds little.**
- Evidence: 856 review sessions, 150 planted errors. Fresh-session same-model + GPT-5.4 cross-model review found 56.7% vs 42.7% for two fresh same-model reviews (+14 pp, p=.006). But vs two cross-model reviews the gain was not significant. A third reviewer added 1.3 pp. A lightweight cross-model reviewer was no better than self-review. Same-model review was better on code; cross-model better on documents. Cross-model pairs overlapped 37–46% vs 52–62% within-model. Cross-model reviewers added false positives.
- Source: "When Does a Second Model Help?", https://arxiv.org/html/2610.01471v2 (6 Oct 2026). **[single]**, single author, one generator, planted errors.
- Contrary evidence: a search summary of the same line of work reported no detectable difference between mixed-family and same-model union recall in some conditions; treat the cross-family benefit as plausible, not proven.

**5.3 Why another family should help: model errors are correlated, more so within a provider.**
- Evidence: across 350+ LLMs, models agree on ~60% of the questions they both get wrong; shared provider and architecture raise correlation; stronger models correlate more even across providers.
- Source: Kim et al., "Correlated Errors in Large Language Models", ICML 2025. https://arxiv.org/abs/2506.07962 (Jun 2025).

**5.4 Same-model judges favour their own outputs.**
- Evidence: judges prefer their own generations even unlabelled; mechanism tied to low perplexity (familiarity).
- Sources: Wataoka et al., "Self-Preference Bias in LLM-as-a-Judge", https://arxiv.org/abs/2410.21819 (Oct 2024, rev. Jun 2025); Panickssery et al., https://arxiv.org/abs/2404.13076 (Apr 2024).
- Implication: when the verifier is the same family as the primary, it is a weaker check on that primary's findings.

**5.5 Ensembles in production: GitHub measured gains; Cursor used voting.**
- Evidence: GitHub's ReviewBench builds a golden set from humans, follow-up commits, static tools and multiple model families, then **semantically deduplicates** so agreement does not inflate counts; a true positive must be "true, relevant, and non-trivial". A multi-run ensemble in an online A/B test: addressed rate +8.0%, recall +13.6%, comment volume +61%, cost −8.0%. Critical comments rose ~262%.
- Source: https://github.blog/ai-and-ml/github-copilot/reviewbench-an-open-benchmark-for-ai-code-review/ (5 Oct 2026). **[vendor]**.
- Evidence: Bugbot v1: 8 parallel passes, randomized diff order, group similar findings, keep only those flagged by multiple passes, validator model, dedupe against earlier runs. https://cursor.com/blog/building-bugbot (Jan 2026) **[vendor]**.
- Note the volume risk: +61% comments is exactly the noise that 4.2 shows developers reject. Ensembles need a precision gate after the merge.

**5.6 Anchoring: framing in the PR description collapses LLM reviewer detection; redaction restores it.**
- Evidence: telling a model vulnerable code was secure cut GPT-4o-mini detection from 97.2% to 3.6%; most models dropped 16–60 points (Claude Opus 4.5 about 7). LLM-refined persuasive PR metadata got reverted CVEs past Claude Code (17/17) and CodeRabbit (15/16). Removing the PR description restored detection in 16 of 32 cases; also removing commit messages and code comments restored the rest.
- Source: "When AI Agrees Too Much", https://arxiv.org/html/2603.18740 (Mar 2026, v5 27 Oct 2026).
- In humans: people who see AI suggestions before forming a judgment align more with wrong AI assessments, and time pressure worsens it (pathology and general automation-bias literature: https://arxiv.org/abs/2603.11821, Mar 2026; https://link.springer.com/article/10.1007/s00146-025-02422-7, 2025). Not code-review specific.
- Implication: the second reviewer should review blind (no access to the first reviewer's findings, and ideally a pass without the author's narrative) before any reconciliation.

---

## 6. Human-in-the-loop review with AI

**6.1 Explanations alone do not reliably prevent over-reliance; cognitive forcing does.**
- Evidence: 199-participant experiment. Cognitive forcing functions (e.g. committing to your own decision before seeing the AI's, or waiting/asking to see it) significantly reduced over-reliance compared with explanation-only designs, did not eliminate it, and were rated least pleasant. Benefits concentrated in people high in need-for-cognition. Vasconcelos et al. (2022) show explanations can help when they are cheaper to verify than the task itself.
- Sources: Buçinca, Malaya, Gajos, CSCW 2021, https://arxiv.org/abs/2102.09692 (Feb 2021); Vasconcelos et al., https://arxiv.org/abs/2212.06823 (Dec 2022).

**6.2 AI help lowers understanding unless the human asks conceptual questions** (1.6). Highest-scoring AI users asked "why" questions; full delegators scored lowest.

**6.3 AI assistance makes people overconfident about security.**
- Evidence: participants with an AI assistant wrote insecure code more often on 4 of 5 tasks and were more likely to believe their code was secure. Those who trusted the AI less produced fewer vulnerabilities.
- Source: Perry et al., "Do Users Write More Insecure Code with AI Assistants?", CCS 2023. https://arxiv.org/abs/2211.03622 (Nov 2022 / Nov 2023).

**6.4 Vendors themselves say AI review must not replace human approval.**
- Evidence: OpenAI calls reviews "a support layer, not a guarantee" and flags over-reliance as a serious risk; Anthropic's Code Review does not approve PRs; MSR 2026 data show agent-only PRs merge less and are abandoned more (4.2C); Atlassian keeps humans as final decision makers.
- Sources: as in 4.1/4.2.

**6.5 AI review shifts human effort, it does not remove it.**
- Evidence: Atlassian saw human comments per PR fall 36% (risk of humans stepping back); industry Qodo deployment saw PR closure time rise 42% (more to process). A 2026 vision paper cites a study where AI assistance surfaced more low-severity issues but no additional high-severity defects.
- Sources: https://arxiv.org/html/2601.01129v2; https://arxiv.org/abs/2412.18531; https://arxiv.org/pdf/2605.17548 (May 2026) **[secondary]** for the cited study.

**6.6 What to ask the human.** No study tests specific question sets for AI-led review. The evidence points to questions that (a) target missing rationale and unfamiliar code (1.5), (b) are conceptual "why/what if" questions rather than "does this look OK" (1.6, 6.1), (c) require the human to state a judgment before seeing the AI's (6.1, 5.6), and (d) cover design intent, where agents are most often wrong (4.2A: intentional design decisions are the top rejection reason). This is an inference from the cited findings, not a tested protocol.

---

## 7. Security, test adequacy, and AI-generated code

**7.1 Human security review is weak and highly variable; experience does not predict effectiveness.**
- Evidence: 30 developers reviewed the same web app; overall effectiveness was low, variance high, and experience/education did not correlate with finding vulnerabilities; the authors model how many independent reviewers are needed.
- Source: Edmundson et al., ESSoS 2013. https://people.eecs.berkeley.edu/~daw/papers/coderev-essos13.pdf (2013).
- Implication: security needs a dedicated checklist pass and tools, not general reading.

**7.2 AI-generated code has frequent security flaws, and newer models have not improved on security.**
- Evidence: 100+ LLMs on 80 CWE-based tasks: insecure in ~45% of cases; Java >70%; XSS defences missing in 86% of relevant samples; security did not improve while syntax did.
- Source: Veracode 2025 GenAI Code Security Report. https://www.veracode.com/press-release/ai-generated-code-poses-major-security-risks-in-nearly-half-of-all-development-tasks-veracode-research-reveals/ (30 Jul 2025). **[vendor]**.

**7.3 AI-co-authored PRs carry more and more severe issues.**
- Evidence: 470 OSS PRs; 10.83 issues per AI PR vs 6.45 per human PR (~1.7x); 1.4x more critical and 1.7x more major issues. Security multiplier reported inconsistently (1.57x vs 2.74x).
- Source: CodeRabbit, "State of AI vs Human Code Generation", https://www.coderabbit.ai/blog/state-of-ai-vs-human-code-generation-report (17 Dec 2025). **[vendor]**; labels of "human" PRs uncertain.

**7.4 AI reviewers do slightly worse on AI-written code, and may be gamed by it.**
- Evidence: OpenAI: 46% of comments led to changes on Codex PRs vs 53% on human PRs; detection falls faster with less thinking budget on Codex-generated code; they monitor whether the model games its own checks.
- Source: https://alignment.openai.com/scaling-code-verification/ (1 Dec 2025) **[vendor]**. Combined with 5.6 (persuasive metadata defeats reviewers): an agent-written PR description is untrusted input to the reviewer.

**7.5 Test adequacy: surfacing surviving mutants on changed lines during review works at scale.**
- Evidence: Google mutates only changed lines and surfaces surviving mutants in code review: ~17M mutants over 760k changes, 2M surfaced; developer feedback collected per mutant; a later study links surfacing to better tests over time.
- Sources: Petrović et al., "Practical Mutation Testing at Scale", TSE 2021, https://research.google/pubs/practical-mutation-testing-at-scale-a-view-from-google/ (2021); Ivanković/Fraser/Just ICSE 2023 follow-up (via the same page). Exact usefulness percentage not retrieved **[secondary]**.

**7.6 Test-first reading finds more test defects** (2.3). Execution checks correct LLM verdicts (4.5).

---

## 8. Output: how to post review results

**8.1 One overview comment plus inline comments on specific lines/ranges.**
- Evidence: Anthropic posts one overview and inline comments per bug, ranked by severity. GitHub moved to multi-line (range) comments and clusters repeated patterns into one comment with batch autofix.
- Sources: https://claude.com/blog/code-review (Mar 2026); https://github.blog/ai-and-ml/github-copilot/60-million-copilot-code-reviews-and-counting/ (Mar 2026). **[vendor]**.

**8.2 Include a concrete suggested change where possible.**
- Evidence: inline code suggestions are the strongest acceptance predictor (OR ≈1.62) (4.2A). Google's one-click ML edits resolve a measurable share of comments (4.1).
- Mechanism: GitHub suggestion blocks (```` ```suggestion ````) inside review comments, which the author can commit in a batch. https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/reviewing-changes-in-pull-requests/commenting-on-a-pull-request

**8.3 State the problem, why it matters, and the fix; keep it short; label severity.**
- Evidence: GitHub: comments should explain the problem and the fix (vendor). Google guide: explain why, label Nit/Optional/FYI, be courteous. "Go Home Copilot": longer, explanation-heavy comments did slightly worse, except explanations citing a rule, a benefit, or an example.
- Sources: 4.1, 2.5, 4.2A.

**8.4 Submit as one batched review, not a stream of comments, and do not let AI approve.**
- Mechanism: GitHub's "Create a review" API takes all inline comments plus a body and one event (`COMMENT`, `REQUEST_CHANGES`, `APPROVE`); a pending review can be built up and submitted once. https://docs.github.com/en/rest/pulls/reviews (accessed Oct 2026).
- Evidence for not auto-approving: 6.4.

**8.5 Explanations of intent belong in the code or PR, not only in the review thread.**
- Evidence: Google: if a reviewer did not understand code, the fix is usually clearer code or a comment, because explanations in the review tool do not help future readers.
- Source: https://google.github.io/eng-practices/review/reviewer/comments.html.

**8.6 Measure by resolution at merge, not reactions.**
- Evidence: Atlassian found thumbs up/down rarely used; Cursor's LLM-judged resolution-at-merge metric drove its improvements; BitsAI-CR uses an "Outdated Rate" (comment's code later changed).
- Sources: 4.1, 4.4.

---

## Evidence gaps

- No controlled study shows that an AI walkthrough before review improves human defect detection; the best study (3.6) shows more time but not more thoroughness.
- Cross-family reviewer benefit rests on one 2026 preprint (5.2) plus indirect evidence (5.3, 5.4).
- Most AI-review precision numbers are vendor-reported with incompatible definitions (resolution rate, addressed rate, acceptance, "marked incorrect"). Independent studies (4.2) report much lower uptake.
- No tested protocol for which questions an AI should ask a human reviewer.

---

## Implications for aiman review mode

Design principle: the review produces two artifacts, a verified set of findings for the PR and an engineer who understands the change. The human signs off; agents never approve.

### Roles

| Role | Who | Job |
|---|---|---|
| Lead reviewer | smart-tier agent (`agents.smart`), repo access + execution | Builds the tour, runs the structured review, reconciles findings, drafts the GitHub review |
| Second reviewer | smart-tier agent from a **different provider** than the lead (fallback: same agent, fresh session) | Blind independent review |
| Verifier | lead in a fresh context, or the second reviewer on the lead's findings | Tries to reproduce each finding before it can be posted |
| Human reviewer | the operator | Answers questions, records their own judgment, decides what is posted, owns approval |

Justification for a different provider: 5.2, 5.3, 5.4. Use only top-tier models for the second reviewer; lightweight cross-model reviewers did no better than self-review (5.2). Do not add a third reviewer by default (5.2: +1.3 pp).

### Phases

**Phase 0: Intake and sizing.** Operator picks a PR. aiman fetches PR, diff, linked JIRA, CI state, checks out the head in a worktree. Compute size; above ~400 changed LOC, warn and propose splitting the walkthrough into chunks of ≤ ~400 LOC with a session budget of about an hour per chunk (2.4, 1.2). Mark the PR description and commit messages as untrusted author claims, especially when an agent wrote the PR (5.6, 7.4).
- Artifact: `review/intake.json` (size, files, risk areas, CI state).

**Phase 1: Blind independent reviews, in parallel.** Both reviewers review before seeing each other's output (5.6). Each runs the same rubric in the same order: intent and design, then correctness, then security, then tests, then maintainability; style is out of scope unless a repo rule says otherwise (2.1, 1.3, 2.6). Each does one pass with the PR description and one "redacted" pass on the code alone, to resist framing (5.6). Reviewers must use repo context and may run tests (4.5, 4.6). A security sub-pass uses a checklist (CWE/OWASP-style) because general reading misses vulnerabilities (7.1, 7.2). A test-adequacy sub-pass reads tests first and checks changed lines for untested behavior, ideally by mutating changed lines and seeing what survives (2.3, 7.5).
- Artifact: `review/findings-<agent>.json`, each finding with file/range, category, severity (blocking / should-fix / nit / question), claim, evidence, proposed fix.

**Phase 2: Merge, dedupe, verify.** Lead semantically deduplicates the two sets (5.5) and records agreement. Every blocking or should-fix finding goes through verification: reproduce it (failing test, script, or concrete trace), or run the proposed fix against tests (4.5). Unverifiable findings are downgraded to questions, not posted as bugs (4.3). Agreement between reviewers raises confidence but does not replace verification, because errors correlate (5.3). Drop findings below the posting bar; cluster repeated patterns into one finding (8.1). Expect silence on some PRs and treat that as a valid result (4.3).
- Artifact: `review/findings.json` with `verified: reproduced|fix-tested|reasoned|unverified`, `found_by: [lead, second]`.

**Phase 3: Walkthrough for the human (explanation-first).** Lead produces a tour in reading order: the problem and intent (as claimed by the author, flagged as a claim), the design in one paragraph and an optional diagram, then hunks grouped by concern with the core logic first and tests read early, with moved/generated code collapsed (2.1, 2.2, 3.2, 3.5). Do **not** show AI findings yet (5.6, 6.1). Keep each chunk within the size budget (2.4).
- Artifact: `review/tour.md` (rendered in the TUI; optional pr-walkthrough deck).

**Phase 4: Questions to the human (cognitive forcing).** Before revealing findings, the lead asks a short set of questions and records the answers:
1. In your words, what does this change do and why? (comprehension check; 1.6)
2. Is the design the right one? What alternative would you have chosen? (design first; 2.1)
3. Two or three targeted "what happens if…" questions at the riskiest hunks, chosen from findings without revealing them (6.1, 6.2).
4. Questions only the human can answer: intended behaviour, team conventions, whether an apparent oddity is deliberate (4.2A: "intentional design decision" is the top reason AI comments are rejected; 4.7).
5. "Anything that worries you that I have not mentioned?"
The human's answers and their own concerns become findings with `found_by: human`. Then reveal the AI findings side by side with the human's, and have the human mark each AI finding agree / disagree / unsure, with a one-line reason for blocking ones (6.1, 6.4). Answers that contradict a finding (e.g. "that's deliberate") feed back into the finding before posting.
- Artifact: `review/human.json` (answers, human findings, per-finding verdicts). Store a short note in shared context so the understanding survives the session (aiman's existing handover/context store).

**Phase 5: Post to GitHub (human-approved).** The human sees the draft review and edits or drops comments. aiman posts **one** batched review via the reviews API (8.4):
- Body: a summary (what the change does, verdict, counts by severity, what was verified and how, what was not reviewed), and a disclosure that agents assisted and which human reviewed.
- Inline comments only for verified findings, on line ranges, each labelled (`blocking:`, `suggestion:`, `question:`, `nit (non-blocking):`, Conventional-Comments style), stating problem, why, and fix, with a ```` ```suggestion ```` block when the fix is local (8.1–8.3, 2.5).
- Event: `COMMENT` or `REQUEST_CHANGES` chosen by the human. `APPROVE` only when the human explicitly picks it, never by an agent (6.4).
- Questions about intent go to the author as `question:` comments; answers that explain code should end up in the code (8.5).

**Phase 6: Follow-up and measurement.** Serve's PR watcher (`internal/ciwatch`) checks at merge which posted comments were resolved (resolution-at-merge, 8.6, 4.1). Log per finding: who found it, verification status, posted or not, resolved or not, and whether the human agreed. This produces aiman's own precision and cross-model value data, which closes the evidence gaps above for this team.

### What each step guards against

| Risk | Mitigation in workflow | Finding |
|---|---|---|
| Noise erodes trust | verify before posting; silence allowed; cluster duplicates | 4.2, 4.3, 4.4, 5.5 |
| False positives on design intent | human answers intent questions before posting | 4.2A, 4.7 |
| Anchoring of second reviewer | blind parallel reviews; redacted pass | 5.6 |
| Same-family blind spots | different provider for second reviewer | 5.2, 5.3, 5.4 |
| Rubber-stamping by the human | human states own view before seeing AI findings; per-finding verdicts | 6.1, 6.3 |
| Engineer not understanding the change | tour first, conceptual questions, recorded answers | 1.1, 1.6, 3.4 |
| Oversized PRs | chunking at ~400 LOC, ~1 h per chunk | 2.4, 1.2 |
| Security and test gaps | checklist sub-pass; tests-first and mutation on changed lines | 7.1, 7.2, 7.5, 2.3 |
| Persuasive agent-written PR text | treat description as claim; redacted pass | 5.6, 7.4 |
