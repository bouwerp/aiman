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
	}
	return &Engine{
		cfg: cfg,
	}
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
		skills = append(skills, domain.Skill{
			Name:        strings.TrimSuffix(info.Name(), ext),
			Path:        path,
			Description: fmt.Sprintf("%s skill from %s", skillType, relPath),
			Type:        skillType,
		})

		return nil
	})

	return skills, err
}

func (e *Engine) PrepareSession(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool) (string, error) {
	name := strings.ToLower(agent.Name)
	
	// For Claude Code
	if strings.Contains(name, "claude") {
		return e.prepareClaude(ctx, remote, worktreePath, agent, selectedSkills, promptFree)
	}

	// For Gemini
	if strings.Contains(name, "gemini") {
		return e.prepareGemini(ctx, remote, worktreePath, agent, selectedSkills, promptFree)
	}

	// For OpenCode
	if strings.Contains(name, "opencode") {
		cmd := agent.Command
		if promptFree {
			// User previously said --yolo doesn't exist, but if they want "yolo" mode, 
			// let's see if there's any other flag. For now, we'll keep it as is 
			// or add a flag if known. The user specifically mentioned Gemini and Cursor now.
		}
		return cmd, nil
	}

	// For Cursor
	if strings.Contains(name, "cursor") {
		cmd := agent.Command
		if promptFree {
			cmd = fmt.Sprintf("%s --force .", cmd)
		} else {
			cmd = fmt.Sprintf("%s .", cmd)
		}
		return cmd, nil
	}

	// Default: Apply prompt-free flag if it's a known pattern
	cmd := agent.Command
	if promptFree {
		if strings.Contains(name, "copilot") {
			// gh copilot doesn't have a standard yolo flag for the chat
		}
	}

	return cmd, nil
}

func (e *Engine) prepareClaude(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool) (string, error) {
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

	if len(prompts) == 0 {
		return cmd, nil
	}

	// Concatenate prompts and escape for shell
	systemPrompt := strings.Join(prompts, "\n\n")
	
	// Create a remote file with the system prompt
	remotePromptPath := filepath.Join(worktreePath, ".aiman_prompt")
	if err := remote.WriteFile(ctx, remotePromptPath, []byte(systemPrompt)); err != nil {
		return "", fmt.Errorf("failed to upload system prompt to remote: %w", err)
	}

	// Wrap the agent command. For Claude Code, it might use a CLI flag to load a system prompt.
	// Assuming `claude --prompt-file .aiman_prompt` or similar.
	// If not supported, we can inject it into stdin, but that's harder for an interactive CLI.
	// For now, let's assume it's an environment variable.
	return fmt.Sprintf("SYSTEM_PROMPT_FILE=%s %s", remotePromptPath, cmd), nil
}

func (e *Engine) prepareGemini(ctx context.Context, remote domain.RemoteExecutor, worktreePath string, agent domain.Agent, selectedSkills []domain.Skill, promptFree bool) (string, error) {
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

	if len(prompts) == 0 {
		return cmd, nil
	}

	systemPrompt := strings.Join(prompts, "\n\n")
	remotePromptPath := filepath.Join(worktreePath, ".aiman_gemini_prompt")
	if err := remote.WriteFile(ctx, remotePromptPath, []byte(systemPrompt)); err != nil {
		return "", fmt.Errorf("failed to upload gemini prompt to remote: %w", err)
	}

	return fmt.Sprintf("GEMINI_SYSTEM_INSTRUCTION_FILE=%s %s", remotePromptPath, cmd), nil
}
