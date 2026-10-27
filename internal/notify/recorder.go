package notify

import (
	"context"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Store persists notifications with min_interval.
type Store interface {
	AppendIfDue(ctx context.Context, n domain.Notification, minInterval time.Duration) (domain.Notification, bool, error)
}

// Settings configures the recorder.
type Settings struct {
	Events      map[domain.NotifyKind]bool
	MinInterval time.Duration
	Host        string
}

// Observation is one session state sample.
type Observation struct {
	SessionID string
	Label     string
	State     domain.AgentState
	Confident bool
	Ended     bool
	Question  string
	LastLine  func() string
}

// Recorder tracks state transitions and emits notifications.
type Recorder struct {
	store    Store
	settings Settings
	publish  func(sessionID string, kind domain.NotifyKind)
	mu       sync.Mutex
	last     map[string]domain.NotifyKind
}

// NewRecorder creates a recorder.
func NewRecorder(store Store, s Settings, publish func(sessionID string, kind domain.NotifyKind)) *Recorder {
	return &Recorder{
		store:    store,
		settings: s,
		publish:  publish,
		last:     map[string]domain.NotifyKind{},
	}
}

// Forget drops in-memory state for a session.
func (r *Recorder) Forget(sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.last, sessionID)
	r.mu.Unlock()
}

// Observe derives a notification kind from state and may notify on transition.
func (r *Recorder) Observe(ctx context.Context, o Observation) error {
	if r == nil {
		return nil
	}
	kind, detail := r.derive(o)
	r.mu.Lock()
	prev, seen := r.last[o.SessionID]
	if !seen {
		r.last[o.SessionID] = kind
		r.mu.Unlock()
		return nil
	}
	if kind == prev {
		r.mu.Unlock()
		return nil
	}
	if kind == "" {
		r.last[o.SessionID] = kind
		r.mu.Unlock()
		return nil
	}
	if !r.settings.Events[kind] {
		r.last[o.SessionID] = kind
		r.mu.Unlock()
		return nil
	}
	label := o.Label
	r.mu.Unlock()

	if detail == "" && kind == domain.NotifyWaitingInput && o.LastLine != nil {
		detail = o.LastLine()
	}
	if detail == "" && (kind == domain.NotifyEnded || kind == domain.NotifyError) && o.LastLine != nil {
		detail = o.LastLine()
	}
	text := Format(label, r.settings.Host, kind, detail)
	n := domain.Notification{
		SessionID: o.SessionID,
		Kind:      kind,
		Label:     label,
		Host:      r.settings.Host,
		Text:      text,
		At:        time.Now(),
	}
	nn, ok, err := r.store.AppendIfDue(ctx, n, r.settings.MinInterval)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	r.mu.Lock()
	r.last[o.SessionID] = kind
	r.mu.Unlock()
	if r.publish != nil {
		r.publish(o.SessionID, nn.Kind)
	}
	return nil
}

// Emit bypasses transitions and forces a notification if allowed.
func (r *Recorder) Emit(ctx context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error {
	if r == nil {
		return nil
	}
	if r.settings.Events == nil || !r.settings.Events[kind] {
		return nil
	}
	text := Format(label, r.settings.Host, kind, detail)
	n := domain.Notification{
		SessionID: sessionID,
		Kind:      kind,
		Label:     label,
		Host:      r.settings.Host,
		Text:      text,
		At:        time.Now(),
	}
	nn, ok, err := r.store.AppendIfDue(ctx, n, r.settings.MinInterval)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if r.publish != nil {
		r.publish(sessionID, nn.Kind)
	}
	return nil
}

func (r *Recorder) derive(o Observation) (domain.NotifyKind, string) {
	if o.Ended {
		return domain.NotifyEnded, ""
	}
	if !o.Confident {
		return "", ""
	}
	switch o.State {
	case domain.AgentStateExited:
		return domain.NotifyEnded, ""
	case domain.AgentStateErrored:
		return domain.NotifyError, ""
	case domain.AgentStateWaitingInput:
		return domain.NotifyWaitingInput, o.Question
	default:
		return "", ""
	}
}
