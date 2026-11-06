package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

const (
	realMenu       = "Do you want to proceed?\n❯ 1. Yes\n  2. No\n\n Esc to cancel"
	composerText   = "● ok\n────\n❯ 1. foo\n────\n  -- INSERT --"
	blockquote     = "● here is the list:\n> 1. item\n  2. item\n"
	askedScreen    = "● secret-token-in-transcript\n● Should I migrate now or keep the shim?\n\n✻ Brewed for 42s · done 13:45\n\n────\n❯ \n────\n  -- INSERT --"
	statementFrame = "● secret-token-in-transcript\n● All done.\n\n✻ Brewed for 42s · done 13:45\n\n────\n❯ \n────\n  -- INSERT --"
)

// sweepRig is a serve over a real store and a fake PTY runtime. Every session
// it creates has a holder.
type sweepRig struct {
	srv   *Server
	repo  *sqlite.Repository
	pty   *ptyfake.FakePTY
	store *notifyStore
}

func newSweepRig(t *testing.T) *sweepRig {
	t.Helper()
	repo := logRepo(t)
	pty := ptyfake.New()
	srv := New(nil, repo, &fakeRemote{}, nil, nil, pty, "test")
	store := &notifyStore{}
	srv.EnableNotifications(notify.NewRecorder(store, notify.Settings{
		Events: map[domain.NotifyKind]bool{domain.NotifyWaitingInput: true, domain.NotifyEnded: true},
		Host:   "h",
	}, nil), nil)
	return &sweepRig{srv: srv, repo: repo, pty: pty, store: store}
}

// session stores a session with a fresh lifecycle hook report of state, or no
// report at all when state is empty, showing screen.
func (r *sweepRig) session(t *testing.T, id string, state domain.AgentState, screen string, opts ...func(*domain.Session)) domain.Session {
	t.Helper()
	sess := domain.Session{
		ID: id, Name: id, Group: "g", TerminalName: id, Backend: domain.BackendPTY,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}
	if state != "" {
		sess.HookState, sess.HookStateSource, sess.HookStateAt = state, agenthook.SourceLifecycle, time.Now()
	}
	for _, o := range opts {
		o(&sess)
	}
	if err := r.repo.Save(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	r.pty.Add(ptyruntime.SessionInfo{ID: id, Name: id, Status: ptyruntime.StatusRunning})
	r.pty.SetScreen(id, screen)
	return sess
}

// report applies a hook report, as the session's agent would.
func (r *sweepRig) report(t *testing.T, id string, state domain.AgentState) {
	t.Helper()
	resp := rpc(t, r.srv, "", "session.report_agent_session", map[string]any{
		"id": id, "state": string(state), "source": agenthook.SourceLifecycle,
	})
	if resp.Error != nil {
		t.Fatalf("report: %+v", resp.Error)
	}
}

func (r *sweepRig) uncertainEvents(t *testing.T, id string) int {
	t.Helper()
	events, err := r.repo.ListSessionEvents(context.Background(), id, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Kind == domain.EventPaneUncertain {
			n++
		}
	}
	return n
}

func TestClassifySituations(t *testing.T) {
	ended := func(s *domain.Session) { s.AgentEnded = true }
	for _, tc := range []struct {
		name     string
		hook     domain.AgentState
		screen   string
		opts     []func(*domain.Session)
		want     domain.AgentState
		wantConf pane.Confidence
	}{
		{name: "working hook, real menu", hook: domain.AgentStateWorking, screen: realMenu, want: domain.AgentStateWaitingInput, wantConf: pane.High},
		{name: "working hook, composer text", hook: domain.AgentStateWorking, screen: composerText, want: domain.AgentStateWorking, wantConf: pane.High},
		{name: "working hook, blockquote", hook: domain.AgentStateWorking, screen: blockquote, want: domain.AgentStateWorking, wantConf: pane.High},
		{name: "idle hook, composer text", hook: domain.AgentStateIdle, screen: composerText, want: domain.AgentStateIdle, wantConf: pane.High},
		{name: "idle hook, real menu is never overruled", hook: domain.AgentStateIdle, screen: realMenu, want: domain.AgentStateIdle, wantConf: pane.High},
		{name: "idle hook, a question stays idle", hook: domain.AgentStateIdle, screen: askedScreen, want: domain.AgentStateIdle, wantConf: pane.High},
		{name: "ended answers first", hook: domain.AgentStateWorking, screen: realMenu, opts: []func(*domain.Session){ended}, want: domain.AgentStateIdle, wantConf: pane.High},
		{name: "no hook, composer text keeps the rules' answer", screen: composerText, want: domain.AgentStateWaitingInput, wantConf: pane.High},
		{name: "no hook, real menu", screen: realMenu, want: domain.AgentStateWaitingInput, wantConf: pane.High},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSweepRig(t)
			sess := r.session(t, "s1", tc.hook, tc.screen, tc.opts...)
			got, conf := r.srv.classify(context.Background(), sess)
			if got != tc.want || conf != tc.wantConf {
				t.Fatalf("classify = %s/%v, want %s/%v", got, conf, tc.want, tc.wantConf)
			}
		})
	}
}

