package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/redact"
	"github.com/bouwerp/aiman/internal/review"
)

// Harness call limits.
const (
	// HarnessUpWait is how long harness.up {wait:true} holds the call for the
	// run to leave starting; the CLI then polls harness.status.
	HarnessUpWait = 20 * time.Second
	// HarnessUpTimeout bounds harness.up: checks, the tree export and the wait.
	HarnessUpTimeout = 2 * time.Minute
	// HarnessCheckTimeout bounds one harness.check, every check included.
	HarnessCheckTimeout = 10 * time.Minute
	// HarnessDownTimeout bounds harness.down.
	HarnessDownTimeout = 2 * time.Minute
	// harnessNoteBytes caps the tester's note in a steering note.
	harnessNoteBytes = 1000
)

// HarnessDeps is what the harness methods need.
type HarnessDeps struct {
	Manager *harness.Manager
	// Repos is the repos section of serve's config: repos.<slug>.harness
	// overrides and the review image override.
	Repos map[string]config.RepoBootstrap
}

type harnessState struct {
	d HarnessDeps
}

// SetHarness enables the harness.* methods. Without it they answer
// unavailable.
func (s *Server) SetHarness(d HarnessDeps) {
	if d.Manager == nil {
		return
	}
	s.harness = &harnessState{d: d}
}

// HarnessRecorder writes the manager's ledger rows.
func (s *Server) HarnessRecorder() harness.Recorder {
	return func(id string, actor domain.EventActor, kind domain.EventKind, payload map[string]any) {
		s.appendEvent(context.Background(), id, actor, kind, payload)
	}
}

// dropHarness takes down a forgotten session's run.
func (s *Server) dropHarness(ctx context.Context, id string) {
	if s.harness != nil {
		s.harness.d.Manager.Drop(ctx, id)
	}
}

