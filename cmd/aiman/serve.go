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
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/aimanskill"
	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/debuglog"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/infra/slack"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/bouwerp/aiman/internal/verify"
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

	home, _ := os.UserHomeDir()
	root := config.ServeGitRoot(cfg, home, config.DirExists)
	localExec := local.NewExecutor(root)
	jiraProvider := jira.NewProvider(jira.Config{
		URL:                     cfg.Integrations.Jira.URL,
		Email:                   cfg.Integrations.Jira.Email,
		APIToken:                cfg.Integrations.Jira.APIToken,
		IssueStatuses:           cfg.Integrations.Jira.IssueStatuses,
		AcceptanceCriteriaField: cfg.Integrations.Jira.AcceptanceCriteriaField,
	})
	gitManager := git.NewManager(&cfg.Git)
	flow := usecase.NewFlowManager(jiraProvider, &cfg.Integrations.Jira, gitManager, localExec, domain.NewGitSlugger(), skills.NewEngine(cfg))
	flow.SetRepoBootstrap(cfg.Repos)
	flow.SetRoles(cfg.Roles)

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
	ensureMosh(ctx)
	ensureAimanSkill(ctx, db)
	ensureAgentHooks(cfg.Verify.BeforePR)
	store := contextstore.NewFiles(contextstore.Root(dir))
	if ix, err := contextstore.OpenIndex(db.DB()); err != nil {
		log.Printf("context index unavailable: %v", err)
	} else {
		reindexStart := time.Now()
		updated, removed, err := store.Reindex(ctx)
		if err != nil {
			log.Printf("context index reindex: %v", err)
		} else {
			log.Printf("context index: reindexed %d, removed %d in %s", updated, removed, time.Since(reindexStart).Round(time.Millisecond))
		}
		store.AttachIndex(ix)
	}
	ptyMgr := ptyruntime.NewManager()
	defer ptyMgr.CloseAll()
	srv := server.New(ln, db, localExec, flow, store, ptyMgr, version)
	flow.SetDoneItems(srv.DoneItems())
	if jobs, err := memoryjob.Open(db.DB()); err != nil {
		log.Printf("memory jobs unavailable: %v", err)
	} else {
		srv.SetMemoryJobs(jobs)
	}
	todos := todo.NewFiles(filepath.Join(dir, "todos"))
	srv.EnableTodos(todos, db)
	tracker := usage.NewTracker(db, db, srv.PublishUsage)
	tracker.SetContextWindow(cfg.Usage.ContextWindowTokens)
	tracker.SetTestCommands(cfg.Usage.TestCommands)
	tracker.SetDefaultBudget(cfg.Usage.Budgets.DefaultTokens)
	tracker.SetBudgetHandler(srv.PublishBudget)
	srv.SetUsageDefaultBudget(cfg.Usage.Budgets.DefaultTokens)
	rep := usage.Reporter{
		Store:  db,
		Prices: usage.DefaultPrices().Merge(usagePrices(cfg)),
		Now:    time.Now,
	}
	srv.EnableUsage(ctx, tracker, rep)

	// notifications
	n := cfg.Notifications
	host, _ := os.Hostname()
	publish := func(sessID string, kind domain.NotifyKind) {
		srv.HubPublish(server.SessionEvent{Type: "notify", ID: sessID, Kind: string(kind)})
	}
	events := map[domain.NotifyKind]bool{}
	for _, k := range n.EventKinds() {
		events[k] = true
	}
	rec := notify.NewRecorder(db, notify.Settings{Events: events, MinInterval: n.MinIntervalDuration(), Host: host}, publish)
	srv.EnableNotifications(rec, db)
	var sinks []notify.Sink
	if n.Webhook != "" {
		sinks = append(sinks, &notify.WebhookSink{URL: n.Webhook})
	}
	if n.SlackChannel != "" {
		if tok, err := slack.ReadToken(slack.TokenPath(dir)); err == nil {
			sinks = append(sinks, &notify.SlackSink{Client: &slack.Client{Token: tok}, Channel: n.SlackChannel})
		} else {
			log.Printf("notify: slack_channel set but no Slack user token; run aiman slack auth")
		}
	}
	if len(sinks) > 0 {
		go notify.NewDispatcher(notify.StoreSource{Store: db}, notify.StoreCursor{Name: "serve", DB: db}, sinks...).Run(ctx, 2*time.Second)
	}

	ciDeps := ciwatch.Deps{
		Sessions: db,
		GitHub:   ciwatch.GHCLI{},
		Todos:    srv.TodoAccess(),
		Steer: func(id string, author domain.TodoAuthor, text string) {
			srv.Steerer().Enqueue(server.SteerNote{SessionID: id, Author: author, Text: text})
		},
		State: db,
	}
	// A nil *Recorder in the interface would not compare equal to nil.
	if rec := srv.NotifyRecorder(); rec != nil {
		ciDeps.Notify = rec
	}
	ciWatcher := ciwatch.New(ciDeps)
	srv.SetCIWatcher(ciWatcher)
	go ciWatcher.Run(ctx)

	scanner := agent.NewScanner(localExec)
	srv.SetAgentChoice(cfg.Agents.Preferred, scanner.Installed)
	verifyDeps := srv.VerifyDeps(verify.NewStore(filepath.Join(dir, "reviews")), func(command string) bool {
		return scanner.Installed(ctx, command)
	})
	// Only a passing review with evidence ticks Done-when met.
	verifyDeps.Done = srv.DoneItems()
	verifier := verify.NewManager(
		verify.Config{Agents: cfg.ReviewerCandidates(), BeforePR: cfg.Verify.BeforePR, Timeout: cfg.Verify.Timeout, TestCommand: cfg.Verify.TestCommand, Reproduce: cfg.Verify.Reproduce},
		verifyDeps,
	)
	srv.SetVerifier(verifier)
	verifier.Resume(ctx)
	defer verifier.Close()

	childTracker := &coordinator.Tracker{
		Todos:    srv.TodoAccess(),
		Sessions: func() ([]domain.Session, error) { return db.List(ctx) },
		PR:       coordinator.GHPR,
		Passed: func(childID string) bool {
			r, ok := verifier.LatestFor(childID)
			return ok && r.Passed()
		},
		// verify.before_pr does not gate child completion until nl-10.
		BeforePR: func(string) bool { return false },
		Report:   coordinator.ExecReport(localExec),
		Steer: func(id, text string) {
			srv.Steerer().Enqueue(server.SteerNote{SessionID: id, Author: domain.AuthorAiman, Text: text})
		},
	}
	if rec := srv.NotifyRecorder(); rec != nil {
		childTracker.Notify = rec
	}
	go childTracker.Run(ctx, coordinator.TrackInterval)

	log.Printf("aiman serve listening on %s", filepath.Join(dir, "aiman.sock"))
	return srv.Serve(ctx)
}

