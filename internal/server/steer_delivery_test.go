package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
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
