package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/hoststats"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/limits"
	"github.com/bouwerp/aiman/internal/memoryjob"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/selfupdate"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/bouwerp/aiman/internal/verify"
)

// rememberedPane is the last rendered screen served for one session. The next
// capture diffs against it. One memory per session, not per client: a client
// whose hash does not match gets a full screen.

type sessionCreator interface {
	CreateSession(ctx context.Context, cfg domain.SessionConfig) (*domain.Session, error)
}

// PTYRuntime is the slice of the PTY runtime serve uses. Production passes a
// *ptyruntime.Manager; a nil runtime means serve hosts no terminals.
type PTYRuntime interface {
	Create(spec ptyruntime.Spec) (*ptyruntime.SessionInfo, error)
	List() []ptyruntime.SessionInfo
	Get(id string) (*ptyruntime.SessionInfo, error)
	Write(id string, data []byte) error
	Resize(id string, cols, rows int) error
	Capture(id string, maxBytes int) ([]byte, error)
	CaptureScreen(id string) (string, error)
	CaptureFrame(id string) (view, all string, err error)
	TakeRedrawn(id string) []string
	OnAltScreen(id string) bool
	Kill(id string) error
	Forget(id string) error
	Subscribe(id string) ([]byte, <-chan []byte, func(), error)
}

var _ PTYRuntime = (*ptyruntime.Manager)(nil)

// isNilPTY reports a nil interface or an interface holding a nil pointer, so
// a typed nil never reaches the s.pty != nil checks.
func isNilPTY(p PTYRuntime) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

