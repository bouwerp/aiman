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
	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/aimanskill"
	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/debuglog"
	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/infra/slack"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/limits"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/bouwerp/aiman/internal/verify"
)

// awsCredentialsWriter writes the device flow's credentials into the host file.
type awsCredentialsWriter struct {
	path, lockPath string
}

func (w awsCredentialsWriter) WriteHostCredentials(profile string, creds *awsdelegation.SessionCredentials) error {
	return awsdelegation.WriteHostCredentials(w.path, w.lockPath, profile, creds)
}

// wireAWSLogin starts the device-flow engine when this host has delegations and
// returns the call that stops it, else nil and the aws.* methods stay unavailable.
func wireAWSLogin(srv *server.Server, delegations []config.ServeAWSDelegation, home string) (func(), *awslogin.Manager) {
	if len(delegations) == 0 {
		return nil, nil
	}
	if home == "" {
		log.Printf("aws sign-in unavailable: no home directory")
		return nil, nil
	}
	credentials := filepath.Join(home, ".aws", "credentials")
	engine := awslogin.New(awslogin.Options{
		Home: home,
		Writer: awsCredentialsWriter{
			path:     credentials,
			lockPath: filepath.Join(home, config.DirName, "aws-credentials.lock"),
		},
	})
	srv.SetAWS(config.ServeAWSConfig{Delegations: delegations}, credentials, engine)
	// The watchers stop first so none reads the engine while it closes and
	// writes a finished row for a login serve is abandoning.
	return func() {
		srv.CloseAWSLogin()
		engine.Close()
	}, engine
}

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
	flow.SetUpdateGuards(cfg.AgentUpdateGuards())
	flow.SetSessionStore(db)

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
	extendServePath()
	ensureAimanSkill(ctx, db)
	ensureAgentHooks(agentHookOptions(cfg))
	store := contextstore.NewFiles(contextstore.Root(dir))
	if ix, err := contextstore.OpenIndex(db.DB(), db.Reader()); err != nil {
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
	if err := srv.BackfillNames(ctx); err != nil {
		log.Printf("session name backfill: %v", err)
	}
	flow.SetDoneItems(srv.DoneItems())
	if jobs, err := memoryjob.Open(db.DB(), db.Reader()); err != nil {
		log.Printf("memory jobs unavailable: %v", err)
	} else {
		srv.SetMemoryJobs(jobs)
	}
	todos := todo.NewFiles(filepath.Join(dir, "todos"))
	srv.EnableTodos(todos, db)
	srv.SetPeerNotes(cfg.Sessions.PeerNotesOn())
	srv.SetAgentNice(cfg.Host.Load.AgentNiceOrDefault())
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
	limitsManager := limits.NewManager(limits.DefaultSources(limits.HostEnv()), time.Now)
	defer limitsManager.Close()
	srv.SetLimits(limitsManager)
	closeAWS, awsEngine := wireAWSLogin(srv, cfg.AWS.Delegations, home)
	if closeAWS != nil {
		defer closeAWS()
	}

	// notifications
	n := cfg.Notifications
	host, _ := os.Hostname()
	publish := func(nt domain.Notification) {
		srv.HubPublish(server.SessionEvent{Type: "notify", ID: nt.SessionID, Kind: string(nt.Kind), Text: nt.Text})
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

	agentUpdates := newAgentUpdates(cfg.Agents.Update, dir, agentUpdateDeps{
		Exec:        pathExec{base: agentupdate.LocalExec{}, home: home},
		HTTP:        agentupdate.HTTPFetcher{},
		EnsureHooks: func() { ensureAgentHooks(agentHookOptions(cfg)) },
		Home:        home,
	})
	agentUpdates.Sessions = srv.LiveAgentSessions
	agentUpdates.BaseCtx = ctx
	wireAgentUpdateNotify(ctx, agentUpdates, srv.NotifyRecorder(), host)
	srv.SetAgentUpdates(agentUpdates, host)
	go agentUpdates.Run(ctx)

	ciDeps := ciwatch.Deps{
		Sessions: db,
		GitHub:   ciwatch.GHCLI{},
		Todos:    srv.TodoAccess(),
		Steer: func(id string, author domain.TodoAuthor, text string) {
			srv.Steerer().Enqueue(server.SteerNote{SessionID: id, Author: author, Text: text})
		},
		State:         db,
		TrustedLogins: cfg.CI.TrustedLogins,
	}
	// A nil *Recorder in the interface would not compare equal to nil.
	if rec := srv.NotifyRecorder(); rec != nil {
		ciDeps.Notify = rec
	}
	ciWatcher := ciwatch.New(ciDeps)
	srv.SetCIWatcher(ciWatcher)
	go ciWatcher.Run(ctx)

	scanner := agent.NewScanner(localExec)
	srv.SetAgentChoice(cfg.Agents, scanner.Installed)
	srv.SetRepoLister(gitManager)
	srv.SetRestartDeps(server.RestartDeps{Config: cfg, Engine: flow.SkillEngine, Ignorer: gitManager})
	srv.SetConsultDir(filepath.Join(dir, "consults"))
	srv.SetReviewGitHub(&review.GHCLI{Run: review.ExecRun, RunInput: review.ExecRunInput})
	srv.SetReviewWorktrees(gitManager)
	srv.SetReviewDir(filepath.Join(dir, "pr-reviews"))
	srv.SetReviewRunner(review.Runner{Docker: review.DockerExec, UID: os.Getuid(), GID: os.Getgid()})
	srv.SetReviewConfig(cfg.Review, cfg.Repos)
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

	var handovers *handoverchild.Manager
	if cfg.Handover.ChildEnabled() {
		handoverDeps := srv.HandoverDeps(handoverchild.NewStore(filepath.Join(dir, "handover-runs")), func(command string) bool {
			return scanner.Installed(ctx, command)
		})
		handoverDeps.Candidates = cfg.HandoverCandidates
		handoverDeps.BaseDir = filepath.Join(dir, "handovers")
		handoverDeps.Timeout = cfg.Handover.ChildTimeout()
		handovers = handoverchild.NewManager(handoverDeps)
		// Resume first so a run left active by the last serve absorbs new
		// triggers instead of starting a second child.
		handovers.Resume(ctx)
		srv.SetHandovers(handovers)
		defer handovers.Close()
	}

	hostUpdates := newHostUpdates(cfg.Host.Update, dir, version, hostUpdateDeps{
		Busy: []func() int{srv.Inflight, awsEngine.Pending, handovers.Starting, agentUpdates.Updating},
	})
	startHostUpdates(ctx, srv, hostUpdates, host)

	srv.SetDesignPublisher(&design.Publisher{Cfg: cfg.Design, Home: home, Run: design.ExecRun})
	srv.SetTranscripts(server.NewTranscriptSource(home))

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
		Ledger:         srv.AppendLedger,
		Retire:         srv.RetireConsult,
		KeepReport:     srv.KeepConsultReport,
		Alive:          srv.ConsultAlive,
		ConsultTimeout: cfg.Handover.ChildTimeout(),
		// A review session's consults get review.consult_timeout instead.
		ConsultTimeoutFor: coordinator.ConsultTimeouts(cfg.Review.ConsultTimeoutOrDefault(), cfg.Handover.ChildTimeout()),
		FixTimeout:        cfg.Review.FixTimeoutOrDefault(),
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
			roots = skillRoots(sessions)
		}
	}
	results, err := aimanskill.EnsureOnHost(home, roots)
	if err != nil {
		log.Printf("aiman skill: %v", err)
	}
	log.Printf("aiman skill: %s", aimanskill.Summarize(results))
}

