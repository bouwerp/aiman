# Initial-Prompt Text Box for New Sessions

**Date:** 2026-06-08
**Status:** Approved

## Summary

Replace the per-session "Inject Secrets" multi-select in the session summary
dialog with a single-line text box for an **initial prompt**. The box is the
default-focused element so the cursor is ready to type; pressing **Enter** starts
the session, sending the typed text as part of the initial prompt delivered to the
agent.

This works for both session types:

- **Ad-hoc sessions** — the typed text becomes the entire initial prompt
  (previously ad-hoc sessions had no initial prompt).
- **JIRA-based sessions** — the issue contents still feed in via `.aiman_task.md`
  and the standard "Read `.aiman_task.md`…" trigger; the typed text is **appended**
  so the user can add more instructions on top of the issue context.

## Decisions

- **Secrets:** stop injecting secrets into new sessions entirely. Remove the
  per-session selection UI and stop populating `SessionConfig.EnvSecrets`. The
  global secrets storage/management UI (`secrets_setup.go`) is **untouched** —
  only per-session injection is removed.
- **Prompt box:** single-line `textinput`. Enter starts the session (no multi-line
  textarea). This matches the existing single-line input pattern in the codebase
  and the "pressing enter starts the session" requirement.

## Design

### 1. `internal/ui/summary.go` — the dialog

**Remove** all secrets handling from `SummaryModel`:

- Fields `allSecrets []domain.Secret` and `selectedSecrets map[string]bool`.
- Method `SetSecrets(...)`.
- The `space`/`" "` toggle case in `Update()`.
- Helper `secretFocusStart()`.
- The "Inject Secrets" render block in `View()`.
- The `EnvSecrets` population loop in `GetSessionConfig()`.

**Add** a dedicated prompt input:

- New field `promptInput textinput.Model`, initialized in both `NewSummaryModel`
  and `NewAdHocSummaryModel` (single-line, width consistent with other fields,
  placeholder e.g. `"Initial prompt (optional)"`).
- Kept as a **separate struct field**, not folded into the existing `m.inputs`
  slice, to avoid disturbing the positional index helpers (`awsProfileIdx`,
  `awsRegionIdx`, `openRouterIdx`).

**Focus model:**

- The prompt input is focus index `0` and is **focused by default** (cursor ready
  to type) regardless of whether AWS/OpenRouter sections are present.
- Tab order: prompt input → AWS/OpenRouter inputs (if any) → Create button.
- `buttonFocusIndex()` becomes `len(m.inputs) + 1` (the `+1` accounts for the
  prompt input); the previous `+ len(m.allSecrets)` term is removed.
- Typing routes to whichever element is focused. When the prompt input is focused,
  key events delegate to `m.promptInput.Update(msg)`; the AWS/OpenRouter delegation
  is rebased to account for the prompt input occupying focus index 0.

**Enter behavior:** unchanged — `Update()` already confirms immediately when an
agent is selected. Enter from the focused prompt box therefore starts the session.

**Render:** show an "Initial Prompt" line near the top of the editable section for
both JIRA and ad-hoc sessions, with a focus marker (`>`) when focused. Update the
hint line: drop "space to toggle secret"; keep "tab to cycle fields".

**Capture:** `GetSessionConfig()` sets
`cfg.InitialPrompt = strings.TrimSpace(m.promptInput.Value())`.

### 2. `internal/domain/session.go` — config field

Add `InitialPrompt string` to `SessionConfig`.

### 3. `internal/usecase/flow_manager.go` — prompt assembly

After `SkillEngine.PrepareSession(...)` returns (around lines 164–168), join the
user-typed text onto the prepared prompt:

```
sendKeysPrompt = join(prepared.InitialPrompt, config.InitialPrompt)
```

- Empty user text → `sendKeysPrompt` unchanged.
- Empty base prompt (ad-hoc) + user text → user text becomes the whole prompt.
- Both present (JIRA + extra) → joined with `\n\n`, matching the existing
  multi-part prompt convention.

Reuse the skills package join logic by exporting `appendPrompt` → `AppendPrompt`,
or add a small local join helper. The existing `tmux send-keys` delivery block
(lines 251–267) is untouched.

### 4. `internal/ui/dashboard.go` — wiring

Remove the `SetSecrets(...)` call in the summary-setup path (around lines
4478–4492). No other change is needed; `cfg.EnvSecrets` simply stays empty.

### Secrets injection loop in flow_manager

The `for _, secret := range config.EnvSecrets` loop (lines 221–223) is **left in
place**. With nothing populating `EnvSecrets`, it iterates an empty slice and is a
no-op. Leaving working code avoids unnecessary churn; it can be removed later if
desired.

## Testing (TDD)

- **`SummaryModel` unit tests:**
  - Prompt input is focused by default after construction (both JIRA and ad-hoc).
  - Typing characters populates the prompt input.
  - Enter with an agent selected sets `confirmed`.
  - `GetSessionConfig()` returns the typed text in `InitialPrompt` and never
    populates `EnvSecrets`.
- **Prompt-join helper test:** base+user → `"base\n\nuser"`; empty base+user →
  `"user"`; base+empty → `"base"`.

## Out of Scope

- Global secrets storage and management UI (`internal/ui/secrets_setup.go`).
- Multi-line prompt input / textarea.
- Changes to `tmux send-keys` delivery mechanics.