type Server struct {
	ln       *Listener
	repo     domain.SessionRepository
	sessList sessionListCache
	remote   domain.RemoteExecutor
	create   sessionCreator
	ctxStore domain.ContextStore
	pty      PTYRuntime
	// procInSession overrides inHolderTree, so tests can fake the ancestry check.
	procInSession func(pid int) bool
	// sessionOfPID overrides holderSessionOf, so tests can fake which
	// holder owns a pid.
	sessionOfPID func(pid int) (string, bool)
	// peerNotesOff is sessions.peer_notes: false.
	peerNotesOff atomic.Bool
	// agentNice is host.load.agent_nice, the nice increment pty.create
	// gives every agent pty.
	agentNice atomic.Int32
	version   string

	// hub carries events published by handlers to session.events streams.
	hub *eventHub

	mu     sync.Mutex
	sessMu map[string]*sync.Mutex

	// attaches counts live pty.attach streams per session so a dashboard
	// preview fit cannot shrink a session out from under a fullscreen client.
	attachMu sync.Mutex
	attaches map[string]int

	// panes remembers the last rendered capture per session so a preview poll
	// can return a row patch instead of the whole screen.
	paneMu sync.Mutex
	panes  map[string]rememberedPane

	// todos holds each session's to-do list and steer queues notes for agents.
	// Both stay nil until EnableTodos; Kick and Forget on a nil steerer are
	// no-ops, so handlers call them unconditionally.
	todos todoStore
	steer *steerer

	// steerMu guards in-flight typing and the time confirm last finished.
	// A timeout after the text is in the composer must not type it again, and
	// the classifier quiet window runs from confirm, not from the first Return.
	steerMu      sync.Mutex
	steerFlights map[string]steerFlight
	steerSettled map[string]time.Time
	// target is the session side of steering, the Server itself outside tests.
	target steerTarget

	// usage follows transcripts. reporter prices the stored buckets for usage.get.
	// Both stay nil until EnableUsage, and a nil tracker kick is a no-op.
	usage    *usage.Tracker
	reporter usageReporter
	// usageDefaultBudget is usage.budgets.default_tokens; 0 is none.
	usageDefaultBudget int64
	// todoLocks holds one *sync.Mutex per session id around a to-do write and
	// its render.
	todoLocks sync.Map
	// uncertainPanes holds the hash of the last low-confidence pane recorded
	// per session id, so a pane that does not change is recorded once.
	uncertainPanes sync.Map
	// sessionStates holds one cachedState per session id, written by the state
	// sweep and read by the event stream.
	sessionStates sync.Map
	// sweepMu is held while a sweep runs, so sweeps never stack. sweepSkipped
	// counts ticks lost to a running or overlong sweep.
	sweepMu      sync.Mutex
	sweepSkipped atomic.Int64
	// sweepMemos holds, per session id, the hook state the last pass saw and
	// the pass that last captured it; sweepPass counts passes. Both are guarded
	// by sweepMu. A pass drops the memo of every session no longer active.
	sweepMemos map[string]*sweepMemo
	sweepPass  uint64
	// sweepEvery is the sweep's current period in nanoseconds; zero is
	// notifySweepEvery.
	sweepEvery atomic.Int64

	// memoryJobs queues handover-derived memory candidates for the laptop.
	memoryJobs *memoryjob.Queue

	// ciWatch polls watched sessions' PRs; nil until SetCIWatcher.
	ciWatch *ciwatch.Watcher
	// ciLocal runs the local CI gate and answers ci.gate; nil until SetCILocal.
	ciLocal *ciLocalState
	// verifier runs reviewer sessions; nil until SetVerifier.
	verifier *verify.Manager
	// land holds session.land's lock directory and local gate.
	land landDeps
	// handovers runs handover child agents; nil until SetHandovers, and then
	// writeHandover keeps the mechanical write and enqueues the memory job.
	handovers *handoverchild.Manager
	// designer publishes design pages; nil until SetDesignPublisher.
	designer *design.Publisher
	// transcripts opens vendor transcripts; nil until SetTranscripts.
	transcripts TranscriptSource
	// trPins are the readers session.transcript opened lately, which the
	// event poll watches.
	trPins transcriptPins

	// agents is the model tiers from this host's config; agentInstalled
	// reports whether a command resolves on this host. session.create with no
	// agent picks from the tiers. Both stay empty until SetAgentChoice.
	agents         config.AgentsConfig
	agentInstalled func(ctx context.Context, command string) bool
	// consultDir holds consult scratch directories; empty is
	// ~/.aiman/consults (SetConsultDir).
	consultDir string

	// reviewGH reads PRs and reviewWT makes their worktrees; review creates
	// fail until both are set. reviewDir holds each review's state file and
	// scratch directory; empty is ~/.aiman/pr-reviews. reviewLocks holds one
	// *sync.Mutex per PR ref around a review create.
	reviewGH     review.GitHub
	reviewWT     ReviewWorktrees
	reviewDir    string
	reviewLocks  sync.Map
	reviewRunner review.Runner
	reviewCfg    config.ReviewConfig
	reviewRepos  map[string]config.RepoBootstrap

	// repos caches repo.list; empty until SetRepoLister.
	repos repoCache
	// restart is what session.restart needs beyond the session; empty until
	// SetRestartDeps, and a restart then launches the bare agent command.
	restart RestartDeps
	// hostStats samples /proc for host.stats, on its first call.
	hostStats *hoststats.Sampler
	// limits caches each provider's remaining allowance; nil until SetLimits.
	limits *limits.Manager
	aws    *awsState
	// holderHeld overrides heldByHolder, so tests can fake a holder.
	holderHeld func(pid int) bool

	// agentUpdates reports and applies agent CLI updates; nil until
	// SetAgentUpdates. agentHost names this host in its replies.
	agentUpdates *agentupdate.Manager
	agentHost    string

	// hostUpdates keeps serve on the latest release; nil until SetHostUpdates,
	// which means updates are off. hostName names this host in host.version.
	hostUpdates   *selfupdate.Manager
	hostName      string
	hostUpdatesMu sync.RWMutex

	// inflight counts long calls running over the socket; the host update
	// gate waits for it to reach zero.
	inflight atomic.Int64

	// adm is the admission gate and request accounting; admission() makes it.
	adm     *admission
	admOnce sync.Once

	notifyRec *notify.Recorder
	notifyLog NotifyLog
	notifyMu  sync.RWMutex
}

// SetMemoryJobs attaches the memory-candidate queue. It stays nil when serve
// could not open the queue; memory.jobs then returns an empty list.
func (s *Server) SetMemoryJobs(q *memoryjob.Queue) {
	s.memoryJobs = q
}

// SetAgentChoice sets the model tiers and the probe that reports whether a
// command is installed on this host.
func (s *Server) SetAgentChoice(agents config.AgentsConfig, installed func(ctx context.Context, command string) bool) {
	s.agents = config.AgentsConfig{
		Smart:     append([]config.PreferredAgent(nil), agents.Smart...),
		Preferred: append([]config.PreferredAgent(nil), agents.Preferred...),
		Update:    agents.Update,
	}
	s.agentInstalled = installed
}

// pickAgent returns the first installed entry of tier, falling back to the
// other tier when it has none.
func (s *Server) pickAgent(ctx context.Context, tier string) (coordinator.Pick, error) {
	var installed []string
	if s.agentInstalled != nil {
		for _, tier := range []string{config.TierSmart, config.TierWorker} {
			for _, e := range s.agents.Tier(tier) {
				name := e.Command()
				if name == "" || slices.Contains(installed, name) {
					continue
				}
				if s.agentInstalled(ctx, name) {
					installed = append(installed, name)
				}
			}
		}
	}
	return coordinator.PickTier(s.agents, tier, installed)
}