// With no screen to read the fresh hook still answers.
// Most turns outlast the hook's TTL, so a menu in a long turn has only a stale
// working report above it, and the menu can sit above the rules' six-row tail.
func TestClassifyFindsAMenuPastTheHookTTL(t *testing.T) {
	const longMenu = "Which library?\n❯ 1. A\n     a\n  2. B\n     b\n  3. C\n     c\n  4. D\n     d\n Enter to select"
	stale := func(s *domain.Session) { s.HookStateAt = time.Now().Add(-time.Hour) }

	r := newSweepRig(t)
	sess := r.session(t, "open", domain.AgentStateWorking, longMenu, stale)
	if st, conf := r.srv.classify(context.Background(), sess); st != domain.AgentStateWaitingInput || conf != pane.High {
		t.Fatalf("open turn: classify = %s/%v, want waiting_input/high", st, conf)
	}

	// With no turn open the same screen is text, and the rules alone decide.
	idle := r.session(t, "closed", domain.AgentStateIdle, longMenu, stale)
	if st, _ := r.srv.classify(context.Background(), idle); st == domain.AgentStateWaitingInput {
		t.Fatalf("closed turn: classify = %s, want not waiting_input", st)
	}
}

func TestClassifyWorkingHookWithoutACapture(t *testing.T) {
	r := newSweepRig(t)
	sess := r.session(t, "s1", domain.AgentStateWorking, realMenu)
	r.srv.pty = nil
	if got, conf := r.srv.classify(context.Background(), sess); got != domain.AgentStateWorking || conf != pane.High {
		t.Fatalf("classify = %s/%v", got, conf)
	}
}

func TestSessionPromptAcceptedOnIdleSessionEndingInAQuestion(t *testing.T) {
	r := newSweepRig(t)
	r.session(t, "s1", domain.AgentStateIdle, askedScreen)
	r.srv.sweepStates(context.Background())
	resp := rpc(t, r.srv, "", "session.prompt", map[string]any{"id": "s1", "text": "migrate now"})
	if resp.Error != nil {
		t.Fatalf("prompt refused: %+v", resp.Error)
	}
}

func TestSessionPromptRefusedOnMaskedMenu(t *testing.T) {
	r := newSweepRig(t)
	r.session(t, "s1", domain.AgentStateWorking, realMenu)
	resp := rpc(t, r.srv, "", "session.prompt", map[string]any{"id": "s1", "text": "hello"})
	if resp.Error == nil || resp.Error.Code != CodeAgentBlocked {
		t.Fatalf("error = %+v, want %s", resp.Error, CodeAgentBlocked)
	}
}

func TestSessionListAndGetSeeTheMaskedMenu(t *testing.T) {
	r := newSweepRig(t)
	r.session(t, "s1", domain.AgentStateWorking, realMenu)
	var got struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	rpcOK(t, r.srv, "", "session.list", map[string]any{}, &got)
	if len(got.Sessions) != 1 || got.Sessions[0].State != string(domain.AgentStateWaitingInput) || got.Sessions[0].StateConfidence != "high" {
		t.Fatalf("list = %+v", got.Sessions)
	}
}

func TestSweepNotifiesOnAQuestionWithoutTerminalText(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	r.session(t, "s1", domain.AgentStateWorking, "● thinking")
	r.srv.sweepStates(ctx)
	r.report(t, "s1", domain.AgentStateIdle)
	r.pty.SetScreen("s1", askedScreen)
	r.srv.sweepStates(ctx)
	r.srv.sweepStates(ctx)

	if len(r.store.rows) != 1 {
		t.Fatalf("rows = %+v", r.store.rows)
	}
	if want := "s1 on h: waiting for input · asked a question"; r.store.rows[0].Text != want {
		t.Fatalf("text = %q, want %q", r.store.rows[0].Text, want)
	}
	if st, ok := r.srv.cachedSessionState("s1"); !ok || st.State != string(domain.AgentStateIdle) {
		t.Fatalf("a question must leave the state idle: %+v", st)
	}
}

