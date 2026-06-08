# Session Initial-Prompt Text Box Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-session secret-injection multi-select in the session summary dialog with a single-line, default-focused initial-prompt text box whose contents are sent to the agent when the user presses Enter.

**Architecture:** `SummaryModel` (Bubble Tea) loses all secrets state and gains a dedicated `promptInput textinput.Model` focused by default. The typed text is captured into a new `SessionConfig.InitialPrompt` field, merged in the dashboard, and stitched onto the agent prompt in `flow_manager.go` via a small `joinPrompt` helper before delivery over the existing `tmux send-keys` path.

**Tech Stack:** Go, Bubble Tea (`charmbracelet/bubbletea`, `bubbles/textinput`), lipgloss.

---

## File Structure

- `internal/domain/session.go` — add `InitialPrompt string` to `SessionConfig`.
- `internal/usecase/flow_manager.go` — add `joinPrompt` helper; stitch user text onto `sendKeysPrompt`.
- `internal/usecase/flow_manager_test.go` — **new**; unit test for `joinPrompt`.
- `internal/ui/summary.go` — remove secrets; add `promptInput`; rework focus model; capture into config.
- `internal/ui/summary_test.go` — **new**; unit tests for `SummaryModel`.
- `internal/ui/dashboard.go` — drop `SetSecrets(...)` wiring; copy `InitialPrompt` into `sessionCfg`.

---

## Task 1: Prompt-join helper + config field

**Files:**
- Modify: `internal/domain/session.go:152-183` (add field)
- Modify: `internal/usecase/flow_manager.go` (add helper)
- Test: `internal/usecase/flow_manager_test.go` (create)

- [ ] **Step 1: Add the `InitialPrompt` field to `SessionConfig`**

In `internal/domain/session.go`, inside the `SessionConfig` struct, add the field just below `EnvSecrets` (line ~182):

```go
	// EnvSecrets is the list of global secrets selected for injection into this session.
	// Each secret is added as a -e KEY=VALUE flag to the tmux new-session command.
	EnvSecrets []Secret
	// InitialPrompt is free-text entered in the session summary dialog. It is appended
	// to the agent's initial prompt (after any JIRA task trigger) and delivered via
	// tmux send-keys. Empty means no extra prompt text.
	InitialPrompt string
```

- [ ] **Step 2: Write the failing test for `joinPrompt`**

Create `internal/usecase/flow_manager_test.go`:

```go
package usecase

import "testing"

func TestJoinPrompt(t *testing.T) {
	cases := []struct {
		name       string
		base, user string
		want       string
	}{
		{"both present", "base prompt", "extra", "base prompt extra"},
		{"empty base", "", "extra", "extra"},
		{"empty user", "base prompt", "", "base prompt"},
		{"trims whitespace", "  base  ", "  extra  ", "base extra"},
		{"both empty", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := joinPrompt(c.base, c.user); got != c.want {
				t.Fatalf("joinPrompt(%q, %q) = %q, want %q", c.base, c.user, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/usecase/ -run TestJoinPrompt`
Expected: FAIL — `undefined: joinPrompt`.

- [ ] **Step 4: Implement `joinPrompt`**

In `internal/usecase/flow_manager.go`, add this function near the other package helpers (e.g. just above `func (m *FlowManager) CreateSession`):

```go
// joinPrompt appends user-entered prompt text to a base agent prompt. The base is
// typically the JIRA task trigger (empty for ad-hoc sessions). A single space
// separates the two parts so the combined prompt is delivered as one line via
// tmux send-keys (newlines are deliberately avoided — some agents submit on them).
func joinPrompt(base, user string) string {
	base = strings.TrimSpace(base)
	user = strings.TrimSpace(user)
	switch {
	case base == "":
		return user
	case user == "":
		return base
	default:
		return base + " " + user
	}
}
```

(`strings` is already imported in `flow_manager.go`.)

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/usecase/ -run TestJoinPrompt`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/domain/session.go internal/usecase/flow_manager.go internal/usecase/flow_manager_test.go
git commit -m "feat: add joinPrompt helper and SessionConfig.InitialPrompt"
```

---

## Task 2: Wire the user prompt into session creation

**Files:**
- Modify: `internal/usecase/flow_manager.go:162-169`

- [ ] **Step 1: Stitch `config.InitialPrompt` onto `sendKeysPrompt`**

In `CreateSession`, find this block (around lines 162-169):

