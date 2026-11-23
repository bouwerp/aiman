package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/overview"
	"github.com/bouwerp/aiman/internal/transcript"
)

// Overview run states.
const (
	OverviewStarting = "starting"
	OverviewRunning  = "running"
	OverviewDone     = "done"
	OverviewFailed   = "failed"
)

const (
	// overviewEndedReason is the session.ended reason of a retired run.
	overviewEndedReason = "overview_finished"
	// overviewKeep is how many finished runs stay on disk.
	overviewKeep = 20
	// DefaultOverviewPoll is how often the watcher looks for the report.
	DefaultOverviewPoll = 5 * time.Second
)

// OverviewRun is one overview run, as overview.get and overview.list answer
// it and as it is kept on disk.
type OverviewRun struct {
	ID        string           `json:"id"`
	Session   string           `json:"session,omitempty"`
	Agent     string           `json:"agent"`
	Model     string           `json:"model,omitempty"`
	State     string           `json:"state"`
	StartedAt time.Time        `json:"started_at"`
	EndedAt   *time.Time       `json:"ended_at,omitempty"`
	Error     string           `json:"error,omitempty"`
	Sessions  int              `json:"sessions"`
	NamesOnly []string         `json:"names_only,omitempty"`
	Markdown  string           `json:"markdown,omitempty"`
	Report    *overview.Report `json:"report,omitempty"`
	// NeedsYou counts the report's Needs you bullets, set when the run is done.
	NeedsYou *int `json:"needs_you,omitempty"`
	// Steps are the run's phases in order (overviewStepLabels).
	Steps []OverviewStep `json:"steps,omitempty"`

	// finishing is set once a finish has begun, so a run ends once.
	finishing bool
}

// overviewState is serve's one running overview, if any, and the background
// context its goroutine runs on.
type overviewState struct {
	running *OverviewRun
	bg      *overviewBG
}

// SetOverviewDir sets the directory overview runs live in. Empty is
// ~/.aiman/overviews.
func (s *Server) SetOverviewDir(dir string) { s.overviewDir = dir }

// SetOverviewTimeout bounds one run; zero is config.DefaultOverviewTimeout.
func (s *Server) SetOverviewTimeout(d time.Duration) { s.overviewTimeout = d }

func (s *Server) overviewBase() string {
	if s.overviewDir != "" {
		return s.overviewDir
	}
	home := hostHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, config.DirName, "overviews")
}

// dispatchOverview routes the overview.* methods.
func (s *Server) dispatchOverview(ctx context.Context, req Request) Response {
	switch req.Method {
	case "overview.run":
		return s.handleOverviewRun(ctx, req)
	case "overview.list":
		return s.handleOverviewList(req)
	default:
		return s.handleOverviewGet(req)
	}
}

