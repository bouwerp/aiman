package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usecase"
)

// steerGateServer is a serve instance over a real repo holding one session.
func steerGateServer(t *testing.T, sess domain.Session) *Server {
	t.Helper()
	repo := testRepo(t)
	if sess.Status == "" {
		sess.Status = domain.SessionStatusActive
	}
	if err := repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	return New(nil, repo, nil, nil, nil, nil, "test")
}

func hookedSession(id string, state domain.AgentState, source string, at time.Time) domain.Session {
	return domain.Session{
		ID: id, Name: id, Group: "g", TmuxSession: id, Backend: domain.BackendTmux,
		HookState: state, HookStateSource: source, HookStateAt: at,
	}
}

func TestSteerHoldsWhileEnded(t *testing.T) {
	sess := hookedSession("se1", domain.AgentStateIdle, agenthook.SourceLifecycle, time.Now())
	sess.AgentEnded = true
	srv := steerGateServer(t, sess)
	ready, held := srv.steerReady(context.Background(), "se1", time.Time{})
	if ready || held != "ended" {
		t.Fatalf("steerReady = (%v, %q), want (false, \"ended\")", ready, held)
	}
}

func TestSteerReadyOnFreshIdleHook(t *testing.T) {
	srv := steerGateServer(t, hookedSession("se2", domain.AgentStateIdle, agenthook.SourceLifecycle, time.Now()))
	ready, held := srv.steerReady(context.Background(), "se2", time.Now().Add(-time.Minute))
	if !ready {
		t.Fatalf("steerReady = (false, %q), want ready on a fresh idle report", held)
	}
}

// An idle report from before the last delivery describes the agent before it
// read that note. The gate waits for a report made after the note went in.
func TestSteerReadyRequiresFreshIdleAfterDelivery(t *testing.T) {
	now := time.Now()
	srv := steerGateServer(t, hookedSession("se3", domain.AgentStateIdle, agenthook.SourceLifecycle, now.Add(-10*time.Second)))
	if ready, _ := srv.steerReady(context.Background(), "se3", now.Add(-2*time.Second)); ready {
		t.Fatal("an idle report older than the last delivery must not open the gate")
	}
	idlePrompt := hookedSession("se3b", domain.AgentStateWaitingInput, agenthook.SourceIdlePrompt, now.Add(-10*time.Second))
	srv = steerGateServer(t, idlePrompt)
	if ready, _ := srv.steerReady(context.Background(), "se3b", now.Add(-2*time.Second)); ready {
		t.Fatal("an idle prompt older than the last delivery must not open the gate")
	}
}

func TestSteerHoldsOnDialog(t *testing.T) {
	srv := steerGateServer(t, hookedSession("se4", domain.AgentStateWaitingInput, agenthook.SourceLifecycle, time.Now()))
	ready, held := srv.steerReady(context.Background(), "se4", time.Time{})
	if ready || held != "dialog" {
		t.Fatalf("steerReady = (%v, %q), want (false, \"dialog\")", ready, held)
	}
}

func TestSteerHoldsWhileWorking(t *testing.T) {
	srv := steerGateServer(t, hookedSession("se4w", domain.AgentStateWorking, agenthook.SourceLifecycle, time.Now()))
	ready, held := srv.steerReady(context.Background(), "se4w", time.Time{})
	if ready || held != "working" {
		t.Fatalf("steerReady = (%v, %q), want (false, \"working\")", ready, held)
	}
}

func TestSteerReadyOnIdlePromptWaiting(t *testing.T) {
	srv := steerGateServer(t, hookedSession("se5", domain.AgentStateWaitingInput, agenthook.SourceIdlePrompt, time.Now()))
	ready, held := srv.steerReady(context.Background(), "se5", time.Now().Add(-time.Minute))
	if !ready {
		t.Fatalf("steerReady = (false, %q), want ready on an idle prompt", held)
	}
}

// Without a hook report or a pane to classify, readiness is unknown and the
// note is held.
func TestSteerHoldsWhenUnknown(t *testing.T) {
	srv := steerGateServer(t, domain.Session{ID: "se6", Name: "se6", Group: "g", Backend: domain.BackendPTY})
	ready, held := srv.steerReady(context.Background(), "se6", time.Time{})
	if ready || held != "unknown" {
		t.Fatalf("steerReady = (%v, %q), want (false, \"unknown\")", ready, held)
	}
	if ready, held := srv.steerReady(context.Background(), "missing", time.Time{}); ready || held != "unknown" {
		t.Fatalf("missing session: steerReady = (%v, %q), want (false, \"unknown\")", ready, held)
	}
}