```go
	agentCmd := config.Agent.Command
	var sendKeysPrompt string
	if m.SkillEngine != nil {
		prepared, err := m.SkillEngine.PrepareSession(ctx, sshMgr, workingDir, *config.Agent, config.Skills, config.PromptFree, config.Issue, config.PriorSnapshot)
		if err == nil {
			agentCmd = prepared.Command
			sendKeysPrompt = prepared.InitialPrompt
		}
	}
```

Add the join immediately after the closing brace of the `if m.SkillEngine != nil` block (so it also applies to ad-hoc text-only sessions where `SkillEngine` produced nothing):

```go
	agentCmd := config.Agent.Command
	var sendKeysPrompt string
	if m.SkillEngine != nil {
		prepared, err := m.SkillEngine.PrepareSession(ctx, sshMgr, workingDir, *config.Agent, config.Skills, config.PromptFree, config.Issue, config.PriorSnapshot)
		if err == nil {
			agentCmd = prepared.Command
			sendKeysPrompt = prepared.InitialPrompt
		}
	}
	// Append any free-text prompt entered in the summary dialog. For JIRA sessions
	// this follows the "Read .aiman_task.md…" trigger; for ad-hoc sessions it becomes
	// the entire prompt.
	sendKeysPrompt = joinPrompt(sendKeysPrompt, config.InitialPrompt)
```

The existing `if sendKeysPrompt != "" { ... }` send-keys delivery block (lines ~251-267) is unchanged.

- [ ] **Step 2: Verify the package builds**

Run: `go build ./internal/usecase/`
Expected: no output (success).

- [ ] **Step 3: Run the usecase tests**

Run: `go test ./internal/usecase/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/usecase/flow_manager.go
git commit -m "feat: deliver summary initial-prompt text to the agent"
```

---

## Task 3: Rework SummaryModel — remove secrets, add prompt input

**Files:**
- Modify: `internal/ui/summary.go`
- Test: `internal/ui/summary_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/summary_test.go`:

```go
package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSummaryModelPromptFocusedByDefault(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	if m.focusIndex != 0 {
		t.Fatalf("expected focusIndex 0 (prompt), got %d", m.focusIndex)
	}
	if !m.promptInput.Focused() {
		t.Fatal("expected prompt input to be focused by default")
	}
}

func TestSummaryModelAdHocPromptFocusedByDefault(t *testing.T) {
	m := NewAdHocSummaryModel("my-label")
	if m.focusIndex != 0 || !m.promptInput.Focused() {
		t.Fatal("expected ad-hoc prompt input focused by default")
	}
}

func TestSummaryModelTypingPopulatesPrompt(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi there")})
	m = updated.(SummaryModel)
	if got := m.promptInput.Value(); got != "hi there" {
		t.Fatalf("expected prompt %q, got %q", "hi there", got)
	}
}

func TestSummaryModelGetSessionConfigReturnsPromptNoSecrets(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("do the thing")})
	m = updated.(SummaryModel)
	cfg := m.GetSessionConfig()
	if cfg.InitialPrompt != "do the thing" {
		t.Fatalf("expected InitialPrompt %q, got %q", "do the thing", cfg.InitialPrompt)
	}
	if len(cfg.EnvSecrets) != 0 {
		t.Fatalf("expected no EnvSecrets, got %d", len(cfg.EnvSecrets))
	}
}

func TestSummaryModelEnterConfirmsWithAgent(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(SummaryModel)
	if !m.IsConfirmed() {
		t.Fatal("expected confirmed after Enter with agent set")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ui/ -run TestSummaryModel`
