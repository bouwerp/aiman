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

	"github.com/bouwerp/aiman/internal/debuglog"
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
	if serveWantsHelp(os.Args[2:]) {
		printServeUsage(os.Stdout)
		return nil
	}
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
	writers := []io.Writer{os.Stderr, f}
	if w := debuglog.Writer(); w != nil {
		writers = append(writers, w)
	}
	log.SetOutput(io.MultiWriter(writers...))

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

func serveWantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func printServeUsage(w io.Writer) {
	fmt.Fprint(w, `aiman serve — agent API on THIS host

In-pane agents talk to this process over ~/.aiman/aiman.sock
(aiman session … and the skill). One instance per host.

Do not run this on your laptop to enable remotes. Start it from the TUI:

  Tab  →  select "agent API" on the remote  →  i  install/enable
  s restart   c reload   u update   ctrl+k stop   r probe

Foreground on the remote (debugging):
  aiman serve

systemd --user (installed by the TUI):
  systemctl --user status aiman-serve
`)
}