// fastSteerSubmit shortens the PTY submit budget for one test.
func fastSteerSubmit(t *testing.T) {
	t.Helper()
	prev := steerSubmitTiming
	steerSubmitTiming = usecase.SubmitTiming{Initial: 50 * time.Millisecond, Settle: 50 * time.Millisecond, Attempts: 3}
	t.Cleanup(func() { steerSubmitTiming = prev })
}

// catSession runs `cat` under a real PTY runtime and registers it as a pty
// session on srv.
func catSession(t *testing.T, id string) *Server {
	t.Helper()
	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	t.Cleanup(mgr.CloseAll)
	if _, err := mgr.Create(ptyruntime.Spec{ID: id, Name: id, Command: "cat"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Kill(id) })
	srv := steerGateServer(t, domain.Session{ID: id, Name: id, Group: "g", Backend: domain.BackendPTY})
	srv.pty = mgr
	return srv
}

func TestDeliverSteerToPTY(t *testing.T) {
	fastSteerSubmit(t)
	srv := catSession(t, "st1")
	if _, err := srv.deliverSteer(context.Background(), "st1", "hello steer"); err != nil {
		t.Fatalf("deliverSteer: %v", err)
	}
	eventually(t, 5*time.Second, func() bool {
		screen, err := srv.pty.CaptureScreen("st1")
		return err == nil && strings.Contains(screen, "hello steer")
	})
}

// Serve's PTY submit uses the remote prompt path's proven budget, and its worst
// case still fits inside one steering operation.
func TestSteerSubmitBudget(t *testing.T) {
	if steerSubmitTiming != usecase.DefaultSubmitTiming() {
		t.Fatalf("steerSubmitTiming = %+v, want usecase.DefaultSubmitTiming() %+v", steerSubmitTiming, usecase.DefaultSubmitTiming())
	}
	worst := steerSubmitTiming.Initial + time.Duration(steerSubmitTiming.Attempts-1)*steerSubmitTiming.Settle
	if worst >= steerOpTimeout {
		t.Fatalf("worst-case submit %s does not fit steerOpTimeout %s", worst, steerOpTimeout)
	}
}

// An idle report that lands after Return but before deliverSteer finishes
// confirming is about the delivered note, so it opens the gate for the next.
func TestIdleReportDuringConfirmOpensGate(t *testing.T) {
	fastSteerSubmit(t)
	srv := catSession(t, "st3")
	submitAt, err := srv.deliverSteer(context.Background(), "st3", "first note")
	returned := time.Now()
	if err != nil {
		t.Fatalf("deliverSteer: %v", err)
	}
	if !submitAt.Before(returned.Add(-steerSubmitTiming.Settle)) {
		t.Fatalf("submitAt %v should precede the confirm loop that ended at %v", submitAt, returned)
	}
	sess, err := srv.repo.Get(context.Background(), "st3")
	if err != nil {
		t.Fatal(err)
	}
	sess.HookState = domain.AgentStateIdle
	sess.HookStateSource = agenthook.SourceLifecycle
	sess.HookStateAt = submitAt.Add(returned.Sub(submitAt) / 2)
	if err := srv.repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	if ready, held := srv.steerReady(context.Background(), "st3", submitAt); !ready {
		t.Fatalf("steerReady = (false, %q), want ready on an idle report made after the submit", held)
	}
}

func TestDeliverSteerToTmuxUsesLocalSendKeys(t *testing.T) {
	fastSteerSubmit(t)
	sess := domain.Session{ID: "st2", Name: "st2", Group: "g", TmuxSession: "work", Backend: domain.BackendTmux, Status: domain.SessionStatusActive}
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{}
	srv := New(nil, repo, remote, nil, nil, nil, "test")
	if _, err := srv.deliverSteer(context.Background(), "st2", "hello tmux"); err != nil {
		t.Fatalf("deliverSteer: %v", err)
	}
	remote.mu.Lock()
	execs := strings.Join(remote.execs, "\n")
	remote.mu.Unlock()
	if !strings.Contains(execs, `tmux send-keys -t "work"`) {
		t.Fatalf("want a local tmux send-keys to \"work\", got:\n%s", execs)
	}
	if !strings.Contains(execs, `"/tmp/aiman-prompt-st2-steer"`) {
		t.Fatalf("steering must use its own prompt file, got:\n%s", execs)
	}
	if strings.Contains(execs, "aiman pty input") {
		t.Fatalf("tmux delivery must not go through the pty CLI, got:\n%s", execs)
	}
}