// todoStore is the to-do persistence serve needs: the shared Store plus the
// merge of an agent's own task-list snapshot.
type todoStore interface {
	todo.Store
	Mirror(sessionID string, snap []domain.TodoSnapshotItem, deliveredAt time.Time) (domain.TodoList, todo.Change, error)
}

// NotifyLog reads the notification ledger.
type NotifyLog interface {
	NotificationsSince(ctx context.Context, after int64, limit int) ([]domain.Notification, error)
	LastNotificationSeq(ctx context.Context) (int64, error)
}

const (
	notifySweepEvery = 2 * time.Second
	notifyRetention  = 7 * 24 * time.Hour
)

type rememberedPane struct {
	text string
}

func New(ln *Listener, repo domain.SessionRepository, remote domain.RemoteExecutor, create sessionCreator, ctxStore domain.ContextStore, ptyMgr PTYRuntime, version string) *Server {
	if version == "" {
		version = "dev"
	}
	if isNilPTY(ptyMgr) {
		ptyMgr = nil
	}
	s := &Server{
		ln:       ln,
		repo:     repo,
		remote:   remote,
		create:   create,
		ctxStore: ctxStore,
		pty:      ptyMgr,
		version:  version,
		sessMu:   map[string]*sync.Mutex{},
		attaches: map[string]int{},
		hub:      newEventHub(),

		hostStats: hoststats.New(),
	}
	s.target = s
	s.watchSessionList(repo)
	return s
}

func (s *Server) Serve(ctx context.Context) error {
	if s.steer != nil {
		go s.steer.Run(ctx)
	}
	go s.runStateSweep(ctx)
	go func() {
		<-ctx.Done()
		_ = s.ln.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	pid, pidOK := peerPID(conn)
	ctx = withPeerPID(ctx, pid, pidOK)
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if errorsIsEOF(err) {
				return
			}
			return
		}
		line = bytesTrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if isPTYAttach(line) {
			s.handlePTYAttachConn(ctx, conn, line)
			return
		}
		// Like pty.attach, the event stream takes over the connection instead of
		// answering once.
		if isSessionEvents(line) {
			s.handleSessionEventsConn(ctx, conn, line)
			return
		}
		resp := s.dispatch(ctx, line)
		out, encErr := EncodeResponse(resp)
		if encErr != nil {
			return
		}
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// Inflight is the number of long calls running now.
func (s *Server) Inflight() int {
	return int(s.inflight.Load())
}

// track counts method in Inflight while it runs when it is a long call; the
// returned func ends the count.
func (s *Server) track(method string) func() {
	if !longCall(method) {
		return func() {}
	}
	s.inflight.Add(1)
	return func() { s.inflight.Add(-1) }
}

// decodeAdmit decodes a request line and passes it through admission. It
// returns the request, its context and the func that ends it, or the answer
// to send instead.
func (s *Server) decodeAdmit(ctx context.Context, line []byte) (Request, context.Context, func(), *Response) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return req, ctx, nil, &Response{ID: "", Error: &Error{Code: CodeInvalidParams, Message: err.Error()}}
	}
	ctx, done, refused := s.admit(ctx, req)
	return req, ctx, done, refused
}

