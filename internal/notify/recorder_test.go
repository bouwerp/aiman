package notify

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type fakeStore struct {
	rows        []domain.Notification
	last        map[[2]string]time.Time
	appendCalls int
}

func (f *fakeStore) AppendIfDue(ctx context.Context, n domain.Notification, minInterval time.Duration) (domain.Notification, bool, error) {
	f.appendCalls++
	key := [2]string{n.SessionID, string(n.Kind)}
	if last, ok := f.last[key]; ok && n.At.Sub(last) < minInterval {
		return domain.Notification{}, false, nil
	}
	if f.last == nil {
		f.last = map[[2]string]time.Time{}
	}
	f.last[key] = n.At
	seq := int64(len(f.rows) + 1)
	n.Seq = seq
	f.rows = append(f.rows, n)
	return n, true, nil
}

func TestRecorderFirstObservationPrimes(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyWaitingInput: true}, MinInterval: time.Minute, Host: "h"}, nil)
	err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateIdle})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 0 {
		t.Fatalf("rows = %d", len(s.rows))
	}
}

func TestRecorderNotifiesOnTransition(t *testing.T) {
	s := &fakeStore{}
	var pub [][2]string
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyWaitingInput: true}, MinInterval: time.Minute, Host: "h"}, func(id string, k domain.NotifyKind) { pub = append(pub, [2]string{id, string(k)}) })
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateIdle}); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateWaitingInput, Confident: true, Question: "Continue?"}); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d", len(s.rows))
	}
	if s.rows[0].Text != "PROJ-1 on h: waiting for input · Continue?" {
		t.Fatalf("text = %q", s.rows[0].Text)
	}
	if len(pub) != 1 || pub[0] != [2]string{"s1", "waiting_input"} {
		t.Fatalf("pub = %#v", pub)
	}
}

func TestRecorderIgnoresLowConfidence(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyWaitingInput: true}, MinInterval: time.Minute, Host: "h"}, nil)
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateIdle}); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateWaitingInput, Confident: false, Question: "Continue?"}); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 0 {
		t.Fatalf("rows = %d", len(s.rows))
	}
}

func TestRecorderEndedWithoutConfidence(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyEnded: true}, MinInterval: time.Minute, Host: "h"}, nil)
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateIdle}); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", Ended: true}); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d", len(s.rows))
	}
	if s.rows[0].Kind != domain.NotifyEnded {
		t.Fatalf("kind = %s", s.rows[0].Kind)
	}
}

func TestRecorderRespectsEventsFilter(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{}, MinInterval: time.Minute, Host: "h"}, nil)
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateIdle}); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", Ended: true}); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 0 {
		t.Fatalf("rows = %d", len(s.rows))
	}
}

func TestRecorderEmitBypassesTransitions(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyCIEscalation: true}, MinInterval: time.Minute, Host: "h"}, nil)
	if err := r.Emit(context.Background(), "s1", "PROJ-1", domain.NotifyCIEscalation, "fail"); err != nil {
		t.Fatal(err)
	}
	if err := r.Emit(context.Background(), "s1", "PROJ-1", domain.NotifyCIEscalation, "fail"); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d", len(s.rows))
	}
}

func TestRecorderEmitHostEventSkipsMinInterval(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyAgentUpdate: true}, MinInterval: time.Hour, Host: "h"}, nil)
	for _, text := range []string{"codex 0.164.0 → 0.170.0 on h (press a on Agent API)", "claude 2.1.312 → 2.1.315 on h (press a on Agent API)"} {
		if err := r.Emit(context.Background(), "", "x", domain.NotifyAgentUpdate, text); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.rows) != 2 {
		t.Fatalf("rows = %d, want both host events", len(s.rows))
	}
	if s.rows[0].SessionID != "" || s.rows[0].Text != "codex 0.164.0 → 0.170.0 on h (press a on Agent API)" {
		t.Fatalf("row %+v", s.rows[0])
	}
}

func TestNilRecorderIsNoop(t *testing.T) {
	var r *Recorder
	if err := r.Emit(context.Background(), "s1", "x", domain.NotifyError, "d"); err != nil {
		t.Fatal(err)
	}
	if err := r.Observe(context.Background(), Observation{}); err != nil {
		t.Fatal(err)
	}
	r.Forget("s1")
}

func waitingInputRecorder(minInterval time.Duration) (*Recorder, *fakeStore) {
	s := &fakeStore{}
	return NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyWaitingInput: true}, MinInterval: minInterval, Host: "h"}, nil), s
}