type harnessParams struct {
	Session  string `json:"session"`
	Run      string `json:"run"`
	Scenario string `json:"scenario"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	Lines    int    `json:"lines"`
	Step     *int   `json:"step"`
	Result   string `json:"result"`
	Note     string `json:"note"`
	Dirty    bool   `json:"dirty"`
	Debug    bool   `json:"debug"`
	Force    bool   `json:"force"`
	Wait     bool   `json:"wait"`
}

// dispatchHarness routes the harness.* methods.
func (s *Server) dispatchHarness(ctx context.Context, req Request) Response {
	if s.harness == nil {
		return errResp(req.ID, CodeUnavailable, "the harness is not enabled on this serve (harness.enabled, or no docker)")
	}
	var p harnessParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if strings.TrimSpace(p.Session) == "" {
		p.Session = req.Caller
	}
	switch req.Method {
	case "harness.up":
		return s.handleHarnessUp(ctx, req, p)
	case "harness.list":
		return s.handleHarnessList(ctx, req, p)
	case "harness.verdict":
		return s.handleHarnessVerdict(ctx, req, p)
	default:
		return s.handleHarnessRunCall(ctx, req, p)
	}
}

// harnessErr answers a manager error: a harness refusal keeps its code and
// data, a bad request is invalid_params, anything else fallback.
func harnessErr(id string, err error, fallback string) Response {
	var he *harness.Error
	switch {
	case errors.As(err, &he):
		r := errResp(id, he.Code, he.Error())
		if d := he.Data(); d != nil {
			r.Error.Data = d
		}
		return r
	case errors.Is(err, harness.ErrParams):
		return errResp(id, CodeInvalidParams, err.Error())
	default:
		return errResp(id, fallback, err.Error())
	}
}

// harnessSession resolves the session a call names and checks the caller's
// scope over it.
func (s *Server) harnessSession(ctx context.Context, req Request, name string) (domain.Session, *Response) {
	fail := func(code, msg string) (domain.Session, *Response) {
		r := errResp(req.ID, code, msg)
		return domain.Session{}, &r
	}
	if strings.TrimSpace(name) == "" {
		return fail(CodeInvalidParams, "session is required")
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return fail(CodeInvalidParams, err.Error())
	}
	sess, ok := resolveSession(list, name)
	if !ok {
		return fail(CodeNotFound, "session not found")
	}
	if r, refused := s.refuseOutOfScope(ctx, req, sess.ID); refused {
		return domain.Session{}, &r
	}
	return sess, nil
}

// harnessSource is where a session's runs come from: its checkout, the
// commit (a review's PR head, else HEAD) and the operator's overrides.
type harnessSource struct {
	repo           string
	sha            string
	skipCleanCheck bool
	override       []byte
	appImage       string
}

func (s *Server) harnessSourceFor(ctx context.Context, sess domain.Session) (harnessSource, error) {
	dir := sess.WorkingDirectory
	if dir == "" {
		dir = sess.WorktreePath
	}
	if dir == "" {
		return harnessSource{}, errors.New("session has no worktree")
	}
	top, err := gitLine(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return harnessSource{}, err
	}
	src := harnessSource{repo: top}
	if b, ok := config.LookupRepo(s.harness.d.Repos, sess.RepoName); ok {
		src.appImage = b.Review.Image
		if len(b.Harness) > 0 {
			if src.override, err = harness.OverrideBytes(b.Harness); err != nil {
				return harnessSource{}, err
			}
		}
	}
	if sess.ReviewPR != "" {
		// A review's worktree holds the PR staged on its base; the run is the
		// PR head, whatever the index says.
		st, err := review.LoadState(s.reviewBase(), sess.ID)
		if err != nil || st.HeadSHA == "" {
			return harnessSource{}, fmt.Errorf("read the review's PR head: %v", err)
		}
		src.sha, src.skipCleanCheck = st.HeadSHA, true
	}
	return src, nil
}

func (s *Server) harnessActor(ctx context.Context, req Request) domain.EventActor {
	if s.isSessionCall(ctx, req) {
		return domain.EventActorAgent
	}
	return domain.EventActorUser
}

func (s *Server) handleHarnessUp(ctx context.Context, req Request, p harnessParams) Response {
	if p.Debug {
		return errResp(req.ID, CodeInvalidParams, "debug runs are not available yet: the harness runs offline without a debugger")
	}
	if strings.TrimSpace(p.Scenario) == "" {
		return errResp(req.ID, CodeInvalidParams, "scenario is required")
	}
	sess, fail := s.harnessSession(ctx, req, p.Session)
	if fail != nil {
		return *fail
	}
	ctx, cancel := context.WithTimeout(ctx, HarnessUpTimeout)
	defer cancel()
	src, err := s.harnessSourceFor(ctx, sess)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	m := s.harness.d.Manager
	run, err := m.Up(ctx, harness.UpRequest{
		Session: sess.ID, Repo: src.repo, SHA: src.sha, Scenario: p.Scenario,
		Dirty: p.Dirty, SkipCleanCheck: src.skipCleanCheck,
		Override: src.override, AppImageOverride: src.appImage,
		Actor: s.harnessActor(ctx, req),
	})
	if err != nil {
		return harnessErr(req.ID, err, CodeCreateFailed)
	}
	if p.Wait {
		run = m.Wait(ctx, run.ID, HarnessUpWait)
		if run.State == harness.StateFailed && run.FailedService != "" {
			return harnessErr(req.ID, &harness.Error{
				Code: harness.CodeServiceFailed, Service: run.FailedService, Tail: run.Error,
				Msg: fmt.Sprintf("service %s of run %s did not become ready", run.FailedService, run.ID),
			}, CodeCreateFailed)
		}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "harness_run", "run": run}}
}

// handleHarnessList lists the scenarios of the session's harness at HEAD (or
// the review's PR head) and the session's runs. A missing or broken file is
// reported in file_error; the runs are still listed.
func (s *Server) handleHarnessList(ctx context.Context, req Request, p harnessParams) Response {
	sess, fail := s.harnessSession(ctx, req, p.Session)
	if fail != nil {
		return *fail
	}
	runs, err := s.harness.d.Manager.Runs(sess.ID)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if runs == nil {
		runs = []harness.Run{}
	}
	out := map[string]any{"type": "harness_list", "session": sess.ID, "runs": runs, "scenarios": []harness.Scenario{}}
	src, err := s.harnessSourceFor(ctx, sess)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sha := src.sha
	if sha == "" {
		sha = "HEAD"
	}
	loaded, err := harness.LoadAtSHA(ctx, src.repo, sha, src.override)
	if err != nil {
		var he *harness.Error
		fe := map[string]any{"code": CodeInvalidParams, "message": err.Error()}
		if errors.As(err, &he) {
			fe["code"] = he.Code
			if d := he.Data(); d != nil {
				fe["data"] = d
			}
		}
		out["file_error"] = fe
		return Response{ID: req.ID, Result: out}
	}
	out["source"], out["sha"], out["scenarios"] = loaded.Source, loaded.SHA, loaded.File.Scenarios
	return Response{ID: req.ID, Result: out}
}

// harnessRun finds the run a call names: run, else the session's live run,
// else (when kept is set) its newest run. The caller's scope must cover the
// run's session.
func (s *Server) harnessRun(ctx context.Context, req Request, p harnessParams, kept bool) (harness.Run, *Response) {
	m := s.harness.d.Manager
	var run harness.Run
	if strings.TrimSpace(p.Run) != "" {
		r, err := m.Get(strings.TrimSpace(p.Run))
		if err != nil {
			resp := harnessErr(req.ID, err, CodeInvalidParams)
			return run, &resp
		}
		if resp, refused := s.refuseOutOfScope(ctx, req, r.Session); refused {
			return run, &resp
		}
		return r, nil
	}
	sess, fail := s.harnessSession(ctx, req, p.Session)
	if fail != nil {
		return run, fail
	}
	if r, ok := m.LiveRun(sess.ID); ok {
		return r, nil
	}
	if kept {
		if runs, err := m.Runs(sess.ID); err == nil && len(runs) > 0 {
			return runs[0], nil
		}
	}
	resp := errResp(req.ID, harness.CodeNotRunning, "session "+sess.ID+" has no harness run")
	return run, &resp
}

// handleHarnessRunCall answers status, result, check, down and logs.
func (s *Server) handleHarnessRunCall(ctx context.Context, req Request, p harnessParams) Response {
	kept := req.Method == "harness.status" || req.Method == "harness.result" || req.Method == "harness.logs"
	run, fail := s.harnessRun(ctx, req, p, kept)
	if fail != nil {
		return *fail
	}
	m := s.harness.d.Manager
	m.Touch(run.ID)
	switch req.Method {
	case "harness.status":
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_run", "run": run}}
	case "harness.result":
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_result", "run": run}}
	case "harness.check":
		cctx, cancel := context.WithTimeout(ctx, HarnessCheckTimeout)
		defer cancel()
		checks, err := m.Check(cctx, run.ID, strings.TrimSpace(p.Name), s.harnessActor(ctx, req))
		if err != nil {
			return harnessErr(req.ID, err, CodeInvalidParams)
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_check", "run": run.ID, "checks": checks}}
	case "harness.down":
		dctx, cancel := context.WithTimeout(ctx, HarnessDownTimeout)
		defer cancel()
		if err := m.Down(dctx, run.ID, harness.StopDown); err != nil {
			return harnessErr(req.ID, err, CodeInvalidParams)
		}
		run, _ = m.Get(run.ID)
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_run", "run": run}}
	default:
		return s.harnessLogs(ctx, req, run, p)
	}
}

func (s *Server) harnessLogs(ctx context.Context, req Request, run harness.Run, p harnessParams) Response {
	if strings.TrimSpace(p.Service) == "" {
		return errResp(req.ID, CodeInvalidParams, "service is required")
	}
	n := p.Lines
	if n <= 0 {
		n = HostLogsDefaultLines
	}
	n = min(n, HostLogsMaxLines)
	lctx, cancel := context.WithTimeout(ctx, HostDaemonsTimeout)
	defer cancel()
	lines, err := s.harness.d.Manager.Logs(lctx, run.ID, p.Service, n)
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	lines, hits := redact.Lines(lines, nil)
	return Response{ID: req.ID, Result: map[string]any{
		"type": "harness_logs", "run": run.ID, "service": p.Service, "lines": capLogLines(lines), "redacted": hits,
	}}
}

// handleHarnessVerdict records the operator's verdict. A fail adds or updates
// the to-do for that step on the run's session and steers a note; a pass
// closes nothing.
func (s *Server) handleHarnessVerdict(ctx context.Context, req Request, p harnessParams) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator records a harness verdict")
	}
	run, fail := s.harnessRun(ctx, req, p, true)
	if fail != nil {
		return *fail
	}
	step := 0
	if p.Step != nil {
		step = *p.Step
	}
	v, run, err := s.harness.d.Manager.Verdict(run.ID, step, strings.TrimSpace(p.Result), p.Note, "operator")
	if err != nil {
		return harnessErr(req.ID, err, CodeInvalidParams)
	}
	out := map[string]any{"type": "harness_verdict", "verdict": v, "run": run}
	if v.Result == harness.ResultFail {
		id, err := s.harnessFailTodo(ctx, run, v)
		if err != nil {
			log.Printf("harness: to-do for run %s step %d: %v", run.ID, step, err)
		}
		out["todo"] = id
	}
	return Response{ID: req.ID, Result: out}
}

// harnessFailTodo opens (or rewrites) `Harness: <scenario> step <n>: <note>`
// with Ref harness:<scenario>:<n> on the run's session, and steers the
// tester's note to its agent. Step 0 is the scenario, Ref ...:all.
func (s *Server) harnessFailTodo(ctx context.Context, run harness.Run, v harness.Verdict) (string, error) {
	list, err := s.loadSessions(ctx)
	if err != nil {
		return "", err
	}
	sess, ok := resolveSession(list, run.Session)
	if !ok {
		return "", nil // the session is gone; the ledger keeps the verdict
	}
	first, _, _ := strings.Cut(v.Note, "\n")
	first = strings.TrimSpace(first)
	if first == "" {
		first = "failed"
	}
	ref, text := fmt.Sprintf("harness:%s:all", run.Scenario), fmt.Sprintf("Harness: %s: %s", run.Scenario, first)
	if v.Step > 0 {
		ref, text = fmt.Sprintf("harness:%s:%d", run.Scenario, v.Step), fmt.Sprintf("Harness: %s step %d: %s", run.Scenario, v.Step, first)
	}
	updated, err := s.MutateTodos(ctx, sess, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			it := &l.Items[i]
			if it.Ref == ref && (it.State == domain.TodoPending || it.State == domain.TodoActive) {
				it.Text = text
				return nil
			}
		}
		l.Items = append(l.Items, domain.TodoItem{Text: text, State: domain.TodoPending, Ref: ref})
		return nil
	})
	if err != nil {
		return "", err
	}
	id := ""
	for _, it := range updated.Items {
		if it.Ref == ref && (it.State == domain.TodoPending || it.State == domain.TodoActive) {
			id = it.ID
		}
	}
	if s.steer != nil {
		s.steer.Enqueue(SteerNote{SessionID: sess.ID, Author: domain.AuthorAiman, Text: harnessFailNote(run, v, id)})
	}
	return id, nil
}

func harnessFailNote(run harness.Run, v harness.Verdict, todoID string) string {
	what := "the scenario"
	if v.Step > 0 {
		what = fmt.Sprintf("step %d", v.Step)
	}
	tree := shortSHA(run.SHA)
	if run.Dirty {
		tree += " (dirty, diff " + shortSHA(run.DiffSHA256) + ")"
	}
	note := strings.TrimSpace(v.Note)
	if len(note) > harnessNoteBytes {
		note = note[:harnessNoteBytes] + "…"
	}
	if note == "" {
		note = "no note"
	}
	return fmt.Sprintf("[aiman] a tester marked harness scenario %s %s fail at %s: %s\nTo-do %s. Fix it, commit, and tell the operator the scenario is ready to run again.",
		run.Scenario, what, tree, note, todoID)
}
