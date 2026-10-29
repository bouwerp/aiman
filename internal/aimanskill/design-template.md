# KEY: Title in plain words

<!-- Copy this file to .aiman_design.md in your working directory, fill every section in this order, and delete these comments. Write for an engineer who has not read the code. -->

**Revision:** n · **Size:** small | project · **Status:** draft | in review | approved | as built
**Task:** link to the issue · **Spec:** `docs/specs/KEY.md` (when there is one)

**Legend:** green = new, amber = changed, red dashed = removed. Every diagram uses these three classes:

```text
classDef new fill:#d4f7d4
classDef changed fill:#fde9b8
classDef removed stroke-dasharray:4,fill:#f9d0d0
```

In a sequence diagram, which has no classes, wrap new steps in `rect rgb(212,247,212)` and changed steps in `rect rgb(253,233,184)`.

## The problem

<!-- What is wrong or missing today and who feels it, in plain words, before any design. -->

## How it works today

<!-- The current domain model and flow for the part this touches, with a diagram and links to the files (path:line). -->

## Glossary

<!-- Every term this page uses that a newcomer would not know, one line each. -->

| Term | Meaning |
|---|---|

## Proposed design

<!-- The domain model first, then structure, then behaviour; mark each element new, changed or removed with the classes above. -->

### Domain model

```mermaid
classDiagram
    classDef new fill:#d4f7d4
    classDef changed fill:#fde9b8
    classDef removed stroke-dasharray:4,fill:#f9d0d0
```

### Structure

```mermaid
flowchart LR
    classDef new fill:#d4f7d4
    classDef changed fill:#fde9b8
    classDef removed stroke-dasharray:4,fill:#f9d0d0
```

### Behaviour

```mermaid
sequenceDiagram
```

## A scenario, end to end

<!-- One concrete case walked step by step through the new design, naming the real inputs, files and outputs. -->

## Alternatives considered

<!-- Each option that lost and the reason it lost. -->

## Open questions

<!-- Each question with the context needed to answer it: what it decides, the options, and what each option costs. -->

## Decisions

<!-- One row per consult objection and per answered question. Triage is accept, reject or defer; a reject or defer needs its reason. -->

| Objection | Severity | Triage | Reason |
|---|---|---|---|

## Breakdown

<!-- One row per piece. Pieces in the same parallel group own disjoint files. Acceptance tests are exact commands. -->

| Piece | Owns files | Parallel group | Depends on | Complexity | Tier | Acceptance tests |
|---|---|---|---|---|---|---|

## What you should be able to explain after reading

<!-- Three to five points the engineer can explain without the agent once they have read this page. -->

1.

## Deviations from the approved design

<!-- As built only: add this section when the work merges, listing each place the shipped code differs from the approved design and why. -->