// handleOverviewRun starts one overview run: a hidden smart-tier agent that
// reads a bundle of every visible session and writes overview.md.
func (s *Server) handleOverviewRun(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator starts an overview")
	}
	var p struct {
		Agent string `json:"agent"`
		Model string `json:"model"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if s.create == nil {
		return errResp(req.ID, CodeCreateFailed, "create not configured")
	}
	base := s.overviewBase()
	if base == "" {
		return errResp(req.ID, CodeCreateFailed, "no home directory for overview runs")
	}
	s.overviewMu.Lock()
	if run := s.overview.running; run != nil {
		s.overviewMu.Unlock()
		return errResp(req.ID, CodeOverviewRunning, fmt.Sprintf("overview %s is still running", run.ID))
	}
	run := &OverviewRun{ID: uuid.NewString()[:8], State: OverviewStarting, StartedAt: time.Now().UTC(), Steps: newOverviewSteps()}
	s.overview.running = run
	s.overviewMu.Unlock()

	pick, err := s.pickOverviewAgent(ctx, p.Agent, p.Model)
	if err != nil {
		s.clearOverview(run)
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	s.overviewMu.Lock()
	run.Agent, run.Model = pick.Agent, pick.Model
	snap := run.clone()
	s.overviewMu.Unlock()
	// Saved before the goroutine starts, so the run is on disk even if serve
	// dies before the first step.
	s.saveOverview(base, &snap)
	s.publishOverview(&snap, "")
	s.goOverview(func(bg context.Context) { s.runOverview(bg, base, run, pick) })
	return Response{ID: req.ID, Result: map[string]any{"type": "overview_run", "run": snap}}
}

// pickOverviewAgent is the named agent when it is installed, with the
// model of its smart-tier entry unless one is given; else the first
// installed smart-tier entry.
func (s *Server) pickOverviewAgent(ctx context.Context, name, model string) (config.PreferredAgent, error) {
	installed := func(command string) bool {
		return s.agentInstalled != nil && s.agentInstalled(ctx, command)
	}
	name, model = strings.TrimSpace(name), strings.TrimSpace(model)
	if name != "" {
		command, err := s.restartAgent(ctx, name)
		if err != nil {
			return config.PreferredAgent{}, err
		}
		pick := config.PreferredAgent{Agent: command, Model: model}
		for _, e := range s.agents.Smart {
			if agent.BaseOf(e.Agent) == agent.BaseOf(command) && model == "" {
				pick.Model, pick.Effort = e.Model, e.Effort
				break
			}
		}
		return pick, nil
	}
	if model != "" {
		return config.PreferredAgent{}, errors.New("model needs agent")
	}
	for _, e := range s.agents.Smart {
		if installed(e.Agent) {
			return e, nil
		}
	}
	return config.PreferredAgent{}, errors.New("no agents.smart entry is installed on this host; name an agent")
}

// runOverview does a run's slow work on serve's background context:
// collect, bundle, start the agent, then watch for the report. Each step is
// saved and published as it changes; a failed step fails the run.
func (s *Server) runOverview(ctx context.Context, base string, run *OverviewRun, pick config.PreferredAgent) {
	defer s.overviewPanic(base, run)
	dir := filepath.Join(base, run.ID)
	fail := func(sess domain.Session, why string) {
		s.finishOverview(ctx, base, run, sess, OverviewFailed, "", nil, why)
	}

	s.updateOverview(base, run, StepCollect, func(r *OverviewRun, now time.Time) { r.startStep(StepCollect, now) })
	inputs, err := s.overviewInputs(ctx)
	if err != nil {
		fail(domain.Session{}, err.Error())
		return
	}
	nonce := overview.NewNonce()
	bundle := overview.Build(inputs, nonce, nil)
	detail := plural(len(bundle.Sessions), "session")
	if n := len(bundle.NamesOnly); n > 0 {
		detail += fmt.Sprintf(" (plus %d by name only)", n)
	}
	s.updateOverview(base, run, StepCollect, func(r *OverviewRun, now time.Time) {
		r.Sessions, r.NamesOnly = len(bundle.Sessions), bundle.NamesOnly
		r.endStep(StepCollect, detail, now)
		r.startStep(StepBundle, now)
	})

	if err := prepareConsultDir(ctx, dir, overview.Brief(nonce, dir)); err != nil {
		fail(domain.Session{}, err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(dir, overview.BundleFile), []byte(bundle.Text), 0o600); err != nil {
		fail(domain.Session{}, err.Error())
		return
	}
	s.updateOverview(base, run, StepBundle, func(r *OverviewRun, now time.Time) {
		r.endStep(StepBundle, formatKiB(len(bundle.Text)), now)
		r.startStep(StepStartAgent, now)
	})

	if ctx.Err() != nil {
		fail(domain.Session{}, overviewClosing)
		return
	}
	if !s.PressureOpen() {
		fail(domain.Session{}, CodeHostUnderPressure+": host under memory pressure; run the overview again later")
		return
	}
	sess, err := s.startOverviewAgent(ctx, dir, run.ID, pick)
	if err != nil {
		if ctx.Err() != nil {
			fail(domain.Session{}, overviewClosing)
			return
		}
		fail(domain.Session{}, err.Error())
		return
	}
	// Recorded at once, so a serve that dies now can still retire it.
	s.updateOverview(base, run, StepStartAgent, func(r *OverviewRun, _ time.Time) { r.Session = sess.ID })
	if ctx.Err() != nil {
		fail(sess, overviewClosing)
		return
	}
	agentLabel := pick.Agent
	if pick.Model != "" {
		agentLabel += " (" + pick.Model + ")"
	}
	snap := s.updateOverview(base, run, StepStartAgent, func(r *OverviewRun, now time.Time) {
		r.endStep(StepStartAgent, agentLabel, now)
		r.State = OverviewRunning
		r.startStep(StepReport, now)
	})
	s.appendEvent(ctx, domain.OverviewLedger, domain.EventActorUser, domain.EventOverviewStarted, map[string]any{
		"run": run.ID, "session": sess.ID, "agent": snap.Agent, "model": snap.Model, "sessions": snap.Sessions,
	})
	s.watchOverview(ctx, base, run, sess)
}

// overviewClosing is the step error of a run serve's close cut short.
const overviewClosing = "serve closing"

// startOverviewAgent starts the hidden overview session in dir.
func (s *Server) startOverviewAgent(ctx context.Context, dir, runID string, pick config.PreferredAgent) (domain.Session, error) {
	runAgent := domain.Agent{Name: pick.Agent, Command: pick.Agent, LaunchModel: pick.Model, LaunchEffort: pick.Effort}
	if known, ok := agent.FindKnown(pick.Agent); ok {
		known.LaunchModel, known.LaunchEffort = pick.Model, pick.Effort
		runAgent = known
	}
	return s.startConsult(ctx, domain.SessionConfig{
		Name:             "overview-" + runID,
		Role:             domain.SessionRoleOverview,
		Tier:             config.TierSmart,
		Agent:            &runAgent,
		AdHoc:            true,
		PromptFree:       true,
		PreparedWorktree: dir,
		InitialPrompt: "Read " + domain.AimanTaskFileName + " and follow it. Read " + overview.BundleFile +
			" and write " + filepath.Join(dir, overview.ReportFile) + ", then stop.",
	})
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func formatKiB(n int) string {
	return fmt.Sprintf("%d KiB", (n+1023)/1024)
}

// overviewInputs collects every visible session whose agent has not ended.
func (s *Server) overviewInputs(ctx context.Context) ([]overview.Input, error) {
	list, err := s.loadSessions(ctx)
	if err != nil {
		return nil, err
	}
	var out []overview.Input
	for _, sess := range list {
		if sess.Role.Hidden() || sess.AgentEnded {
			continue
		}
		in := overview.Input{Session: sess}
		if st, ok := s.cachedSessionState(sess.ID); ok {
			in.State = st.State
		}
		if l, err := s.TodoAccess().Get(sess.ID); err == nil {
			in.Todos = l
		}
		in.Items = s.overviewTranscript(sess)
		if lister, ok := s.repo.(recentEventLister); ok {
			if evs, err := lister.ListRecentSessionEvents(ctx, sess.ID, overview.LedgerEvents); err == nil {
				in.Events = evs
			}
		}
		out = append(out, in)
	}
	return out, nil
}

func (s *Server) overviewTranscript(sess domain.Session) []transcript.Item {
	if s.transcripts == nil {
		return nil
	}
	r, err := s.transcripts.Open(sess, "")
	if err != nil {
		return nil
	}
	page, err := r.Page(nil, overview.TranscriptItems)
	if err != nil {
		return nil
	}
	return page.Items
}

// watchOverview waits for a valid report, an ended agent, the timeout or
// serve's close, then retires the run's session.
func (s *Server) watchOverview(ctx context.Context, base string, run *OverviewRun, sess domain.Session) {
	timeout := s.overviewTimeout
	if timeout <= 0 {
		timeout = config.DefaultOverviewTimeout
	}
	poll := s.overviewPoll
	if poll <= 0 {
		poll = DefaultOverviewPoll
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	w := &overviewWatch{base: base, run: run, sess: sess, timeout: timeout}
	for {
		select {
		case <-ctx.Done():
			s.finishOverview(ctx, base, run, sess, OverviewFailed, "", nil, overviewClosing)
			return
		case <-tick.C:
		}
		if s.overviewEnded(run) || s.overviewStep(ctx, w, time.Now()) {
			return
		}
	}
}

func (s *Server) overviewEnded(run *OverviewRun) bool {
	s.overviewMu.Lock()
	defer s.overviewMu.Unlock()
	return run.ended() || run.finishing
}

// overviewWatch is one run's watcher state.
type overviewWatch struct {
	base    string
	run     *OverviewRun
	sess    domain.Session
	timeout time.Duration
	asked   bool
}

// overviewStep looks once: a valid report ends the run done; an invalid one
// gets one reminder; an ended agent or a run past its timeout fails. It
// reports whether the run ended.
func (s *Server) overviewStep(ctx context.Context, w *overviewWatch, now time.Time) bool {
	md, found := readOverviewReport(filepath.Join(w.base, w.run.ID, overview.ReportFile))
	var parseErr error
	if found {
		parsed, err := overview.ParseReport(md)
		if err == nil {
			s.finishOverview(ctx, w.base, w.run, w.sess, OverviewDone, md, &parsed, "")
			return true
		}
		parseErr = err
		if !w.asked && s.steer != nil {
			w.asked = true
			s.steer.Enqueue(SteerNote{SessionID: w.sess.ID, Author: domain.AuthorAiman,
				Text: "[aiman] " + overview.ReportFile + " is not valid: " + err.Error() + ". Write the four sections exactly as the brief says."})
			s.updateOverview(w.base, w.run, StepReport, func(r *OverviewRun, _ time.Time) {
				if st := r.step(StepReport); st != nil {
					st.Detail = "reminded: " + err.Error()
				}
			})
		}
	}
	ended := !serverTerminal{s}.Alive(ctx, w.sess)
	if cur, ok := s.sessionByID(ctx, w.sess.ID); ok && cur.AgentEnded {
		ended = true
	}
	switch {
	case ended:
		s.finishOverview(ctx, w.base, w.run, w.sess, OverviewFailed, md, nil, failWhy("the agent ended without a valid report", parseErr))
		return true
	case now.After(w.run.StartedAt.Add(w.timeout)):
		s.finishOverview(ctx, w.base, w.run, w.sess, OverviewFailed, md, nil, failWhy("no valid report within "+w.timeout.String(), parseErr))
		return true
	}
	return false
}

func failWhy(why string, parseErr error) string {
	if parseErr != nil {
		return why + ": " + parseErr.Error()
	}
	return why
}

// readOverviewReport reads the report through a size cap.
func readOverviewReport(path string) (string, bool) {
	f, err := os.Open(path) //nolint:gosec // path is under the overview base
	if err != nil {
		return "", false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, overview.ReportMaxBytes+1))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// finishOverview records the outcome, retires the session (when sess has an
// id) and removes the run's scratch directory. A done run marks report and
// cleanup done; a failed run fails its running step and leaves later steps
// pending. It runs once per run, on a context serve's close cannot cut.
func (s *Server) finishOverview(ctx context.Context, base string, run *OverviewRun, sess domain.Session, state, md string, report *overview.Report, why string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	s.overviewMu.Lock()
	if run.ended() || run.finishing {
		s.overviewMu.Unlock()
		return
	}
	run.finishing = true
	s.overviewMu.Unlock()

	if state == OverviewDone {
		detail := "report written"
		if report != nil {
			detail = fmt.Sprintf("4 sections, %d need you", len(report.NeedsYou))
		}
		s.updateOverview(base, run, StepReport, func(r *OverviewRun, now time.Time) {
			r.endStep(StepReport, detail, now)
			r.startStep(StepCleanup, now)
		})
	}

	if sess.ID != "" {
		if err := (serverTerminal{s}).Stop(ctx, sess); err != nil {
			log.Printf("overview %s: stop: %v", run.ID, err)
		}
		if err := s.dropSession(ctx, sess, overviewEndedReason); err != nil {
			log.Printf("overview %s: forget: %v", run.ID, err)
		}
		s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionEnded, map[string]any{"reason": overviewEndedReason})
	}
	dir := filepath.Join(base, run.ID)
	if filepath.Dir(dir) == filepath.Clean(base) {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("overview %s: remove %s: %v", run.ID, dir, err)
		}
	}

	step := StepCleanup
	s.overviewMu.Lock()
	now := time.Now().UTC()
	if state == OverviewDone {
		run.endStep(StepCleanup, "", now)
		run.Markdown, run.Report = md, report
		if report != nil {
			n := len(report.NeedsYou)
			run.NeedsYou = &n
		}
	} else {
		for _, st := range run.Steps {
			if st.State == StepRunning {
				step = st.Name
			}
		}
		run.failRunningStep(why, now)
	}
	run.State, run.EndedAt = state, &now
	if s.overview.running == run {
		s.overview.running = nil
	}
	snapshot := run.clone()
	s.overviewMu.Unlock()
	s.saveOverview(base, &snapshot)
	s.pruneOverviews(base)
	payload := map[string]any{"run": run.ID, "state": state}
	if snapshot.Error != "" {
		payload["error"] = snapshot.Error
	}
	if snapshot.NeedsYou != nil {
		payload["needs_you"] = *snapshot.NeedsYou
	}
	s.appendEvent(ctx, domain.OverviewLedger, domain.EventActorAiman, domain.EventOverviewFinished, payload)
	s.publishOverview(&snapshot, step)
}

func (s *Server) clearOverview(run *OverviewRun) {
	s.overviewMu.Lock()
	if s.overview.running == run {
		s.overview.running = nil
	}
	s.overviewMu.Unlock()
}

// publishOverview sends the hub event overview {id, status, step}: a signal
// to read the run again, not its state.
func (s *Server) publishOverview(run *OverviewRun, step string) {
	if s.hub != nil {
		s.hub.Publish(SessionEvent{Type: "overview", ID: run.ID, Status: run.State, Step: step})
	}
}

func (s *Server) saveOverview(base string, run *OverviewRun) {
	if err := os.MkdirAll(base, 0o700); err != nil {
		log.Printf("overview %s: %v", run.ID, err)
		return
	}
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(base, run.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("overview %s: save: %v", run.ID, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("overview %s: save: %v", run.ID, err)
	}
}

// loadOverviews reads the kept runs, newest first.
func loadOverviews(base string) []OverviewRun {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var runs []OverviewRun
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, e.Name())) //nolint:gosec // a file under the overview base
		if err != nil {
			continue
		}
		var r OverviewRun
		if json.Unmarshal(b, &r) == nil && r.ID != "" {
			runs = append(runs, r)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	return runs
}

// pruneOverviews keeps the newest overviewKeep finished runs.
func (s *Server) pruneOverviews(base string) {
	runs := loadOverviews(base)
	kept := 0
	for _, r := range runs {
		if r.State != OverviewDone && r.State != OverviewFailed {
			continue
		}
		kept++
		if kept > overviewKeep {
			_ = os.Remove(filepath.Join(base, r.ID+".json"))
		}
	}
}

// CleanStaleOverviews ends runs a previous serve left starting or running:
// their goroutine died with it. The step that was running fails with "serve
// restarted", the run's session is retired (found by its name when the run
// never recorded its id) and the run is recorded as failed. Serve calls it
// once at start.
func (s *Server) CleanStaleOverviews(ctx context.Context) {
	base := s.overviewBase()
	if base == "" {
		return
	}
	for _, r := range loadOverviews(base) {
		if r.State != OverviewStarting && r.State != OverviewRunning {
			continue
		}
		run := r
		var sess domain.Session
		if run.Session != "" {
			var ok bool
			if sess, ok = s.sessionByID(ctx, run.Session); !ok {
				sess = domain.Session{ID: run.Session}
			}
		} else {
			sess = s.unrecordedOverviewSession(ctx, run.ID)
		}
		s.finishOverview(ctx, base, &run, sess, OverviewFailed, "", nil, "serve restarted")
	}
}

// unrecordedOverviewSession is the stored overview-role session named
// overview-<run>, which a serve saved before the run recorded its id.
func (s *Server) unrecordedOverviewSession(ctx context.Context, runID string) domain.Session {
	list, err := s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}
	}
	for _, sess := range list {
		if sess.Role == domain.SessionRoleOverview && sess.Name == "overview-"+runID {
			return sess
		}
	}
	return domain.Session{}
}

// handleOverviewGet answers the named run, else the running one, else the
// newest done run, plus latest: the newest run of any state without its
// text. With summary it answers latest alone.
func (s *Server) handleOverviewGet(req Request) Response {
	var p struct {
		Run     string `json:"run"`
		Summary bool   `json:"summary"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	base := s.overviewBase()
	if base == "" {
		return errResp(req.ID, CodeUnavailable, "no home directory for overview runs")
	}
	s.overviewMu.Lock()
	var running *OverviewRun
	if s.overview.running != nil {
		r := s.overview.running.clone()
		running = &r
	}
	s.overviewMu.Unlock()
	runs := loadOverviews(base)
	latest := latestOverview(running, runs)
	if p.Summary {
		result := map[string]any{"type": "overview_run"}
		if latest != nil {
			result["latest"] = latest
		}
		return Response{ID: req.ID, Result: result}
	}
	var found *OverviewRun
	switch {
	case p.Run != "":
		if running != nil && running.ID == p.Run {
			found = running
		}
		for i := range runs {
			if found == nil && runs[i].ID == p.Run {
				found = &runs[i]
			}
		}
	case running != nil:
		found = running
	default:
		for i := range runs {
			if runs[i].State == OverviewDone {
				found = &runs[i]
				break
			}
		}
		if found == nil && len(runs) > 0 {
			found = &runs[0]
		}
	}
	if found == nil {
		return errResp(req.ID, CodeNotFound, "no overview yet: run aiman overview run")
	}
	result := map[string]any{"type": "overview_run", "run": found}
	if running != nil && found.ID != running.ID {
		result["running"] = running.ID
	}
	if latest != nil {
		result["latest"] = latest
	}
	return Response{ID: req.ID, Result: result}
}

// latestOverview is the newest run of any state, the running one first,
// without markdown and report; a run kept before needs_you was recorded
// gets it from its report.
func latestOverview(running *OverviewRun, runs []OverviewRun) *OverviewRun {
	var l OverviewRun
	switch {
	case running != nil:
		l = running.clone()
	case len(runs) > 0:
		l = runs[0].clone()
	default:
		return nil
	}
	if l.NeedsYou == nil && l.Report != nil {
		n := len(l.Report.NeedsYou)
		l.NeedsYou = &n
	}
	l.Markdown, l.Report = "", nil
	return &l
}

// handleOverviewList answers the kept runs, newest first, without their text.
func (s *Server) handleOverviewList(req Request) Response {
	base := s.overviewBase()
	runs := []OverviewRun{}
	if base != "" {
		for _, r := range loadOverviews(base) {
			r.Markdown, r.Report = "", nil
			runs = append(runs, r)
		}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "overview_runs", "runs": runs}}
}