// skillRoots are the session directories the bundled skill is copied into. A
// review's worktree and a fixer's fix tree are untrusted PR checkouts and get
// no copy; their agents run in the scratch working directory. A copy in a fix
// tree would also dirty it, which review.push refuses.
func skillRoots(sessions []domain.Session) []string {
	var roots []string
	for _, s := range sessions {
		if s.ReviewPR == "" && s.FixPR == "" {
			roots = append(roots, s.WorktreePath)
		}
		roots = append(roots, s.WorkingDirectory)
	}
	return roots
}

// agentHookOptions is what serve's config asks of the agent hooks.
func agentHookOptions(cfg *config.Config) agenthook.Options {
	return agenthook.Options{BeforePR: cfg.Verify.BeforePR}
}

func ensureAgentHooks(opts agenthook.Options) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("aiman hooks: home: %v", err)
		return
	}
	results, err := agenthook.EnsureOnHostWith(home, opts)
	if err != nil {
		log.Printf("aiman hooks: %v", err)
	}
	log.Printf("aiman hooks: %s", agenthook.Summarize(results))
	// Codex app-server can take seconds to start; serve does not wait for it.
	go trustCodexHooks(home)
}

// trustCodexHooks marks aiman's Codex hooks trusted. A failure only costs the
// user Codex's review prompt, so it is logged.
func trustCodexHooks(home string) {
	keys, err := agenthook.TrustCodexHooks(context.Background(), home, agentPATH(home))
	if err != nil {
		log.Printf("aiman hooks: codex trust: %v", err)
		return
	}
	if len(keys) > 0 {
		log.Printf("aiman hooks: codex trusted %d aiman hooks", len(keys))
	}
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

On start it installs or updates the bundled agent skill under $HOME
and in each known session worktree, and registers native-session hooks
in each installed agent's config (Claude, Grok, Cursor, Codex, Copilot,
agy, Kilo Code, Pi, Muse Code). Deep Code has no hook API: the same
start installs ~/.aiman/hooks/deepcode-launch.sh, which watches
~/.deepcode/projects/<code>/sessions-index.json via
aiman session deepcode-watch. Resume is deepcode --resume <uuid>.
Model and effort are DEEPCODE_MODEL and DEEPCODE_REASONING_EFFORT.
Permissions stay the user's defaultMode. Ageni is not hooked.
usage.get reports token totals from Claude and Codex transcripts.
limits.get reports what each provider's allowance or balance has left.
It watches the PR of each session with watch_pr on (the default for
autonomous sessions) and steers failed checks and review comments.
verify.start runs a hidden reviewer from a different agent on a snapshot
of a session's tree; REQUIRED findings become that session's to-dos.
`)
}