func (s *Server) dispatch(ctx context.Context, line []byte) Response {
	req, ctx, done, refused := s.decodeAdmit(ctx, line)
	if refused != nil {
		return *refused
	}
	defer done()
	defer s.track(req.Method)()
	switch req.Method {
	case "ping":
		return Response{ID: req.ID, Result: map[string]any{
			"type":     "pong",
			"version":  s.version,
			"protocol": ProtocolVersion,
		}}
	case "session.list":
		return s.handleList(ctx, req)
	case "session.get":
		return s.handleGet(ctx, req)
	case "session.read":
		return s.handleRead(ctx, req)
	case "session.log":
		return s.handleSessionLog(ctx, req)
	case "session.replay":
		return s.handleSessionReplay(ctx, req)
	case "session.prompt":
		return s.handlePrompt(ctx, req)
	case "session.wait":
		return s.handleWait(ctx, req)
	case "session.create":
		return s.handleCreate(ctx, req)
	case "session.rename":
		return s.handleRename(ctx, req)
	case "session.move":
		return s.handleMove(ctx, req)
	case "session.forget":
		return s.handleForget(ctx, req)
	case "session.restart", "session.terminate", "session.land", "repo.list", "host.stats", "host.version", "host.update":
		return s.dispatchLifecycle(ctx, req)
	case "session.register":
		return s.handleRegister(ctx, req)
	case "session.report_agent_session":
		return s.handleReportAgentSession(ctx, req)
	case "notify.since":
		return s.handleNotifySince(ctx, req)
	case "verify.start":
		return s.handleVerifyStart(ctx, req)
	case "verify.status":
		return s.handleVerifyStatus(ctx, req)
	case "ci.get", "ci.watch", "ci.local", "ci.status", "ci.gate", "ci.ready":
		return s.dispatchCI(ctx, req)
	case "context.put":
		return s.handleContextPut(ctx, req)
	case "context.get":
		return s.handleContextGet(ctx, req)
	case "context.list":
		return s.handleContextList(ctx, req)
	case "context.find":
		return s.handleContextFind(ctx, req)
	case "context.pack":
		return s.handleContextPack(ctx, req)
	case "context.stats":
		return s.handleContextStats(ctx, req)
	case "context.verify":
		return s.handleContextVerify(ctx, req)
	case "usage.get", "limits.get":
		return s.dispatchUsage(ctx, req)
	case "aws.status", "aws.login_start", "aws.login_status", "aws.log":
		return s.dispatchAWS(ctx, req)
	case "agents.status", "agents.update", "agents.move_prefix":
		return s.dispatchAgents(ctx, req)
	case "session.transcript", "session.transcript_item", "session.process_output", "session.file":
		return s.dispatchTranscript(ctx, req)
	case "design.publish":
		return s.handleDesignPublish(ctx, req)
	case "review.reveal", "review.post", "review.refresh", "review.run", "review.debug", "review.coverage",
		"review.fix", "review.push", "review.assess", "review.approve":
		return s.dispatchReview(ctx, req)
	case "handover.write":
		return s.handleHandoverWrite(ctx, req)
	case "handover.point":
		return s.handleHandoverPoint(ctx, req)
	case "memory.jobs":
		return s.handleMemoryJobs(ctx, req)
	case "memory.done":
		return s.handleMemoryDone(ctx, req)
	case "todo.get", "todo.summary", "todo.add", "todo.update", "todo.move", "todo.remove", "todo.mirror", "todo.ensure_done", "todo.mark_met", "todo.gate", "todo.approve_design", "todo.approve_review", "steer.send":
		return s.handleTodo(ctx, req)
	case "pty.create":
		return s.handlePTYCreate(ctx, req)
	case "pty.list":
		return s.handlePTYList(ctx, req)
	case "pty.get":
		return s.handlePTYGet(ctx, req)
	case "pty.input":
		return s.handlePTYInput(ctx, req)
	case "pty.run":
		return s.handlePTYRun(ctx, req)
	case "pty.wait-output":
		return s.handlePTYWaitOutput(ctx, req)
	case "pty.capture":
		return s.handlePTYCapture(ctx, req)
	case "pty.resize":
		return s.handlePTYResize(ctx, req)
	case "pty.kill":
		return s.handlePTYKill(ctx, req)
	case "pty.forget":
		return s.handlePTYForget(ctx, req)
	default:
		return Response{ID: req.ID, Error: &Error{Code: CodeInvalidParams, Message: "unknown method " + req.Method}}
	}
}

func errorsIsEOF(err error) bool {
	return err == io.EOF || strings.Contains(err.Error(), "use of closed network connection")
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func (s *Server) lockSession(id string) func() {
	s.mu.Lock()
	m, ok := s.sessMu[id]
	if !ok {
		m = &sync.Mutex{}
		s.sessMu[id] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// dispatchAgents routes the agents.* methods.
func (s *Server) dispatchAgents(ctx context.Context, req Request) Response {
	switch req.Method {
	case "agents.update":
		return s.handleAgentsUpdate(ctx, req)
	case "agents.move_prefix":
		return s.handleAgentsMovePrefix(ctx, req)
	default:
		return s.handleAgentsStatus(ctx, req)
	}
}

func errResp(id, code, msg string) Response {
	return Response{ID: id, Error: &Error{Code: code, Message: msg}}
}

// EnableNotifications wires the notification recorder and log. Must be called before Serve.
func (s *Server) EnableNotifications(rec *notify.Recorder, log NotifyLog) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.notifyRec = rec
	s.notifyLog = log
}

// NotifyRecorder returns the recorder if notifications are enabled.
func (s *Server) NotifyRecorder() *notify.Recorder {
	s.notifyMu.RLock()
	defer s.notifyMu.RUnlock()
	return s.notifyRec
}

// HubPublish publishes an event to all subscribers.
func (s *Server) HubPublish(ev SessionEvent) {
	if s.hub != nil {
		s.hub.Publish(ev)
	}
}
