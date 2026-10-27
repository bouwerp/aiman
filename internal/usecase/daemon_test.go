package usecase

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type daemonRepo struct {
	domain.SessionRepository
	sessions map[string]domain.Session
	prompts  []domain.ScheduledPrompt
	saved    []domain.ScheduledPrompt
}

func (r *daemonRepo) List(context.Context) ([]domain.Session, error) {
	out := make([]domain.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out, nil
}

func (r *daemonRepo) Get(_ context.Context, id string) (*domain.Session, error) {
	s, ok := r.sessions[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &s, nil
}

func (r *daemonRepo) ListScheduledPrompts(context.Context) ([]domain.ScheduledPrompt, error) {
	return r.prompts, nil
}

func (r *daemonRepo) SaveScheduledPrompt(_ context.Context, sp *domain.ScheduledPrompt) error {
	r.saved = append(r.saved, *sp)
	return nil
}

type sentPrompt struct {
	session, text string
	mode          SendMode
}

type recordingSender struct {
	sent      []sentPrompt
	err       error
	delivered bool
}

func (s *recordingSender) SendPrompt(_ context.Context, sessionID, text string, mode SendMode) (bool, error) {
	s.sent = append(s.sent, sentPrompt{session: sessionID, text: text, mode: mode})
	return s.delivered, s.err
}

func TestDueForPollHonoursFrequency(t *testing.T) {
	d := NewDaemon(&config.Config{}, &daemonRepo{}, nil, &recordingSender{})
	t0 := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	steps := []struct {
		rule string
		freq int
		at   time.Duration
		want bool
	}{
		{"r120", 120, 0, true},
		{"r120", 120, 60 * time.Second, false},
		{"r120", 120, 120 * time.Second, true},
		{"r0", 0, 0, true},
		{"r0", 0, 299 * time.Second, false},
		{"r0", 0, 300 * time.Second, true},
	}
	for _, st := range steps {
		if got := d.dueForPoll(st.rule, st.freq, t0.Add(st.at)); got != st.want {
			t.Errorf("dueForPoll(%s, %d, t0+%s) = %v, want %v", st.rule, st.freq, st.at, got, st.want)
		}
	}
}

func scheduledPromptRepo() *daemonRepo {
	return &daemonRepo{
		sessions: map[string]domain.Session{
			"a": {ID: "a", TmuxSession: "aiman-a"},
			"b": {ID: "b", Backend: domain.BackendPTY},
		},
		prompts: []domain.ScheduledPrompt{{
			ID:         "p1",
			CronExpr:   "* * * * *",
			Prompt:     "run the nightly checks",
			SessionIDs: []string{"a", "b"},
			CreatedAt:  time.Now().Add(-time.Hour),
		}},
	}
}

func TestScheduledPromptGoesThroughSender(t *testing.T) {
	repo := scheduledPromptRepo()
	sender := &recordingSender{delivered: true}
	d := NewDaemon(&config.Config{}, repo, nil, sender)

	if err := d.pollScheduledPrompts(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []sentPrompt{
		{session: "a", text: "run the nightly checks", mode: SendNote},
		{session: "b", text: "run the nightly checks", mode: SendNote},
	}
	if len(sender.sent) != len(want) {
		t.Fatalf("sent %+v, want %+v", sender.sent, want)
	}
	for i := range want {
		if sender.sent[i] != want[i] {
			t.Errorf("sent[%d] = %+v, want %+v", i, sender.sent[i], want[i])
		}
	}
	if len(repo.saved) != 1 || repo.saved[0].LastRunAt.IsZero() {
		t.Fatalf("saved %+v, want one save with LastRunAt set", repo.saved)
	}
}

func TestScheduledPromptRetriesWhenServeDown(t *testing.T) {
	repo := scheduledPromptRepo()
	sender := &recordingSender{err: errors.New("server-not-running: /home/u/.aiman/aiman.sock")}
	d := NewDaemon(&config.Config{}, repo, nil, sender)

	if err := d.pollScheduledPrompts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("SaveScheduledPrompt called %d times while serve is down; LastRunAt must not advance", len(repo.saved))
	}
}

func TestUnblockerUsesAnswerMode(t *testing.T) {
	repo := &daemonRepo{sessions: map[string]domain.Session{
		"auto": {ID: "auto", Backend: domain.BackendPTY, Mode: domain.SessionModeAutonomous, Status: domain.SessionStatusActive},
	}}
	sender := &recordingSender{delivered: false}
	d := NewDaemon(&config.Config{}, repo, nil, sender)
	d.capture = func(context.Context, domain.Session) (string, error) {
		return "Run the migration?\nDo you want to proceed? (y/n)\n", nil
	}

	if err := d.monitorPanes(context.Background()); err != nil {
		t.Fatalf("monitorPanes: %v (delivered=false must not be an error)", err)
	}
	want := sentPrompt{session: "auto", text: unblockPrompt, mode: SendAnswer}
	if len(sender.sent) != 1 || sender.sent[0] != want {
		t.Fatalf("sent %+v, want [%+v]", sender.sent, want)
	}
}

func TestTriggerHasNoRawSendKeys(t *testing.T) {
	src, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "send-keys") {
		t.Fatal("daemon.go types into terminals directly; route prompts through the PromptSender")
	}
}
