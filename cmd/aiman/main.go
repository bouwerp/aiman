package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/ai"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/ui"
	"github.com/bouwerp/aiman/internal/usecase"
	tea "github.com/charmbracelet/bubbletea"
)

// Set via -ldflags at build time.
var version = "dev"
var buildTime = ""

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Ensure config directory exists
	if err := config.EnsureDir(); err != nil {
		return fmt.Errorf("failed to ensure config directory: %w", err)
	}

	// 2. Load configuration
	cfg, err := config.Load()
	if err != nil {
		// We could still proceed if we want to show an error in the TUI
		// But for now, let's just fail fast.
		// Actually, let's provide a default config if it's missing just for the demo
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

	// 4. Initialize Infrastructure
	jiraProvider := jira.NewProvider(jira.Config{
		URL:      cfg.Integrations.Jira.URL,
		Email:    cfg.Integrations.Jira.Email,
		APIToken: cfg.Integrations.Jira.APIToken,
	})
	gitManager := git.NewManager(&cfg.Git)
	doctor := usecase.NewDoctor(cfg, jiraProvider, gitManager)

	// 5. Initialize Flow Manager and Skill Engine
	// Use the first configured remote as a default for FlowManager's SSH manager.
	// Per-session overrides (SessionConfig.SSHManager) take precedence at creation time.
	var defaultRemote config.Remote
	if len(cfg.Remotes) > 0 {
		defaultRemote = cfg.Remotes[0]
	}
	sshManager := ssh.NewManager(ssh.Config{
		Host: defaultRemote.Host,
		User: defaultRemote.User,
		Root: defaultRemote.Root,
	})
	skillEngine := skills.NewEngine(cfg)
	slugger := domain.NewGitSlugger()
	flowManager := usecase.NewFlowManager(jiraProvider, &cfg.Integrations.Jira, gitManager, sshManager, slugger, skillEngine)

	// 6. Handle commands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			if buildTime != "" {
				fmt.Printf("aiman %s (built %s)\n", version, buildTime)
			} else {
				fmt.Printf("aiman %s\n", version)
			}
			return nil
		case "update":
			return runUpdate(version)
		case "init":
			p := tea.NewProgram(ui.NewSetupModel(cfg), tea.WithAltScreen(), tea.WithMouseAllMotion())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("alas, there's been an error: %w", err)
			}
			return nil
		case "repos":
			repos, err := gitManager.ListRepos(context.Background())
			if err != nil {
				return fmt.Errorf("failed to list repos: %w", err)
			}
			p := tea.NewProgram(ui.NewRepoPickerModel(repos, &cfg.Git), tea.WithAltScreen(), tea.WithMouseAllMotion())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("alas, there's been an error: %w", err)
			}
			return nil
		default:
			fmt.Fprintf(os.Stderr, "aiman: unknown command %q\n\n", os.Args[1])
			fmt.Fprintf(os.Stderr, "Usage: aiman [command]\n\n")
			fmt.Fprintf(os.Stderr, "Commands:\n")
			fmt.Fprintf(os.Stderr, "  (none)           start the TUI\n")
			fmt.Fprintf(os.Stderr, "  version, -v      print version information\n")
			fmt.Fprintf(os.Stderr, "  update           update aiman to the latest release\n")
			fmt.Fprintf(os.Stderr, "  init             run the configuration setup wizard\n")
			fmt.Fprintf(os.Stderr, "  repos            open the repository picker\n")
			os.Exit(1)
		}
	}

	// 7. Start TUI with StartupModel (Splash screen)
	intelligence := ai.NewIntelligenceProvider(cfg)
	snapshotManager := usecase.NewSnapshotManager(db, intelligence)
	p := tea.NewProgram(ui.NewStartupModel(cfg, doctor, db, flowManager, intelligence, snapshotManager, version), tea.WithAltScreen(), tea.WithMouseAllMotion())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("alas, there's been an error: %w", err)
	}

	return nil
}