// Every situation that notifies, checked for what the transcript holds.
func TestNotificationsNeverCarryTerminalText(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hook       domain.AgentState
		screen     string
		wantDetail string
	}{
		{"masked menu", domain.AgentStateWorking, "● secret-token-in-transcript\n" + realMenu, "a menu is open"},
		{"question", domain.AgentStateIdle, askedScreen, "asked a question"},
		{"rules menu", "", "● secret-token-in-transcript\n" + realMenu, "a menu is open"},
		{"rules confirmation", "", "secret-token-in-transcript\nDelete it? [y/N]", "waiting for confirmation"},
		{"rules blocking prompt", "", "secret-token-in-transcript\nPassword:", "waiting for confirmation"},
		{"rules question", "", "secret-token-in-transcript\nShould I delete it?", "asked a question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newSweepRig(t)
			ctx := context.Background()
			r.session(t, "s1", tc.hook, "● thinking")
			if tc.hook == "" {
				r.pty.SetScreen("s1", "● thinking (12s · esc to interrupt)")
			}
			r.srv.sweepStates(ctx)
			r.pty.SetScreen("s1", tc.screen)
			r.srv.sweepStates(ctx)
			if len(r.store.rows) != 1 {
				t.Fatalf("rows = %+v", r.store.rows)
			}
			text := r.store.rows[0].Text
			if strings.Contains(text, "secret") || !strings.HasSuffix(text, "· "+tc.wantDetail) {
				t.Fatalf("text = %q, want the detail %q and nothing from the screen", text, tc.wantDetail)
			}
		})
	}
}

func TestEndedNotificationCarriesNoTerminalText(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	r.session(t, "s1", domain.AgentStateIdle, statementFrame)
	r.srv.sweepStates(ctx)
	resp := rpc(t, r.srv, "", "session.report_agent_session", map[string]any{"id": "s1", "ended": true})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if len(r.store.rows) != 1 || r.store.rows[0].Text != "s1 on h: ended" {
		t.Fatalf("rows = %+v", r.store.rows)
	}
}

