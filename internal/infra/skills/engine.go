package skills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type Engine struct {
	cfg *config.Config
}

func NewEngine(cfg *config.Config) *Engine {
	// Default skills path if not set
	if cfg.Skills.Path == "" {
		home, _ := os.UserHomeDir()
		cfg.Skills.Path = filepath.Join(home, config.DirName, "skills")
	} else {
		cfg.Skills.Path = expandUserPath(cfg.Skills.Path)
	}
	return &Engine{
		cfg: cfg,
	}
}

// expandUserPath replaces a leading "~" or "~/" with the user's home directory.
func expandUserPath(p string) string {
	if p == "" || p == "~" {
		if p == "~" {
			home, err := os.UserHomeDir()
			if err != nil {
				return p
			}
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return p
}

func (e *Engine) Sync(ctx context.Context) error {
	if e.cfg.Skills.Repo == "" {
		return nil // No skills repo configured
	}

	skillsPath := e.cfg.Skills.Path
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		// Clone the repo
		if err := os.MkdirAll(filepath.Dir(skillsPath), 0755); err != nil {
			return fmt.Errorf("failed to create skills directory: %w", err)
		}
		cmd := exec.CommandContext(ctx, "git", "clone", e.cfg.Skills.Repo, skillsPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to clone skills repo: %w\nOutput: %s", err, string(output))
		}
	} else {
		// Update the repo
		cmd := exec.CommandContext(ctx, "git", "-C", skillsPath, "pull")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to update skills repo: %w\nOutput: %s", err, string(output))
		}
	}

	return nil
}

func (e *Engine) ListSkills() ([]domain.Skill, error) {
	skillsPath := e.cfg.Skills.Path
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		return nil, nil // Skills repo not yet synced
	}

	var skills []domain.Skill

	// Read directories to find skills
	err := filepath.Walk(skillsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		// Detect skill type by extension
		ext := filepath.Ext(path)
		var skillType domain.SkillType
		switch ext {
		case ".md", ".txt":
			skillType = domain.SkillTypePrompt
		case ".sh":
			skillType = domain.SkillTypeAlias
		case ".json":
			skillType = domain.SkillTypeTool
		default:
			return nil // Unknown extension
		}

		relPath, _ := filepath.Rel(skillsPath, path)
		name := strings.TrimSuffix(info.Name(), ext)
		// OpenCode-style layout: skills/<id>/SKILL.md
		if strings.EqualFold(info.Name(), "SKILL.md") || strings.EqualFold(info.Name(), "SKILL.txt") {
			if parent := filepath.Base(filepath.Dir(path)); parent != "" && parent != "." {
				name = parent
			}
		}
		skills = append(skills, domain.Skill{
			Name:        name,
			Path:        path,
			Description: fmt.Sprintf("%s skill from %s", skillType, relPath),
			Type:        skillType,
		})

		return nil
	})

	return skills, err
}

// initialPrompt is the instruction sent to the agent when a JIRA issue is attached.
// The detailed context and working guidelines live in .aiman_task.md which is
// written to the worktree. This trigger is kept short because it is delivered
// via tmux send-keys.
const initialPrompt = `Read .aiman_task.md — it contains the task, acceptance criteria, and your working guidelines for this session. Start by presenting your implementation plan.`

func (e *Engine) PrepareSession(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool, issue *domain.Issue, snapshot *domain.SessionSnapshot) (domain.PreparedSession, error) {
	name := strings.ToLower(agent.Name)

	// Write JIRA task file if an issue is provided.
	if issue != nil {
		if err := writeTaskFile(ctx, remote, worktreePath, issue, snapshot); err != nil {
			return domain.PreparedSession{}, fmt.Errorf("failed to write task file: %w", err)
		}
	}

	// For Claude Code
	if strings.Contains(name, "claude") {
		return e.prepareClaude(ctx, remote, worktreePath, agent, selectedSkills, promptFree, issue)
	}

	// For Gemini
	if strings.Contains(name, "gemini") {
		return e.prepareGemini(ctx, remote, worktreePath, agent, selectedSkills, promptFree, issue)
	}

	// For OpenCode
	if strings.Contains(name, "opencode") {
		cmd := agent.Command
		result := domain.PreparedSession{Command: cmd}
		if issue != nil {
			result.InitialPrompt = initialPrompt
		}
		return result, nil
	}

	// For Cursor
	if strings.Contains(name, "cursor") {
		cmd := agent.Command
		if promptFree {
			cmd = fmt.Sprintf("%s --force .", cmd)
		} else {
			cmd = fmt.Sprintf("%s .", cmd)
		}
		result := domain.PreparedSession{Command: cmd}
		if issue != nil {
			result.InitialPrompt = initialPrompt
		}
		return result, nil
	}

	// For GitHub Copilot CLI
	if strings.Contains(name, "copilot") || strings.Contains(strings.ToLower(agent.Command), "copilot") {
		cmd := agent.Command
		// Always allow all tools/paths/URLs so permission prompts don't block an autonomous session.
		cmd = fmt.Sprintf("%s --allow-all", cmd)
		if promptFree {
			cmd = fmt.Sprintf("%s --autopilot", cmd)
		}
		result := domain.PreparedSession{Command: cmd}
		if issue != nil {
			result.InitialPrompt = initialPrompt
		}
		return result, nil
	}

	// Default
	cmd := agent.Command
	result := domain.PreparedSession{Command: cmd}
	if issue != nil {
		result.InitialPrompt = initialPrompt
	}
	return result, nil
}