func ensureAimanSkill(ctx context.Context, db domain.SessionRepository) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("aiman skill: home: %v", err)
		return
	}
	var roots []string
	if db != nil {
		sessions, err := db.List(ctx)
		if err != nil {
			log.Printf("aiman skill: list sessions: %v", err)
		} else {
			for _, s := range sessions {
				roots = append(roots, s.WorktreePath, s.WorkingDirectory)
			}
		}
	}
	results, err := aimanskill.EnsureOnHost(home, roots)
	if err != nil {
		log.Printf("aiman skill: %v", err)
	}
	log.Printf("aiman skill: %s", aimanskill.Summarize(results))
}

func ensureAgentHooks(beforePR bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("aiman hooks: home: %v", err)
		return
	}
	results, err := agenthook.EnsureOnHost(home, beforePR)
	if err != nil {
		log.Printf("aiman hooks: %v", err)
	}
	log.Printf("aiman hooks: %s", agenthook.Summarize(results))
}

func serveWantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// usagePrices turns configured USD-per-million rows into confirmed prices.
// An empty table leaves the built-in defaults in place.
func usagePrices(cfg *config.Config) usage.PriceTable {
	if cfg == nil || len(cfg.Usage.Prices) == 0 {
		return nil
	}
	out := make(usage.PriceTable, len(cfg.Usage.Prices))
	for id, p := range cfg.Usage.Prices {
		out[id] = usage.Price{
			Input:        p.Input,
			Output:       p.Output,
			CacheRead:    p.CacheRead,
			CacheWrite5m: p.CacheWrite5m,
			CacheWrite1h: p.CacheWrite1h,
			Confirmed:    true,
		}
	}
	return out
}

func printServeUsage(w io.Writer) {
	fmt.Fprint(w, `aiman serve — agent API on THIS host

In-pane agents talk to this process over ~/.aiman/aiman.sock
(aiman session …, aiman context …, and the skill). One instance per host.

Do not run this on your laptop to enable remotes. Start it from the TUI:

  m  →  Agent API  →  select the remote  →  i  install/enable
  s restart   c reload   u update   ctrl+k stop   r probe

Foreground on the remote (debugging):
  aiman serve

systemd --user (installed by the TUI):
  systemctl --user status aiman-serve

On start it installs mosh when mosh-server is missing, so tmux attach can
use it. Without passwordless sudo it unpacks the distro package into
~/.local/bin. A missing package manager is logged and serve still starts.
It also installs or updates the bundled agent skill under $HOME
and in each known session worktree, and registers native-session hooks
in each installed agent's config (Claude, Grok, Cursor, Codex, Copilot,
agy, Kilo Code, Pi, Muse Code). Deep Code has no hook API: the same
start installs ~/.aiman/hooks/deepcode-launch.sh, which watches
~/.deepcode/projects/<code>/sessions-index.json via
aiman session deepcode-watch. Resume is deepcode --resume <uuid>.
Model and effort are DEEPCODE_MODEL and DEEPCODE_REASONING_EFFORT.
Permissions stay the user's defaultMode. Ageni is not hooked.
usage.get reports token totals from Claude and Codex transcripts.
It watches the PR of each session with watch_pr on (the default for
autonomous sessions) and steers failed checks and review comments.
verify.start runs a hidden reviewer from a different agent on a snapshot
of a session's tree; REQUIRED findings become that session's to-dos.
`)
}
