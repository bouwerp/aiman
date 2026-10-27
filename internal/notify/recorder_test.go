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
	lines := 0
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", Ended: true, LastLine: func() string { lines++; return "done" }}); err != nil {
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
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", Ended: true, LastLine: func() string { return "done" }}); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 0 {
		t.Fatalf("rows = %d", len(s.rows))
	}
}

func TestRecorderLastLineOnlyWhenNotifying(t *testing.T) {
	s := &fakeStore{}
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyWaitingInput: true}, MinInterval: time.Minute, Host: "h"}, nil)
	if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateIdle}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for i := 0; i < 5; i++ {
		if err := r.Observe(context.Background(), Observation{SessionID: "s1", Label: "PROJ-1", State: domain.AgentStateWorking, LastLine: func() string { calls++; return "x" }}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatalf("LastLine calls = %d", calls)
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