// writeTaskFile writes the JIRA issue context and working guidelines to
// .aiman_task.md in the worktree. The agent's initial prompt tells it to
// read this file, so all substantive instructions live here rather than in
// the tmux send-keys string.
func writeTaskFile(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, issue *domain.Issue, snapshot *domain.SessionSnapshot) error {
	var sb strings.Builder

	// --- Housekeeping notice ---
	sb.WriteString("<!--\n")
	sb.WriteString("DO NOT COMMIT — session scaffolding generated by Aiman.\n")
	sb.WriteString("-->\n\n")
	sb.WriteString("> **Do not commit this file.** It is generated for this session only and is listed in `.gitignore`.\n\n")

	// --- Task ---
	sb.WriteString("---\n\n")
	sb.WriteString("# Task\n\n")
	sb.WriteString(fmt.Sprintf("## %s: %s\n\n", issue.Key, issue.Summary))
	sb.WriteString(fmt.Sprintf("**Status:** %s\n", issue.Status))
	if issue.Assignee != "" {
		sb.WriteString(fmt.Sprintf("**Assignee:** %s\n", issue.Assignee))
	}
	sb.WriteString("\n### Description\n\n")
	if issue.Description != "" {
		sb.WriteString(issue.Description)
	} else {
		sb.WriteString("_No description provided._")
	}
	sb.WriteString("\n\n")

	// --- Working guidelines ---
	sb.WriteString("---\n\n")
	sb.WriteString("# Working Guidelines\n\n")
	sb.WriteString("Follow these guidelines for the duration of this session.\n\n")

	// 1. Workflow
	sb.WriteString("## Workflow\n\n")
	sb.WriteString("1. **Understand first.** Read the task description above carefully. Identify what is being asked, what the acceptance criteria are, and what is explicitly out of scope.\n")
	sb.WriteString("2. **Explore the codebase.** Before writing any code, navigate the repository to understand the existing architecture, conventions, and patterns. Identify the modules, layers, and files relevant to the task.\n")
	sb.WriteString("3. **Present a plan.** Outline your implementation approach — what you will change, where, and why. Call out trade-offs or assumptions. Wait for confirmation before proceeding unless the task is trivially small.\n")
	sb.WriteString("4. **Write tests first.** Follow a test-driven approach: write a failing test that captures the expected behaviour, then implement the code to make it pass. For bug fixes, write a test that reproduces the bug before fixing it.\n")
	sb.WriteString("5. **Implement incrementally.** Make small, focused changes. Compile and run tests after each meaningful step to catch regressions early.\n")
	sb.WriteString("6. **Verify before finishing.** Ensure the code compiles, all tests pass (including pre-existing ones), and the change works end-to-end where possible.\n\n")

	// 2. Engineering principles
	sb.WriteString("## Engineering Principles\n\n")
	sb.WriteString("Apply these principles pragmatically — they are tools for making better decisions, not rituals to follow blindly.\n\n")
	sb.WriteString("- **TDD.** Tests drive the design. Write the test, watch it fail, make it pass, refactor. Tests should describe behaviour, not implementation details.\n")
	sb.WriteString("- **SOLID.** Favour small, focused types with clear responsibilities. Depend on interfaces, not concretions. But do not introduce abstractions for things that have only one implementation — extract an interface when a second consumer appears, not before.\n")
	sb.WriteString("- **DDD.** Respect domain boundaries. Keep domain logic in the domain layer; keep infrastructure concerns (IO, frameworks, external APIs) at the edges. Use the language of the domain in code and tests.\n")
	sb.WriteString("- **Simplicity over cleverness.** The best code is the code you don't have to write. Prefer the straightforward solution. Three similar lines are better than a premature abstraction. Avoid speculative generality — build for what the task requires, not for hypothetical future requirements.\n")
	sb.WriteString("- **Minimalism.** Change only what the task requires. Do not refactor surrounding code, add comments to untouched code, or introduce unrelated improvements. A small, correct diff is better than a large, thorough one.\n\n")

	// 3. Guardrails
	sb.WriteString("## Guardrails\n\n")
	sb.WriteString("- **Stay on scope.** Only make changes that directly serve the task. If you notice something unrelated that needs attention, note it but do not fix it in this session.\n")
	sb.WriteString("- **Do not commit generated files.** This file (`.aiman_task.md`), `.aiman_prompt`, and any other files prefixed with `.aiman_` are session scaffolding. Never stage, commit, or include them in pull requests.\n")
	sb.WriteString("- **No secrets or credentials in code.** Do not hardcode tokens, passwords, API keys, or other secrets.\n")
	sb.WriteString("- **Preserve existing tests.** Do not delete, skip, or weaken existing tests to make your changes pass. If an existing test conflicts with the new behaviour, update it to reflect the correct new expectation and explain why.\n")
	sb.WriteString("- **Respect the project structure.** Follow existing file layout, naming conventions, and patterns. When in doubt, look at how similar features are implemented in the codebase and stay consistent.\n\n")

	// 4. Communication
	sb.WriteString("## Communication\n\n")
	sb.WriteString("- Present your implementation plan before writing production code.\n")
	sb.WriteString("- When making a design decision with trade-offs, briefly explain the options and why you chose the one you did.\n")
	sb.WriteString("- If the task description is ambiguous or incomplete, state your interpretation and assumptions before proceeding.\n")
	sb.WriteString("- If you get stuck or discover a blocker, explain what you tried and what went wrong rather than silently switching approaches.\n")

	// --- Prior session context ---
	if snapshot != nil && (snapshot.Summary != "" || len(snapshot.NextSteps) > 0) {
		sb.WriteString("\n\n---\n\n")
		sb.WriteString("# Prior Session Context\n\n")
		sb.WriteString(fmt.Sprintf("_Captured on %s_\n\n", snapshot.CreatedAt.Format("2006-01-02 15:04")))
		if snapshot.Summary != "" {
			sb.WriteString("## What was done\n\n")
			sb.WriteString(snapshot.Summary + "\n\n")
		}
		if len(snapshot.NextSteps) > 0 {
			sb.WriteString("## Next steps\n\n")
			for _, s := range snapshot.NextSteps {
				sb.WriteString("- " + s + "\n")
			}
			sb.WriteString("\n")
		}
		sb.WriteString("> Continue from where the previous session left off. Review the above, then present your updated plan before writing code.\n")
	}

	taskPath := filepath.Join(worktreePath, ".aiman_task.md")
	return remote.WriteFile(ctx, taskPath, []byte(sb.String()))
}

