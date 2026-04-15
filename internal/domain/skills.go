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
	SkillTypePrompt SkillType = "prompt" // Markdown/Text instructions
	SkillTypeAlias  SkillType = "alias"  // Shell aliases or scripts
	SkillTypeTool   SkillType = "tool"   // JSON tool definitions
	SkillTypeEnv    SkillType = "env"    // Environment variables
)

// PreparedSession holds the agent launch command and an optional initial prompt
// that should be sent to the agent via tmux send-keys after it has started.
type PreparedSession struct {
	// Command is the shell command to start the agent (e.g. "claude --dangerously-skip-permissions").
	Command string
	// InitialPrompt, if non-empty, should be typed into the tmux pane after the
	// agent is running. Only agents that are confirmed to accept an inline initial
	// message (currently Claude Code) embed the prompt directly in Command instead.
	InitialPrompt string
}

// SkillEngine manages the lifecycle of skills and their injection into agents.
type SkillEngine interface {
	// Sync synchronizes the skills from the remote repository.
	Sync(ctx context.Context) error

	// ListSkills returns all available skills.
	ListSkills() ([]Skill, error)

	// PrepareSession prepares the remote environment with the selected skills.
	// It returns a PreparedSession containing the agent command and an optional
	// initial prompt. If issue is non-nil, a task file (.aiman_task.md) is
	// written to the worktree and the agent receives an initial prompt.
	PrepareSession(ctx context.Context, remote RemoteExecutor, worktreePath string, agent Agent, selectedSkills []Skill, promptFree bool, issue *Issue) (PreparedSession, error)
}