Expected: FAIL — `m.promptInput undefined` (field doesn't exist yet).

- [ ] **Step 3: Update the struct fields**

In `internal/ui/summary.go`, replace the secrets fields (lines 30-32):

```go
	// Global secrets available for injection
	allSecrets      []domain.Secret
	selectedSecrets map[string]bool
```

with the prompt input field:

```go
	// promptInput is a free-text initial prompt sent to the agent. Focused by default.
	promptInput textinput.Model
```

- [ ] **Step 4: Add a prompt-input constructor helper and use it in both constructors**

Add this helper near the constructors in `internal/ui/summary.go`:

```go
func newPromptInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Initial prompt (optional)"
	ti.Width = 40
	ti.Focus()
	return ti
}
```

Update `NewSummaryModel` to initialise it:

```go
func NewSummaryModel(issueKey, branch string, repo domain.Repo, directory string) SummaryModel {
	m := SummaryModel{
		issueKey:    issueKey,
		branch:      branch,
		repo:        repo,
		directory:   directory,
		promptFree:  true,
		inputs:      make([]textinput.Model, 0),
		promptInput: newPromptInput(),
	}

	return m
}
```

Update `NewAdHocSummaryModel`:

```go
func NewAdHocSummaryModel(label string) SummaryModel {
	return SummaryModel{
		branch:      label,
		adHoc:       true,
		promptFree:  true,
		inputs:      make([]textinput.Model, 0),
		promptInput: newPromptInput(),
	}
}
```

- [ ] **Step 5: Remove `SetSecrets` and rework the focus-index helpers**

Delete the entire `SetSecrets` method (lines 155-167).

Replace the index helpers `buttonFocusIndex` and `secretFocusStart` (lines 144-153):

```go
// buttonFocusIndex returns the focusIndex value that corresponds to the Create button.
// It always equals len(m.inputs) since the button lives after all text inputs.
func (m SummaryModel) buttonFocusIndex() int {
	return len(m.inputs) + len(m.allSecrets)
}

// secretFocusStart returns the focusIndex of the first secret toggle row.
func (m SummaryModel) secretFocusStart() int {
	return len(m.inputs)
}
```

with:

```go
// Focus index 0 is always the initial-prompt input. The AWS/OpenRouter inputs in
// m.inputs occupy focus indices 1..len(m.inputs); the Create button is last.

// inputFocusIndex maps an index into m.inputs to its focusIndex value.
func (m SummaryModel) inputFocusIndex(i int) int { return i + 1 }

// buttonFocusIndex returns the focusIndex value that corresponds to the Create button.
func (m SummaryModel) buttonFocusIndex() int {
	return len(m.inputs) + 1
}
```

- [ ] **Step 6: Stop forcing focus to the button in `SetAWSDefaults` / `SetOpenRouterKey`**

In `SetAWSDefaults`, delete these two lines (currently lines 89-90):

```go
	// Default focus to the Create button so the user can just press Enter.
	m.focusIndex = m.buttonFocusIndex()
```

In `SetOpenRouterKey`, delete the identical two lines (currently lines 113-114):

```go
	// Default focus to the Create button so the user can just press Enter.
	m.focusIndex = m.buttonFocusIndex()
```

This leaves `focusIndex` at its zero value (0 = the prompt input), keeping the cursor in the prompt box.

- [ ] **Step 7: Rework `Update()` — navigation, remove secrets toggle, fix `p`, fix delegation**

Replace the navigation focus-state loop (lines 189-196):

```go
			// Update input focus states
			for i := range m.inputs {
				if i == m.focusIndex {
					m.inputs[i].Focus()
				} else {
					m.inputs[i].Blur()
				}
			}
			return m, nil
```

with one that accounts for the prompt input at index 0:

```go
			// Update focus states (focus index 0 = prompt input; 1.. = m.inputs).
			if m.focusIndex == 0 {
				m.promptInput.Focus()
			} else {
				m.promptInput.Blur()
			}
			for i := range m.inputs {
				if m.inputFocusIndex(i) == m.focusIndex {
					m.inputs[i].Focus()
				} else {
					m.inputs[i].Blur()
				}
			}
			return m, nil
```

Replace the `p` case (lines 198-202):

```go
		case "p":
			if !m.awsEnabled || m.focusIndex == m.buttonFocusIndex() {
				m.promptFree = !m.promptFree
			}
			return m, nil
```

with a version that only toggles when the Create button is focused (so `p` types literally inside the prompt box):

```go
		case "p":
			if m.focusIndex == m.buttonFocusIndex() {
				m.promptFree = !m.promptFree
			} else {
				break // let the key fall through to the focused text input
			}
			return m, nil
```

Delete the entire `space`/`" "` secrets-toggle case (lines 203-215):

```go
		case "space", " ":
			// Toggle secret selection when focused on a secret row.
			if m.focusIndex >= m.secretFocusStart() && m.focusIndex < m.buttonFocusIndex() {
				idx := m.focusIndex - m.secretFocusStart()
				if idx < len(m.allSecrets) {
					key := m.allSecrets[idx].Key
					if m.selectedSecrets == nil {
						m.selectedSecrets = make(map[string]bool)
					}
					m.selectedSecrets[key] = !m.selectedSecrets[key]
				}
			}
			return m, nil
```

Replace the delegation block at the end of `Update` (lines 226-232):

```go
	// Delegate key events to the focused text input
	if (m.awsEnabled || m.openRouterEnabled) && m.focusIndex < len(m.inputs) {
		var cmd tea.Cmd
		m.inputs[m.focusIndex], cmd = m.inputs[m.focusIndex].Update(msg)
		return m, cmd
	}

	return m, nil
```

with delegation that routes index 0 to the prompt input and 1..len to `m.inputs`:

```go
	// Delegate key events to the focused text input.
	if m.focusIndex == 0 {
		var cmd tea.Cmd
		m.promptInput, cmd = m.promptInput.Update(msg)
		return m, cmd
	}
	if (m.awsEnabled || m.openRouterEnabled) && m.focusIndex >= 1 && m.focusIndex <= len(m.inputs) {
		idx := m.focusIndex - 1
		var cmd tea.Cmd
		m.inputs[idx], cmd = m.inputs[idx].Update(msg)
		return m, cmd
	}

	return m, nil
```

> Note on the `p` change: the `break` inside the `switch` exits the `switch` so the key falls through to the delegation block above and is typed into the focused input. Because the early `switch` runs only for `tea.KeyMsg`, and the delegation block is reached afterward, a `p` keystroke while the prompt input is focused is inserted as text.

- [ ] **Step 8: Render the prompt input and remove the secrets block in `View()`**

In `View()`, add the prompt-input section. Insert it after the Agent / Prompt-Free section and before the AWS block (i.e. right before the `// AWS credential overrides` comment at line 294):

```go
	// Initial prompt
	b.WriteString("\n" + activeStyle.Render("Initial Prompt") + "\n")
	promptLabel := "  Prompt: "
	if m.focusIndex == 0 {
		promptLabel = activeStyle.Render("> Prompt: ")
	}
	b.WriteString(fmt.Sprintf("%-15s %s\n", promptLabel, m.promptInput.View()))
```

Update the AWS focus markers (lines 299-304) to offset by the prompt input:

```go
		if m.focusIndex == m.awsProfileIdx() {
			profileLabel = activeStyle.Render("> Profile:")
		}
		if m.focusIndex == m.awsRegionIdx() {
			regionLabel = activeStyle.Render("> Region: ")
		}
```

becomes:

```go
		if m.focusIndex == m.inputFocusIndex(m.awsProfileIdx()) {
			profileLabel = activeStyle.Render("> Profile:")
		}
		if m.focusIndex == m.inputFocusIndex(m.awsRegionIdx()) {
			regionLabel = activeStyle.Render("> Region: ")
		}
```

Update the OpenRouter focus marker (line 313):

```go
		if m.focusIndex == m.openRouterIdx() {
```

becomes:

```go
		if m.focusIndex == m.inputFocusIndex(m.openRouterIdx()) {
```

Delete the entire "Secrets multi-select" block (lines 319-338):

```go
	// Secrets multi-select
	if len(m.allSecrets) > 0 {
		b.WriteString("\n" + activeStyle.Render("Inject Secrets") + "\n")
		for i, s := range m.allSecrets {
			focusIdx := m.secretFocusStart() + i
			checked := "[ ]"
			if m.selectedSecrets[s.Key] {
				checked = "[✓]"
			}
			label := s.Key
			if s.Description != "" {
				label += " — " + s.Description
			}
			line := fmt.Sprintf("  %s %s", checked, label)
			if m.focusIndex == focusIdx {
				line = activeStyle.Render(fmt.Sprintf("  %s %s", checked, label))
			}
			b.WriteString(line + "\n")
		}
	}
```

- [ ] **Step 9: Update the hint line in `View()`**

Replace the hint construction (lines 354-365):

```go
	hint := "(enter to create, esc to go back"
	if !m.adHoc {
		hint += ", p to toggle prompt-free"
	}
	if m.awsEnabled || m.openRouterEnabled {
		hint += ", tab to cycle fields"
	}
	if len(m.allSecrets) > 0 {
		hint += ", space to toggle secret"
	}
	hint += ")"
	b.WriteString("\n" + hint + "\n")
```

with (tab cycling is now always relevant because of the prompt field; secrets hint removed):

```go
	hint := "(enter to create, esc to go back, tab to cycle fields"
	if !m.adHoc {
		hint += ", p on Create to toggle prompt-free"
	}
	hint += ")"
	b.WriteString("\n" + hint + "\n")
```

- [ ] **Step 10: Capture the prompt and drop the secrets loop in `GetSessionConfig()`**

In `GetSessionConfig`, delete the secrets loop (lines 416-421):

```go
	// Selected secrets
	for _, s := range m.allSecrets {
		if m.selectedSecrets[s.Key] {
			cfg.EnvSecrets = append(cfg.EnvSecrets, s)
		}
	}

	return cfg
```

and replace it with the prompt capture:

```go
	// Initial prompt text entered by the user.
	cfg.InitialPrompt = strings.TrimSpace(m.promptInput.Value())

	return cfg
```

- [ ] **Step 11: Run the SummaryModel tests**

Run: `go test ./internal/ui/ -run TestSummaryModel`
Expected: PASS (all five tests).

- [ ] **Step 12: Build the whole UI package to catch unused references**

Run: `go build ./internal/ui/`
Expected: no output. (If the compiler reports `allSecrets`/`selectedSecrets` still referenced, a removal was missed — grep with `grep -n "allSecrets\|selectedSecrets\|secretFocusStart\|SetSecrets" internal/ui/summary.go` and remove the leftover.)

- [ ] **Step 13: Commit**

```bash
git add internal/ui/summary.go internal/ui/summary_test.go
git commit -m "feat: replace secret selection with initial-prompt text box in summary"
```

---

## Task 4: Dashboard wiring

**Files:**
- Modify: `internal/ui/dashboard.go:4490-4493` (remove SetSecrets call)
- Modify: `internal/ui/dashboard.go:4509-4519` (copy InitialPrompt)

- [ ] **Step 1: Remove the `SetSecrets` wiring**

In `dashboard.go`, delete this block (lines 4490-4493):

```go
		// Load globally stored secrets so the user can select which to inject.
		if secrets, err := m.db.ListSecrets(context.Background()); err == nil {
			m.summary.SetSecrets(secrets)
		}
```

- [ ] **Step 2: Copy the typed prompt into the session config**

In `handleSummaryUpdate`, find the merge block (lines 4512-4515):

```go
		m.sessionCfg.Agent = summaryCfg.Agent
		m.sessionCfg.PromptFree = summaryCfg.PromptFree
		m.sessionCfg.AWSConfig = summaryCfg.AWSConfig
		m.sessionCfg.OpenRouterAPIKey = summaryCfg.OpenRouterAPIKey
```

Add the InitialPrompt copy:

```go
		m.sessionCfg.Agent = summaryCfg.Agent
		m.sessionCfg.PromptFree = summaryCfg.PromptFree
		m.sessionCfg.AWSConfig = summaryCfg.AWSConfig
		m.sessionCfg.OpenRouterAPIKey = summaryCfg.OpenRouterAPIKey
		m.sessionCfg.InitialPrompt = summaryCfg.InitialPrompt
```

- [ ] **Step 3: Build the UI package**

Run: `go build ./internal/ui/`
Expected: no output. (If `context` is now an unused import in `dashboard.go`, the build will say so — verify with `grep -n "context\." internal/ui/dashboard.go`; it is used elsewhere, so no change is expected. Remove the import only if the compiler flags it.)

- [ ] **Step 4: Commit**

```bash
git add internal/ui/dashboard.go
git commit -m "feat: wire initial-prompt through dashboard, drop secret selection wiring"
```

---

## Task 5: Full verification

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: no output.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: PASS (or pre-existing unrelated failures only — note any).

- [ ] **Step 3: Confirm no stale secret-injection references remain in the summary path**

Run: `grep -rn "SetSecrets\|selectedSecrets\|allSecrets\|secretFocusStart" internal/ui/`
Expected: no matches.

- [ ] **Step 4: Vet**

Run: `go vet ./internal/ui/ ./internal/usecase/ ./internal/domain/`
Expected: no output.

---

## Self-Review Notes

- **Spec coverage:** secrets selection removed (Task 3 step 5/8/10, Task 4 step 1); single-line prompt box added & focused by default (Task 3 steps 3-8); Enter starts session (existing behavior preserved + asserted in Task 3 step 1); text delivered as part of initial prompt for both JIRA and ad-hoc (Task 1 + Task 2 via `joinPrompt`).
- **`EnvSecrets`:** field retained on `SessionConfig`; flow_manager injection loop left untouched (iterates empty slice) per spec — explicitly out of scope to remove.
- **Type consistency:** `joinPrompt`, `newPromptInput`, `inputFocusIndex`, `promptInput`, `SessionConfig.InitialPrompt`, `cfg.InitialPrompt` used consistently across tasks.
- **Global secrets management UI** (`secrets_setup.go`) untouched — out of scope.