func (e *Engine) prepareClaude(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool, issue *domain.Issue) (domain.PreparedSession, error) {
	var prompts []string
	for _, s := range selectedSkills {
		if s.Type == domain.SkillTypePrompt {
			content, err := os.ReadFile(s.Path)
			if err == nil {
				prompts = append(prompts, string(content))
			}
		}
	}

	cmd := agent.Command
	if promptFree {
		cmd = fmt.Sprintf("%s --dangerously-skip-permissions", cmd)
	}

	// Use tmux send-keys for the initial prompt rather than a positional arg.
	// Although Claude Code supports `claude "msg"` interactively, embedding
	// quoted text inside the tmux "bash -l -c '...'" wrapper causes nested
	// quoting breakage. send-keys avoids this entirely.
	result := domain.PreparedSession{Command: cmd}
	if issue != nil {
		result.InitialPrompt = initialPrompt
	}

	if len(prompts) == 0 {
		return result, nil
	}

	// Concatenate prompts and escape for shell
	systemPrompt := strings.Join(prompts, "\n\n")

	// Create a remote file with the system prompt
	remotePromptPath := filepath.Join(worktreePath, ".aiman_prompt")
	if err := remote.WriteFile(ctx, remotePromptPath, []byte(systemPrompt)); err != nil {
		return domain.PreparedSession{}, fmt.Errorf("failed to upload system prompt to remote: %w", err)
	}

	result.Command = fmt.Sprintf("SYSTEM_PROMPT_FILE=%s %s", remotePromptPath, cmd)
	return result, nil
}

func (e *Engine) prepareGemini(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool, issue *domain.Issue) (domain.PreparedSession, error) {
	var prompts []string
	for _, s := range selectedSkills {
		if s.Type == domain.SkillTypePrompt {
			content, err := os.ReadFile(s.Path)
			if err == nil {
				prompts = append(prompts, string(content))
			}
		}
	}

	cmd := agent.Command
	if promptFree {
		cmd = fmt.Sprintf("%s --yolo", cmd)
	}

	// Gemini's positional arg behavior is not confirmed to stay interactive,
	// so we use tmux send-keys via InitialPrompt instead.
	result := domain.PreparedSession{Command: cmd}
	if issue != nil {
		result.InitialPrompt = initialPrompt
	}

	if len(prompts) == 0 {
		return result, nil
	}

	systemPrompt := strings.Join(prompts, "\n\n")
	remotePromptPath := filepath.Join(worktreePath, ".aiman_gemini_prompt")
	if err := remote.WriteFile(ctx, remotePromptPath, []byte(systemPrompt)); err != nil {
		return domain.PreparedSession{}, fmt.Errorf("failed to upload gemini prompt to remote: %w", err)
	}

	result.Command = fmt.Sprintf("GEMINI_SYSTEM_INSTRUCTION_FILE=%s %s", remotePromptPath, cmd)
	return result, nil
}
