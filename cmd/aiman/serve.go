package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

func runServe() error {
	if err := config.EnsureDir(); err != nil {
		return err
	}
	logPath, err := config.GetServeLogPath()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening serve log: %w", err)
	}
	defer f.Close()
	log.SetOutput(io.MultiWriter(os.Stderr, f))

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{}
	}

	dbPath, err := config.GetDBPath()
	if err != nil {
		return err
	}
	db, err := sqlite.NewRepository(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	root := ""
	if len(cfg.Remotes) > 0 {
		root = cfg.Remotes[0].Root
	}
	if root == "" {
		home, _ := os.UserHomeDir()
		root = home
	}
	localExec := local.NewExecutor(root)
	jiraProvider := jira.NewProvider(jira.Config{
		URL:           cfg.Integrations.Jira.URL,
		Email:         cfg.Integrations.Jira.Email,
		APIToken:      cfg.Integrations.Jira.APIToken,
		IssueStatuses: cfg.Integrations.Jira.IssueStatuses,
	})
	gitManager := git.NewManager(&cfg.Git)
	flow := usecase.NewFlowManager(jiraProvider, &cfg.Integrations.Jira, gitManager, localExec, domain.NewGitSlugger(), skills.NewEngine(cfg))

	dir, err := config.GetDir()
	if err != nil {
		return err
	}
	ln, err := server.Listen(dir)
	if err != nil {
		return err
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := server.New(ln, db, localExec, flow, version)
	log.Printf("aiman serve listening on %s", filepath.Join(dir, "aiman.sock"))
	return srv.Serve(ctx)
}