func observeAll(t *testing.T, r *Recorder, obs ...Observation) {
	t.Helper()
	for _, o := range obs {
		o.SessionID, o.Label = "s1", "fix-login"
		if err := r.Observe(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecorderDeriveWaitingInputDetail(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  Observation
		want string
	}{
		{"hook message wins", Observation{State: domain.AgentStateWaitingInput, Confident: true, Question: "Allow Bash?", Reason: ReasonMenuOpen},
			"fix-login on h: waiting for input · Allow Bash?"},
		{"reason when the hook has none", Observation{State: domain.AgentStateWaitingInput, Confident: true, Reason: ReasonMenuOpen},
			"fix-login on h: waiting for input · a menu is open"},
		{"confirmation reason", Observation{State: domain.AgentStateWaitingInput, Confident: true, Reason: ReasonConfirmation},
			"fix-login on h: waiting for input · waiting for confirmation"},
		{"idle and asking", Observation{State: domain.AgentStateIdle, Confident: true, Asking: true},
			"fix-login on h: waiting for input · asked a question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := waitingInputRecorder(0)
			observeAll(t, r, Observation{State: domain.AgentStateWorking, Confident: true}, tc.obs)
			if len(s.rows) != 1 || s.rows[0].Kind != domain.NotifyWaitingInput {
				t.Fatalf("rows = %+v", s.rows)
			}
			if s.rows[0].Text != tc.want {
				t.Fatalf("text = %q, want %q", s.rows[0].Text, tc.want)
			}
		})
	}
}

func TestRecorderAskingNeedsConfidentIdle(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  Observation
	}{
		{"low confidence idle", Observation{State: domain.AgentStateIdle, Confident: false, Asking: true}},
		{"working", Observation{State: domain.AgentStateWorking, Confident: true, Asking: true}},
		{"unknown", Observation{State: domain.AgentStateUnknown, Confident: true, Asking: true}},
		{"idle, not asking", Observation{State: domain.AgentStateIdle, Confident: true, Reason: ReasonMenuOpen}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := waitingInputRecorder(0)
			observeAll(t, r, Observation{State: domain.AgentStateWorking, Confident: true}, tc.obs)
			if len(s.rows) != 0 {
				t.Fatalf("rows = %+v", s.rows)
			}
		})
	}
}

// An ended session reports ended even when its screen still shows a question,
// and carries no detail at all.
func TestRecorderEndedOutranksAsking(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyEnded: true, domain.NotifyWaitingInput: true}, Host: "h"}, nil)
	observeAll(t, r, Observation{State: domain.AgentStateWorking, Confident: true},
		Observation{State: domain.AgentStateIdle, Confident: true, Ended: true, Asking: true, Reason: ReasonAskedQuestion})
	if len(s.rows) != 1 || s.rows[0].Kind != domain.NotifyEnded || s.rows[0].Text != "fix-login on h: ended" {
		t.Fatalf("rows = %+v", s.rows)
	}
}

// Each question alerts once: an agent that goes back to work clears the kind,
// and the next question is news again.
func TestRecorderEachQuestionAlertsOnce(t *testing.T) {
	r, s := waitingInputRecorder(0)
	working := Observation{State: domain.AgentStateWorking, Confident: true}
	asked := Observation{State: domain.AgentStateIdle, Confident: true, Asking: true}
	observeAll(t, r, working, asked, asked, asked)
	if len(s.rows) != 1 {
		t.Fatalf("after the first question rows = %d, want 1", len(s.rows))
	}
	observeAll(t, r, working, asked, asked)
	if len(s.rows) != 2 {
		t.Fatalf("after the second question rows = %d, want 2", len(s.rows))
	}
}

func TestRecorderMinIntervalStillAppliesToQuestions(t *testing.T) {
	r, s := waitingInputRecorder(time.Hour)
	working := Observation{State: domain.AgentStateWorking, Confident: true}
	asked := Observation{State: domain.AgentStateIdle, Confident: true, Asking: true}
	observeAll(t, r, working, asked, working, asked)
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d, want 1 inside min_interval", len(s.rows))
	}
}

func TestRecorderFirstObservationOfAQuestionPrimes(t *testing.T) {
	r, s := waitingInputRecorder(0)
	observeAll(t, r, Observation{State: domain.AgentStateIdle, Confident: true, Asking: true})
	if len(s.rows) != 0 {
		t.Fatalf("rows = %+v", s.rows)
	}
}
