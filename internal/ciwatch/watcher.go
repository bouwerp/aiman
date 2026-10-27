package ciwatch

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// PollInterval is how often each watched session's PR is read.
	PollInterval = 2 * time.Minute
	// MaxFixAttempts is how many new failing SHAs a check may produce after
	// its first failure before the user is asked to step in.
	MaxFixAttempts = 3
)

// Store is the watcher's persisted state; *sqlite.Repository satisfies it.
type Store interface {
	LoadPRWatch(ctx context.Context, sessionID string) (domain.PRWatchState, error)
	SavePRWatch(ctx context.Context, st domain.PRWatchState) error
	RecordCheckFailure(ctx context.Context, sessionID, check, sha string) (int, error)
	MarkCheckEscalated(ctx context.Context, sessionID, check string, at time.Time) (bool, error)
	ClearCheck(ctx context.Context, sessionID, check string) error
	SeenComments(ctx context.Context, sessionID string) (map[int64]bool, error)
	MarkCommentsSeen(ctx context.Context, sessionID string, ids []int64) error
	ResetPRWatch(ctx context.Context, sessionID string) error
}

// Sessions lists the sessions serve knows about.
type Sessions interface {
	List(ctx context.Context) ([]domain.Session, error)
}

// Todos is serve's to-do adapter (Server.TodoAccess). A Mutate as aiman
// renders the list, publishes todo_changed and steers the agent.
type Todos interface {
	Get(sessionID string) (domain.TodoList, error)
	Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error
}

// Notifier records a notification (notify.Recorder).
type Notifier interface {
	Emit(ctx context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error
}

// SteerFunc queues extra note text for a session's agent. serve adapts its
// Steerer to this, because ciwatch cannot import internal/server.
type SteerFunc func(sessionID string, author domain.TodoAuthor, text string)

// Deps are the watcher's collaborators.
type Deps struct {
	Sessions Sessions
	GitHub   GitHub
	Todos    Todos
	Steer    SteerFunc
	Notify   Notifier
	State    Store
	Now      func() time.Time // nil means time.Now
}

// Watcher polls watched sessions' PRs and turns failed checks and new review
// comments into to-dos and steering notes.
type Watcher struct {
	d Deps
	// mu serialises polls, so a Kick and the ticker never react to the same
	// PR state twice.
	mu sync.Mutex
	// viewer is the gh login, cached once known; guarded by mu.
	viewer string
	// kicks asks Run to poll one session now.
	kicks chan string
}

// New returns a watcher over d.
func New(d Deps) *Watcher {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Watcher{d: d, kicks: make(chan string, 16)}
}

// Watched reports whether a session's PR should be polled now.
func Watched(s domain.Session) bool {
	return s.WatchesPR() && s.Status == domain.SessionStatusActive && !s.AgentEnded && sessionDir(s) != ""
}

// Run polls every watched session now and then every PollInterval, and polls
// one session at once when it is kicked. It returns when ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	w.pollAll(ctx)
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.pollAll(ctx)
		case id := <-w.kicks:
			w.pollOne(ctx, id)
		}
	}
}

// Kick asks Run to poll a session now. It never blocks; a kick arriving while
// 16 are pending is dropped, and the ticker covers it.
func (w *Watcher) Kick(sessionID string) {
	select {
	case w.kicks <- sessionID:
	default:
	}
}

// pollAll polls every watched session. A failing poll is recorded as that
// session's LastError and does not stop the others.
func (w *Watcher) pollAll(ctx context.Context) {
	list, err := w.d.Sessions.List(ctx)
	if err != nil {
		return
	}
	for _, s := range list {
		if ctx.Err() != nil {
			return
		}
		if Watched(s) {
			_ = w.PollSession(ctx, s)
		}
	}
}

func (w *Watcher) pollOne(ctx context.Context, id string) {
	list, err := w.d.Sessions.List(ctx)
	if err != nil {
		return
	}
	for _, s := range list {
		if s.ID == id && Watched(s) {
			_ = w.PollSession(ctx, s)
			return
		}
	}
}

// SetWatch starts the session's watch afresh. Turning it on records now as
// WatchSince, so review comments already on the PR are not steered.
func (w *Watcher) SetWatch(ctx context.Context, sessionID string, on bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.d.State.ResetPRWatch(ctx, sessionID); err != nil {
		return err
	}
	if !on {
		return nil
	}
	return w.d.State.SavePRWatch(ctx, domain.PRWatchState{SessionID: sessionID, WatchSince: w.d.Now()})
}

// Status is the session's stored watcher state.
func (w *Watcher) Status(ctx context.Context, sessionID string) (domain.PRWatchState, error) {
	return w.d.State.LoadPRWatch(ctx, sessionID)
}

// Forget drops every CI row for a session.
func (w *Watcher) Forget(ctx context.Context, sessionID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.d.State.ResetPRWatch(ctx, sessionID)
}

// sessionDir is where gh runs for a session.
func sessionDir(s domain.Session) string {
	if s.WorktreePath != "" {
		return s.WorktreePath
	}
	return s.WorkingDirectory
}

// PollSession reads the session's PR once and reacts to it. A gh failure is
// recorded as LastError and returned; nothing else changes.
func (w *Watcher) PollSession(ctx context.Context, s domain.Session) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	st, err := w.d.State.LoadPRWatch(ctx, s.ID)
	if err != nil {
		return err
	}
	pr, err := w.d.GitHub.PRView(ctx, sessionDir(s))
	st.LastPolledAt = w.d.Now()
	switch {
	case errors.Is(err, ErrNoPR):
		st.PRNumber, st.PRState, st.HeadSHA, st.LastError = 0, "", "", ""
		return w.d.State.SavePRWatch(ctx, st)
	case err != nil:
		st.LastError = err.Error()
		if serr := w.d.State.SavePRWatch(ctx, st); serr != nil {
			return errors.Join(err, serr)
		}
		return err
	}
	st.PRNumber, st.PRState, st.HeadSHA, st.LastError = pr.Number, pr.State, pr.HeadSHA, ""
	if err := w.d.State.SavePRWatch(ctx, st); err != nil {
		return err
	}
	if pr.State != "OPEN" {
		return nil
	}
	if err := w.reactToChecks(ctx, s, pr); err != nil {
		return err
	}
	return w.reactToComments(ctx, s, st, pr)
}