// shellRemote runs `;` and `&&` the way a shell does, so a send-keys failure
// followed by `rm` is success when the script is written that way.
type shellRemote struct {
	fakeRemote
	failSend bool
	files    map[string][]byte
}

func (s *shellRemote) Execute(ctx context.Context, cmd string) (string, error) {
	_, _ = s.fakeRemote.Execute(ctx, cmd)
	var status error
	for _, part := range strings.Split(cmd, ";") {
		status = nil
		for _, step := range strings.Split(part, "&&") {
			if s.failSend && strings.Contains(step, "send-keys") {
				status = errors.New("can't find session")
				break
			}
		}
	}
	return "", status
}

func (s *shellRemote) WriteFile(_ context.Context, path string, content []byte) error {
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	s.files[path] = append([]byte(nil), content...)
	return nil
}

func TestDeliverSteerTmuxSendFailureStaysQueued(t *testing.T) {
	sess := domain.Session{ID: "stf", Name: "stf", Group: "g", TmuxSession: "work", Backend: domain.BackendTmux, Status: domain.SessionStatusActive}
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	remote := &shellRemote{failSend: true}
	srv := New(nil, repo, remote, nil, nil, nil, "test")
	if _, err := srv.deliverSteer(context.Background(), "stf", "hello tmux"); err == nil {
		t.Fatal("send-keys failed, but delivery reported success")
	}
}

func TestDeliverSteerFlattensRealNote(t *testing.T) {
	fastSteerSubmit(t)
	note := todo.FormatSteerNote(
		[]domain.TodoAuthor{domain.AuthorUser},
		[]todo.Op{{Kind: todo.OpAdd, ID: "t1", Text: "Ship"}},
		domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "Ship", State: domain.TodoPending}}},
	)
	if !strings.Contains(note, "\nCurrent list:\n") {
		t.Fatalf("fixture is not the multi-line note:\n%s", note)
	}

	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	t.Cleanup(mgr.CloseAll)
	if _, err := mgr.Create(ptyruntime.Spec{ID: "stn", Name: "stn", Command: "cat", Cols: 240, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Kill("stn") })
	srv := steerGateServer(t, domain.Session{ID: "stn", Name: "stn", Group: "g", Backend: domain.BackendPTY})
	srv.pty = mgr
	if _, err := srv.deliverSteer(context.Background(), "stn", note); err != nil {
		t.Fatalf("deliverSteer: %v", err)
	}
	var screen string
	eventually(t, 5*time.Second, func() bool {
		var err error
		screen, err = srv.pty.CaptureScreen("stn")
		return err == nil && strings.Contains(screen, "[aiman]") && strings.Contains(screen, "Adjust your work")
	})
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, "[aiman]") && !strings.Contains(line, "Adjust your work") {
			t.Fatalf("note submitted across lines:\n%s", screen)
		}
	}

	sess := domain.Session{ID: "stn2", Name: "stn2", Group: "g", TmuxSession: "work", Backend: domain.BackendTmux, Status: domain.SessionStatusActive}
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	remote := &shellRemote{}
	tmuxSrv := New(nil, repo, remote, nil, nil, nil, "test")
	if _, err := tmuxSrv.deliverSteer(context.Background(), "stn2", "ping\nfrom user"); err != nil {
		t.Fatalf("tmux deliver: %v", err)
	}
	var body []byte
	for _, b := range remote.files {
		body = b
	}
	if strings.Contains(string(body), "\n") {
		t.Fatalf("tmux prompt file still has a newline:\n%s", body)
	}
	if strings.Contains(string(body), "[aiman]") {
		t.Fatalf("free text gained the to-do wrapper: %s", body)
	}
	if string(body) != "ping from user" {
		t.Fatalf("typed %q, want one line", body)
	}
}

func TestSteerClassifierQuietUsesSettle(t *testing.T) {
	sess := domain.Session{ID: "cq1", Name: "cq1", Group: "g", TmuxSession: "work", Backend: domain.BackendTmux, Status: domain.SessionStatusActive}
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{pane: "bypass permissions on"}
	srv := New(nil, repo, remote, nil, nil, nil, "test")
	submit := time.Now().Add(-5 * time.Second)
	if ready, held := srv.steerReady(context.Background(), "cq1", submit); !ready {
		t.Fatalf("idle classifier with a 5s-old submit should be ready, held %q", held)
	}
	srv.noteSteerSettled("cq1")
	if ready, _ := srv.steerReady(context.Background(), "cq1", submit); ready {
		t.Fatal("confirm just finished; the classifier must hold the next note")
	}
}

