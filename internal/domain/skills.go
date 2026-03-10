package domain

import "context"

// Skill represents a capability or instruction set for an agent.
type Skill struct {
	Name        string
	Path        string
	Description string
	Type        SkillType
}

type SkillType string

const (
	SkillTypePrompt  SkillType = "prompt"  // Markdown/Text instructions
	SkillTypeAlias   SkillType = "alias"   // Shell aliases or scripts
	SkillTypeTool    SkillType = "tool"    // JSON tool definitions
	SkillTypeEnv     SkillType = "env"     // Environment variables
)

// SkillEngine manages the lifecycle of skills and their injection into agents.
type SkillEngine interface {
	// Sync synchronizes the skills from the remote repository.
	Sync(ctx context.Context) error
	
	// ListSkills returns all available skills.
	ListSkills() ([]Skill, error)
	
	// PrepareSession prepares the remote environment with the selected skills.
	// It returns the command to launch the agent with the skills injected.
	PrepareSession(ctx context.Context, remote RemoteExecutor, worktreePath string, agent Agent, selectedSkills []Skill) (string, error)
}