// The hook's own message is the one piece of agent text a notification carries,
// and it belongs to the report that said waiting_input.
func TestHookMessageIsTheDetail(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	r.session(t, "s1", domain.AgentStateWorking, "● thinking")
	r.srv.sweepStates(ctx)
	resp := rpc(t, r.srv, "", "session.report_agent_session", map[string]any{
		"id": "s1", "state": "waiting_input", "source": agenthook.SourceLifecycle, "message": "Claude needs your permission to use Bash",
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if want := "s1 on h: waiting for input · Claude needs your permission to use Bash"; len(r.store.rows) != 1 || r.store.rows[0].Text != want {
		t.Fatalf("rows = %+v", r.store.rows)
	}
}

func TestStaleHookMessageIsNotTheMenuDetail(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	r.session(t, "s1", domain.AgentStateWorking, "● thinking", func(s *domain.Session) { s.HookStateMessage = "left over" })
	r.srv.sweepStates(ctx)
	r.pty.SetScreen("s1", realMenu)
	r.srv.sweepStates(ctx)
	if len(r.store.rows) != 1 || !strings.HasSuffix(r.store.rows[0].Text, "· a menu is open") {
		t.Fatalf("rows = %+v", r.store.rows)
	}
}

func TestHiddenSessionProducesNoNotificationAndNoState(t *testing.T) {
	for _, role := range []domain.SessionRole{domain.SessionRoleReviewer, domain.SessionRoleHandover, domain.SessionRoleConsult} {
		t.Run(string(role), func(t *testing.T) {
			r := newSweepRig(t)
			ctx := context.Background()
			hidden := func(s *domain.Session) { s.Role = role }
			r.session(t, "h1", domain.AgentStateWorking, "● thinking", hidden)
			r.srv.sweepStates(ctx)
			r.pty.SetScreen("h1", realMenu)
			r.srv.sweepStates(ctx)
			r.report(t, "h1", domain.AgentStateIdle)
			r.pty.SetScreen("h1", askedScreen)
			r.srv.sweepStates(ctx)
			r.report(t, "h1", domain.AgentStateWaitingInput)
			if len(r.store.rows) != 0 {
				t.Fatalf("rows = %+v", r.store.rows)
			}
			if st, ok := r.srv.cachedSessionState("h1"); ok {
				t.Fatalf("hidden session cached %+v", st)
			}
		})
	}
}

// A helper's agent reports before its creator saves a row; only the holder
// knows its role then.
func TestHiddenRoleFromTheHolderIsSkipped(t *testing.T) {
	r := newSweepRig(t)
	r.pty.Add(ptyruntime.SessionInfo{ID: "c1", Name: "c1", Status: ptyruntime.StatusRunning, Role: string(domain.SessionRoleConsult)})
	r.pty.SetScreen("c1", realMenu)
	ctx := context.Background()
	r.srv.sweepStates(ctx)
	r.report(t, "c1", domain.AgentStateWorking)
	r.srv.sweepStates(ctx)
	if len(r.store.rows) != 0 {
		t.Fatalf("rows = %+v", r.store.rows)
	}
	if st, ok := r.srv.cachedSessionState("c1"); ok {
		t.Fatalf("hidden session cached %+v", st)
	}
}

// A helper's row can be stored before its role: the report path reads that row,
// and only the holder knows better.
func TestHookReportSkipsARoleOnlyTheHolderKnows(t *testing.T) {
	r := newSweepRig(t)
	r.session(t, "c1", domain.AgentStateWorking, "● thinking")
	r.pty.Add(ptyruntime.SessionInfo{ID: "c1", Name: "c1", Status: ptyruntime.StatusRunning, Role: string(domain.SessionRoleConsult)})
	r.report(t, "c1", domain.AgentStateWorking)
	r.pty.SetScreen("c1", realMenu)
	r.report(t, "c1", domain.AgentStateWorking)
	if len(r.store.rows) != 0 {
		t.Fatalf("rows = %+v", r.store.rows)
	}
	if st, ok := r.srv.cachedSessionState("c1"); ok {
		t.Fatalf("hidden session cached %+v", st)
	}
}

func TestSweepWritesNoUncertainPane(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	sess := r.session(t, "s1", "", "reading the repository\nnothing recognisable")
	r.pty.Add(ptyruntime.SessionInfo{ID: "s1", Name: "s1", Status: ptyruntime.StatusRunning, LastOutput: time.Now()})
	for i := range 3 {
		r.pty.SetScreen("s1", "reading the repository\nnothing recognisable "+strings.Repeat("x", i))
		r.srv.sweepStates(ctx)
	}
	if got := r.uncertainEvents(t, "s1"); got != 0 {
		t.Fatalf("sweep wrote %d pane.uncertain rows", got)
	}
	st, ok := r.srv.cachedSessionState("s1")
	if !ok || st.State != string(domain.AgentStateUnknown) || st.Confidence != "low" {
		t.Fatalf("cached = %+v", st)
	}
	if _, conf := r.srv.classify(ctx, sess); conf != pane.Low {
		t.Fatalf("classify confidence = %v", conf)
	}
	if got := r.uncertainEvents(t, "s1"); got != 1 {
		t.Fatalf("classify wrote %d pane.uncertain rows, want 1", got)
	}
}

// The hook path samples too, and must leave the ledger's pane.uncertain to
// classify.
func TestHookReportWritesNoUncertainPane(t *testing.T) {
	r := newSweepRig(t)
	r.session(t, "s1", "", "reading\nnothing recognisable")
	r.pty.Add(ptyruntime.SessionInfo{ID: "s1", Name: "s1", Status: ptyruntime.StatusRunning, LastOutput: time.Now()})
	resp := rpc(t, r.srv, "", "session.report_agent_session", map[string]any{"id": "s1", "title": "busy"})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if got := r.uncertainEvents(t, "s1"); got != 0 {
		t.Fatalf("hook path wrote %d pane.uncertain rows", got)
	}
}

func TestSweepCachesStateWithoutARecorder(t *testing.T) {
	r := newSweepRig(t)
	r.srv.EnableNotifications(nil, nil)
	r.session(t, "s1", domain.AgentStateWorking, realMenu)
	r.srv.sweepStates(context.Background())
	st, ok := r.srv.cachedSessionState("s1")
	if !ok || st.State != string(domain.AgentStateWaitingInput) || st.Confidence != "high" {
		t.Fatalf("cached = %+v, %v", st, ok)
	}
}

func TestSweepDropsStateOfSessionsThatGoAway(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	sess := r.session(t, "s1", domain.AgentStateIdle, statementFrame)
	r.srv.sweepStates(ctx)
	if _, ok := r.srv.cachedSessionState("s1"); !ok {
		t.Fatal("not cached")
	}
	sess.Status = domain.SessionStatusInactive
	if err := r.repo.Save(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if err := r.pty.Forget("s1"); err != nil {
		t.Fatal(err)
	}
	r.srv.sweepStates(ctx)
	if st, ok := r.srv.cachedSessionState("s1"); ok {
		t.Fatalf("state kept for a session that went away: %+v", st)
	}
}

func TestForgetDropsCachedState(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	sess := r.session(t, "s1", domain.AgentStateIdle, statementFrame)
	r.srv.sweepStates(ctx)
	if err := r.srv.dropSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if st, ok := r.srv.cachedSessionState("s1"); ok {
		t.Fatalf("state kept after forget: %+v", st)
	}
}

// serve caches state whether or not notifications are configured.
func TestServeSweepsWithoutNotifications(t *testing.T) {
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", Group: "g", TerminalName: "s1", Backend: domain.BackendPTY,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
		HookState: domain.AgentStateIdle, HookStateSource: agenthook.SourceLifecycle, HookStateAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	srv, _ := startEventsServer(t, repo)
	eventually(t, 3*notifySweepEvery, func() bool {
		st, ok := srv.cachedSessionState("s1")
		return ok && st.State == string(domain.AgentStateIdle)
	})
}

func TestSessionActivityCarriesCachedState(t *testing.T) {
	l := &fakeLister{sessions: []ptyruntime.SessionInfo{
		{ID: "a", Status: ptyruntime.StatusRunning},
		{ID: "b", Status: ptyruntime.StatusRunning},
	}}
	cached := map[string]cachedState{"a": {State: "working", Confidence: "high"}}
	read := func(id string) (cachedState, bool) { st, ok := cached[id]; return st, ok }
	seen := map[string]SessionEvent{}

	first := collectSessionEvents(l, seen, read)
	if len(first) != 2 {
		t.Fatalf("first = %+v", first)
	}
	byID := map[string]SessionEvent{first[0].ID: first[0], first[1].ID: first[1]}
	if byID["a"].State != "working" || byID["a"].StateConfidence != "high" {
		t.Fatalf("a = %+v", byID["a"])
	}
	if byID["b"].State != "" || byID["b"].StateConfidence != "" {
		t.Fatalf("b has no cached state and must send none: %+v", byID["b"])
	}

	if again := collectSessionEvents(l, seen, read); len(again) != 0 {
		t.Fatalf("unchanged state re-reported: %+v", again)
	}

	cached["a"] = cachedState{State: "waiting_input", Confidence: "high"}
	changed := collectSessionEvents(l, seen, read)
	if len(changed) != 1 || changed[0].ID != "a" || changed[0].State != "waiting_input" {
		t.Fatalf("changed = %+v", changed)
	}
}

// The wire names are the API contract.
func TestSessionActivityWireFormat(t *testing.T) {
	b, err := json.Marshal(SessionEvent{Type: "session_activity", ID: "a", State: "idle", StateConfidence: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"state":"idle"`) || !strings.Contains(string(b), `"state_confidence":"high"`) {
		t.Fatalf("event = %s", b)
	}
	b, err = json.Marshal(SessionEvent{Type: "session_activity", ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "state") {
		t.Fatalf("an event with no state must omit it: %s", b)
	}
}

// A question arriving changes Asking and Reason, which stay off the event, so
// nothing is sent for a session whose state is unchanged.
func TestAQuestionFiresNoExtraActivityEvent(t *testing.T) {
	r := newSweepRig(t)
	ctx := context.Background()
	r.session(t, "s1", domain.AgentStateIdle, statementFrame)
	r.srv.sweepStates(ctx)
	seen := map[string]SessionEvent{}
	collectSessionEvents(r.pty, seen, r.srv.cachedSessionState)

	r.pty.SetScreen("s1", askedScreen)
	r.srv.sweepStates(ctx)
	if ev := collectSessionEvents(r.pty, seen, r.srv.cachedSessionState); len(ev) != 0 {
		t.Fatalf("events = %+v", ev)
	}
}

// The stream reads what the sweep stored and never captures a screen itself.
func TestCollectSessionEventsNeverClassifies(t *testing.T) {
	r := newSweepRig(t)
	r.session(t, "s1", domain.AgentStateWorking, realMenu)
	seen := map[string]SessionEvent{}
	ev := collectSessionEvents(r.pty, seen, r.srv.cachedSessionState)
	if len(ev) != 1 || ev[0].State != "" {
		t.Fatalf("no sweep has run, so no state: %+v", ev)
	}
}