func TestClassifierQuietStartsWhenConfirmFinishes(t *testing.T) {
	submit := time.Now().Add(-5 * time.Second)
	settled := time.Now()
	if !classifierQuietOpen(submit, time.Time{}, submit.Add(6*time.Second)) {
		t.Fatal("with no settle time, the quiet window is measured from submit")
	}
	if classifierQuietOpen(submit, settled, settled.Add(time.Second)) {
		t.Fatal("a note that arrived during confirm is eligible before the quiet window from when confirm finished")
	}
	if !classifierQuietOpen(submit, settled, settled.Add(steerClassifierQuiet+time.Millisecond)) {
		t.Fatal("quiet window should open once 3s have passed since confirm")
	}
	if !classifierQuietOpen(time.Time{}, time.Time{}, time.Now()) {
		t.Fatal("no prior delivery should not be held for quiet")
	}
}

func TestDeliverSteerTimeoutDoesNotTypeTwice(t *testing.T) {
	prev := steerSubmitTiming
	steerSubmitTiming = usecase.SubmitTiming{Initial: time.Second, Settle: 20 * time.Millisecond, Attempts: 2}
	t.Cleanup(func() { steerSubmitTiming = prev })
	srv := catSession(t, "stto")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := srv.deliverSteer(ctx, "stto", "once-only-note"); err == nil {
		t.Fatal("a canceled confirm counted as success")
	}
	if _, err := srv.deliverSteer(context.Background(), "stto", "once-only-note"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	screen, err := srv.pty.CaptureScreen("stto")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen, "once-only-noteonce-only-note") || !strings.Contains(screen, "once-only-note") {
		t.Fatalf("note was typed more than once:\n%s", screen)
	}
}

// recordingTarget is ready at once and records that the gate was asked.
type recordingTarget struct {
	fakeSteerTarget
	mu     sync.Mutex
	checks int
}

func (r *recordingTarget) steerReady(ctx context.Context, id string, last time.Time) (bool, string) {
	r.mu.Lock()
	r.checks++
	r.mu.Unlock()
	return r.fakeSteerTarget.steerReady(ctx, id, last)
}

func (r *recordingTarget) checked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checks
}

func TestReportKicksSteerer(t *testing.T) {
	srv := steerGateServer(t, hookedSession("sk1", domain.AgentStateWorking, agenthook.SourceLifecycle, time.Now()))
	store := &fakeSteerStore{recs: []domain.SteerRecord{{ID: 1, SessionID: "sk1", Author: domain.AuthorUser, Text: "held note", CreatedAt: time.Now()}}, nextID: 1}
	target := &recordingTarget{}
	target.setReady(true, "")
	st := newSteerer(store, target, nil, testSteerWindow)
	if err := st.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.steer = st
	runSteerer(t, st)

	params, _ := json.Marshal(map[string]any{"id": "sk1", "state": "idle", "source": agenthook.SourceLifecycle})
	resp := srv.handleReportAgentSession(context.Background(), Request{ID: "1", Method: "session.report_agent", Params: params})
	if resp.Error != nil {
		t.Fatalf("report: %v", resp.Error)
	}
	eventually(t, 2*time.Second, func() bool { return target.checked() > 0 && len(target.deliveries()) == 1 })
	if got := target.deliveries()[0]; got != "held note" {
		t.Fatalf("delivered %q, want the held note", got)
	}
}

// Forget drops the session's steering queue along with the row.
func TestForgetDropsSteerQueue(t *testing.T) {
	srv := steerGateServer(t, domain.Session{ID: "sf1", Name: "sf1", Group: "g", Backend: domain.BackendTmux})
	store := &fakeSteerStore{}
	srv.steer = newSteerer(store, &fakeSteerTarget{}, nil, testSteerWindow)
	params, _ := json.Marshal(map[string]any{"id": "sf1"})
	resp := srv.handleForget(context.Background(), Request{ID: "1", Method: "session.forget", Params: params})
	if resp.Error != nil {
		t.Fatalf("forget: %v", resp.Error)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleted) != 1 || store.deleted[0] != "sf1" {
		t.Fatalf("deleted = %v, want [sf1]", store.deleted)
	}
}
