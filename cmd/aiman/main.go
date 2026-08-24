package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	"github.com/bouwerp/aiman/internal/debuglog"
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

// errUsage signals that usage has already been written to stderr and the process should
// exit non-zero without an additional "Error:" line.
var errUsage = errors.New("usage")

// configPathForNotice resolves the config path for user-facing messages,
// falling back to the bare filename when the home directory is unavailable.
func configPathForNotice() string {
	if p, err := config.GetConfigPath(); err == nil {
		return p
	}
	return config.ConfigName
}

func main() {
	if err := run(); err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}

func run() error {
	origArgs := append([]string(nil), os.Args...)
	parsed, err := parseGlobalFlags(os.Args[1:])
	if err != nil {
		return err
	}
	os.Args = append([]string{os.Args[0]}, parsed.Rest...)

	// 1. Ensure config directory exists
	if err := config.EnsureDir(); err != nil {
		return fmt.Errorf("failed to ensure config directory: %w", err)
	}

	if parsed.Debug {
		if err := startDebugLog(parsed.DebugPath, origArgs); err != nil {
			return err
		}
		defer debuglog.Close()
	}

	// 2. Load configuration
	cfg, err := config.Load()
	if err != nil {
		// We could still proceed if we want to show an error in the TUI
		// But for now, let's just fail fast.
		// Actually, let's provide a default config if it's missing just for the demo
		cfg = &config.Config{}
	}

	// Report a repaired config file before the TUI takes over the terminal: the
	// token in it was readable by other users until now, which the user should
	// know about rather than have quietly fixed.
	if cfg.PermissionsTightened {
		fmt.Fprintf(os.Stderr, "aiman: %s was group/world-readable and has been tightened to 0600.\n", configPathForNotice())
		fmt.Fprintf(os.Stderr, "       It holds your API token in plaintext; rotate it if this machine is shared.\n")
	}
	if cfg.PermissionsError != nil {
		fmt.Fprintf(os.Stderr, "aiman: warning: %v\n", cfg.PermissionsError)
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
		URL:           cfg.Integrations.Jira.URL,
		Email:         cfg.Integrations.Jira.Email,
		APIToken:      cfg.Integrations.Jira.APIToken,
		IssueStatuses: cfg.Integrations.Jira.IssueStatuses,
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
		case "downgrade":
			return runDowngrade(version, os.Args[2:])
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

		case "ec2-loop":
			return runEC2Loop(cfg, os.Args[2:])
		case "clear-aws-profiles":
			return runClearAWSProfiles(db, os.Args[2:])
		case "serve":
			return runServe()
		case "session":
			return runSession(os.Args[2:])
		case "context":
			return runContext(os.Args[2:])
		case "--skill":
			return runSkill()
		default:
			fmt.Fprintf(os.Stderr, "aiman: unknown command %q\n\n", os.Args[1])
			fmt.Fprintf(os.Stderr, "Usage: aiman [options] [command]\n\n")
			fmt.Fprintf(os.Stderr, "Options:\n")
			fmt.Fprintf(os.Stderr, "  --debug[=PATH]   write debug logs to PATH (default ~/.aiman/debug.log)\n\n")
			fmt.Fprintf(os.Stderr, "Commands:\n")
			fmt.Fprintf(os.Stderr, "  (none)           start the TUI\n")
			fmt.Fprintf(os.Stderr, "  version, -v      print version information\n")
			fmt.Fprintf(os.Stderr, "  update           update aiman to the latest release\n")
			fmt.Fprintf(os.Stderr, "  downgrade [tag]  install the previous (or given) release\n")
			fmt.Fprintf(os.Stderr, "  init             run the configuration setup wizard\n")
			fmt.Fprintf(os.Stderr, "  repos            open the repository picker\n")
			fmt.Fprintf(os.Stderr, "  ec2-loop         launch autonomous loop agent on an on-demand EC2 instance\n")
			fmt.Fprintf(os.Stderr, "  clear-aws-profiles  clear legacy aiman-* AWS profile names from stored sessions\n")
			fmt.Fprintf(os.Stderr, "  serve            agent API on this host (install remotes from TUI: m → Agent API → i)\n")
			fmt.Fprintf(os.Stderr, "  session          list/get/create/prompt sessions (JSON; needs serve)\n")
			fmt.Fprintf(os.Stderr, "  context          ls/find/get/put/pack shared notes (JSON; files if serve is down)\n")
			fmt.Fprintf(os.Stderr, "  --skill          print the agent skill\n")
			return errUsage
		}
	}

	if blockBareTUI(os.Getenv("AIMAN_ENV"), stdinIsTTY()) {
		fmt.Fprintf(os.Stderr, "aiman: refusing to start the TUI (AIMAN_ENV=1 or no TTY). Try: aiman session --help\n")
		return errUsage
	}

	// 7. Start TUI with StartupModel (Splash screen)
	intelligence := ai.NewIntelligenceProvider(cfg)
	snapshotManager := usecase.NewSnapshotManager(db, intelligence)
	startup := ui.NewStartupModel(cfg, doctor, db, flowManager, intelligence, snapshotManager, version)
	p := tea.NewProgram(startup, tea.WithAltScreen(), tea.WithMouseAllMotion())

	// From here the TUI owns the terminal, so anything written to stderr lands
	// in the middle of the rendered frame. Background work logs to a file until
	// the program exits.
	if closeLog := redirectLogToFile(); closeLog != nil {
		defer closeLog()
	}

	// Inject the program reference into the model via message once the event loop starts.
	go func() { p.Send(ui.SetProgramMsg{Program: p}) }()
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("alas, there's been an error: %w", err)
	}

	return nil
}

// redirectLogToFile points the standard logger at ~/.aiman/aiman.log for the
// lifetime of the TUI. Returns a closer, or nil when the file could not be
// opened — in which case logging is discarded rather than allowed to corrupt
// the display.
func redirectLogToFile() func() {
	path, err := config.GetLogPath()
	if err != nil {
		log.SetOutput(io.Discard)
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.SetOutput(io.Discard)
		return nil
	}
	if w := debuglog.Writer(); w != nil {
		log.SetOutput(io.MultiWriter(f, w))
	} else {
		log.SetOutput(f)
	}
	return func() {
		log.SetOutput(os.Stderr)
		_ = f.Close()
	}
}

func startDebugLog(path string, origArgs []string) error {
	if path == "" {
		var err error
		path, err = config.GetDebugLogPath()
		if err != nil {
			return fmt.Errorf("debug log path: %w", err)
		}
	}
	if err := debuglog.Enable(path); err != nil {
		return fmt.Errorf("debug log: %w", err)
	}
	if w := debuglog.Writer(); w != nil {
		log.SetOutput(io.MultiWriter(log.Writer(), w))
	}
	log.Printf("aiman debug version=%s goos=%s goarch=%s args=%q", version, runtime.GOOS, runtime.GOARCH, origArgs)
	fmt.Fprintf(os.Stderr, "aiman: debug log %s\n", path)
	return nil
}
