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
// The detailed context lives in .aiman_task.md which is written to the worktree.
const initialPrompt = `Read .aiman_task.md for the JIRA issue details. That file is session-only scaffolding: do not commit it or add it to the repo. Gather necessary codebase context and prepare an implementation plan.`

func (e *Engine) PrepareSession(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool, issue *domain.Issue) (domain.PreparedSession, error) {
	name := strings.ToLower(agent.Name)

	// Write JIRA task file if an issue is provided.
	if issue != nil {
		if err := writeTaskFile(ctx, remote, worktreePath, issue); err != nil {
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

	// Default
	cmd := agent.Command
	result := domain.PreparedSession{Command: cmd}
	if issue != nil {
		result.InitialPrompt = initialPrompt
	}
	return result, nil
}

// writeTaskFile writes the JIRA issue context to .aiman_task.md in the worktree.
// This file is referenced by the agent's initial prompt so it can load the full
// issue details without shell-escaping concerns.
func writeTaskFile(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, issue *domain.Issue) error {
	var sb strings.Builder
	sb.WriteString("<!--\n")
	sb.WriteString("DO NOT COMMIT — This file is local/session scaffolding from Aiman, not part of the product.\n")
	sb.WriteString("Aiman adds .aiman_task.md to this worktree's .gitignore when the session is created; do not commit it if it still appears as tracked.\n")
	sb.WriteString("-->\n\n")
	sb.WriteString("> **Do not commit this file to version control.** It is generated only for this Aiman agent session. ")
	sb.WriteString("Exclude it from commits (see `.gitignore` in the worktree root).\n\n")
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf("# %s: %s\n\n", issue.Key, issue.Summary))
	sb.WriteString(fmt.Sprintf("**Status:** %s\n", issue.Status))
	if issue.Assignee != "" {
		sb.WriteString(fmt.Sprintf("**Assignee:** %s\n", issue.Assignee))
	}
	sb.WriteString("\n## Description\n\n")
	if issue.Description != "" {
		sb.WriteString(issue.Description)
	} else {
		sb.WriteString("_No description provided._")
	}
	sb.WriteString("\n")

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
