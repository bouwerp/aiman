package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// socketSender steers sessions through serve's steer.send. The trigger runs on
// the remote, so it talks to the socket directly rather than through aiman rpc.
type socketSender struct {
	path string
}

func (s socketSender) SendPrompt(_ context.Context, sessionID, text string, mode usecase.SendMode) (bool, error) {
	resp, err := server.Call(s.path, "steer.send", map[string]any{
		"session": sessionID,
		"text":    text,
		"as":      "aiman",
		"mode":    string(mode),
	})
	if err != nil {
		return false, err
	}
	if resp.Error != nil {
		return false, fmt.Errorf("steer.send: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if mode != usecase.SendAnswer {
		return true, nil
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return false, err
	}
	var result struct {
		Answered bool `json:"answered"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, fmt.Errorf("steer.send result: %w", err)
	}
	return result.Answered, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	log.Println("Initializing aiman-trigger daemon...")

	// 1. Ensure config directory exists
	if err := config.EnsureDir(); err != nil {
		return fmt.Errorf("failed to ensure config directory: %w", err)
	}

	// 2. Load configuration
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}

	// 3. Initialize Database
	dbPath, err := config.GetDBPath()
	if err != nil {
		return fmt.Errorf("failed to get database path: %w", err)
	}
	db, err := sqlite.NewRepository(dbPath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	// 4. Initialize FlowManager dependencies
	jiraProvider := jira.NewProvider(jira.Config{
		URL:                     cfg.Integrations.Jira.URL,
		Email:                   cfg.Integrations.Jira.Email,
		APIToken:                cfg.Integrations.Jira.APIToken,
		IssueStatuses:           cfg.Integrations.Jira.IssueStatuses,
		AcceptanceCriteriaField: cfg.Integrations.Jira.AcceptanceCriteriaField,
	})
	gitManager := git.NewManager(&cfg.Git)
	localManager := local.NewExecutor("") // Default empty, overridden per-session
	slugger := domain.NewGitSlugger()
	skillEngine := skills.NewEngine(cfg)

	flowManager := usecase.NewFlowManager(
		jiraProvider,
		&cfg.Integrations.Jira,
		gitManager,
		localManager,
		slugger,
		skillEngine,
	)
	flowManager.SetRepoBootstrap(cfg.Repos)
	flowManager.SetRoles(cfg.Roles)
	flowManager.SetUpdateGuards(cfg.AgentUpdateGuards())

	dir, err := config.GetDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}
	daemon := usecase.NewDaemon(cfg, db, flowManager, socketSender{path: server.SocketPath(dir)})

	log.Println("Starting autonomous agent trigger daemon...")
	if err := daemon.Run(context.Background()); err != nil {
		return fmt.Errorf("daemon exited with error: %w", err)
	}

	return nil
}
