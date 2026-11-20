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
}

// overviewState is serve's one running overview, if any.
type overviewState struct {
	running *OverviewRun
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
	run := &OverviewRun{ID: uuid.NewString()[:8], State: OverviewStarting, StartedAt: time.Now().UTC()}
	s.overview.running = run
	s.overviewMu.Unlock()

	pick, err := s.pickOverviewAgent(ctx, p.Agent, p.Model)
	if err != nil {
		s.clearOverview(run)
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	run.Agent, run.Model = pick.Agent, pick.Model
	sess, err := s.startOverview(ctx, base, run, pick)
	if err != nil {
		s.clearOverview(run)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	run.Session, run.State = sess.ID, OverviewRunning
	s.saveOverview(base, run)
	s.appendEvent(ctx, domain.OverviewLedger, domain.EventActorUser, domain.EventOverviewStarted, map[string]any{
		"run": run.ID, "session": sess.ID, "agent": run.Agent, "model": run.Model, "sessions": run.Sessions,
	})
	s.publishOverview(run)
	go s.watchOverview(base, run, sess) //nolint:gosec // G118: the run outlives the request that started it
	return Response{ID: req.ID, Result: map[string]any{"type": "overview_run", "run": run.public()}}
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

// startOverview writes the bundle and brief into a fresh repository under
// base and starts the hidden overview session there.
func (s *Server) startOverview(ctx context.Context, base string, run *OverviewRun, pick config.PreferredAgent) (domain.Session, error) {
	inputs, err := s.overviewInputs(ctx)
	if err != nil {
		return domain.Session{}, err
	}
	nonce := overview.NewNonce()
	bundle := overview.Build(inputs, nonce, nil)
	run.Sessions, run.NamesOnly = len(bundle.Sessions), bundle.NamesOnly
	dir := filepath.Join(base, run.ID)
	if err := prepareConsultDir(ctx, dir, overview.Brief(nonce, dir)); err != nil {
		_ = os.RemoveAll(dir)
		return domain.Session{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, overview.BundleFile), []byte(bundle.Text), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return domain.Session{}, err
	}
	runAgent := domain.Agent{Name: pick.Agent, Command: pick.Agent, LaunchModel: pick.Model, LaunchEffort: pick.Effort}
	if known, ok := agent.FindKnown(pick.Agent); ok {
		known.LaunchModel, known.LaunchEffort = pick.Model, pick.Effort
		runAgent = known
	}
	sess, err := s.startConsult(ctx, domain.SessionConfig{
		Name:             "overview-" + run.ID,
		Role:             domain.SessionRoleOverview,
		Tier:             config.TierSmart,
		Agent:            &runAgent,
		AdHoc:            true,
		PromptFree:       true,
		PreparedWorktree: dir,
		InitialPrompt: "Read " + domain.AimanTaskFileName + " and follow it. Read " + overview.BundleFile +
			" and write " + filepath.Join(dir, overview.ReportFile) + ", then stop.",
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return domain.Session{}, err
	}
	return sess, nil
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

// watchOverview waits for a valid report, an ended agent or the timeout, then
// retires the run's session.
func (s *Server) watchOverview(base string, run *OverviewRun, sess domain.Session) {
	ctx := context.Background()
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
	for range tick.C {
		if s.overviewStep(ctx, w, time.Now()) {
			return
		}
	}
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

// finishOverview records the outcome, retires the session and removes the
// run's scratch directory.
func (s *Server) finishOverview(ctx context.Context, base string, run *OverviewRun, sess domain.Session, state, md string, report *overview.Report, why string) {
	if err := (serverTerminal{s}).Stop(ctx, sess); err != nil {
		log.Printf("overview %s: stop: %v", run.ID, err)
	}
	if err := s.dropSession(ctx, sess, overviewEndedReason); err != nil {
		log.Printf("overview %s: forget: %v", run.ID, err)
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionEnded, map[string]any{"reason": overviewEndedReason})
	dir := filepath.Join(base, run.ID)
	if filepath.Dir(dir) == filepath.Clean(base) {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("overview %s: remove %s: %v", run.ID, dir, err)
		}
	}
	ended := time.Now().UTC()
	s.overviewMu.Lock()
	run.State, run.EndedAt, run.Error, run.Report = state, &ended, why, report
	if state == OverviewDone {
		run.Markdown = md
	}
	s.overview.running = nil
	snapshot := *run
	s.overviewMu.Unlock()
	s.saveOverview(base, &snapshot)
	s.pruneOverviews(base)
	payload := map[string]any{"run": run.ID, "state": state}
	if why != "" {
		payload["error"] = why
	}
	if report != nil {
		payload["needs_you"] = len(report.NeedsYou)
	}
	s.appendEvent(ctx, domain.OverviewLedger, domain.EventActorAiman, domain.EventOverviewFinished, payload)
	s.publishOverview(&snapshot)
}

func (s *Server) clearOverview(run *OverviewRun) {
	s.overviewMu.Lock()
	if s.overview.running == run {
		s.overview.running = nil
	}
	s.overviewMu.Unlock()
}

func (s *Server) publishOverview(run *OverviewRun) {
	if s.hub != nil {
		s.hub.Publish(SessionEvent{Type: "overview", ID: run.ID, Status: run.State})
	}
}

// public is the run as clients get it while it may still change.
func (r *OverviewRun) public() OverviewRun {
	return *r
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
// their watcher died with it. Each one's session is retired and the run is
// recorded as failed. Serve calls it once at start.
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
		sess, ok := s.sessionByID(ctx, run.Session)
		if !ok {
			sess = domain.Session{ID: run.Session}
		}
		if run.Session == "" {
			ended := time.Now().UTC()
			run.State, run.EndedAt, run.Error = OverviewFailed, &ended, "serve restarted"
			s.saveOverview(base, &run)
			continue
		}
		s.finishOverview(ctx, base, &run, sess, OverviewFailed, "", nil, "serve restarted")
	}
}

// handleOverviewGet answers the named run, else the running one, else the
// newest finished run.
func (s *Server) handleOverviewGet(req Request) Response {
	var p struct {
		Run string `json:"run"`
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
		r := *s.overview.running
		running = &r
	}
	s.overviewMu.Unlock()
	runs := loadOverviews(base)
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
	return Response{ID: req.ID, Result: result}
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
