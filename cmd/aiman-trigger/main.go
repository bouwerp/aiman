package main

import (
	"context"
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
	"github.com/bouwerp/aiman/internal/usecase"
)

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
		URL:      cfg.Integrations.Jira.URL,
		Email:    cfg.Integrations.Jira.Email,
		APIToken: cfg.Integrations.Jira.APIToken,
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

	daemon := usecase.NewDaemon(cfg, db, flowManager)

	log.Println("Starting autonomous agent trigger daemon...")
	if err := daemon.Run(context.Background()); err != nil {
		return fmt.Errorf("daemon exited with error: %w", err)
	}

	return nil
}
