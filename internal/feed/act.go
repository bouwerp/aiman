package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// ErrRefused is wrapped by every refusal Act makes before it changes
// anything: a closed action, a bad option, params or reason.
var ErrRefused = errors.New("feed: refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// MuteWriter writes a mute to feed.db.
type MuteWriter interface {
	Mute(ctx context.Context, m domain.FeedMute) error
}

// ActEvents resolves the events an action answered.
type ActEvents interface {
	ByIDs(ctx context.Context, ids []int64) ([]domain.FeedEvent, error)
}

// ActDeps are the seams Act and WakeSnoozed use.
type ActDeps struct {
	Store    Store
	Events   ActEvents
	Mutes    MuteWriter
	MuteDays int
	Now      func() time.Time
	// SlackDraft saves a draft in the member's Slack and returns where it is.
	SlackDraft func(ctx context.Context, channel, threadTS, text string) (string, error)
	// TodoAdd adds an open to-do to a visible session's list.
	TodoAdd func(ctx context.Context, session, text string) error
	// OnAction reports each action the call changed (hub event); optional.
	OnAction func(domain.FeedAction)
	// TTL is how long a woken action waits again; zero is 24h.
	TTL time.Duration
}

// ActRequest is one operator decision.
type ActRequest struct {
	Op     string // approve or dismiss
	Option *int
	// Params replace the chosen option's params when set.
	Params json.RawMessage
	Reason domain.FeedDismissReason
	Note   string
	Until  time.Time // later
	By     string
}

// actMu serialises decisions so two approvals of one action cannot both run
// its executor.
var actMu sync.Mutex

func (d ActDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d ActDeps) changed(a domain.FeedAction) {
	if d.OnAction != nil {
		d.OnAction(a)
	}
}

// Act approves or dismisses the action. A refusal wraps ErrRefused and
// leaves the action as it was; an executor failure is not an error: the
// action is stored as failed with the error in Result.
func Act(ctx context.Context, d ActDeps, id int64, req ActRequest) (domain.FeedAction, error) {
	actMu.Lock()
	defer actMu.Unlock()
	a, err := d.Store.GetFeedAction(ctx, id)
	if err != nil {
		return domain.FeedAction{}, err
	}
	if a.State != ActionProposed && a.State != ActionSnoozed {
		return a, refuse("action %d is %s", id, a.State)
	}
	switch req.Op {
	case "approve":
		return approve(ctx, d, a, req)
	case "dismiss":
		return dismiss(ctx, d, a, req)
	default:
		return a, refuse("unknown op %q", req.Op)
	}
}

func approve(ctx context.Context, d ActDeps, a domain.FeedAction, req ActRequest) (domain.FeedAction, error) {
	if req.Option == nil || *req.Option < 0 || *req.Option >= len(a.Options) {
		return a, refuse("option out of range (the action has %d)", len(a.Options))
	}
	opt := a.Options[*req.Option]
	kind := opt.EffectiveKind(a.Kind)
	if !domain.FeedSafeKind(kind) {
		return a, refuse("kind %s is not available yet", kind)
	}
	params := opt.Params
	if len(req.Params) > 0 {
		params = req.Params
	}
	run, err := prepareExecutor(d, kind, params)
	if err != nil {
		return a, err
	}
	now := d.now()
	a.Chosen, a.ChosenParams, a.DecidedBy, a.DecidedAt = req.Option, params, req.By, now
	a.State = ActionRunning
	if err := d.Store.UpdateFeedAction(ctx, a); err != nil {
		return a, err
	}
	d.changed(a)
	result, execErr := run(ctx)
	if execErr != nil {
		a.State, a.Result = ActionFailed, execErr.Error()
	} else {
		a.State, a.Result = ActionDone, result
	}
	if err := d.Store.UpdateFeedAction(ctx, a); err != nil {
		return a, err
	}
	d.changed(a)
	return a, nil
}

// prepareExecutor checks the params of a kind and returns the call that runs
// it, so a bad request is refused before any state changes.
func prepareExecutor(d ActDeps, kind domain.FeedActionKind, params json.RawMessage) (func(context.Context) (string, error), error) {
	switch kind {
	case domain.FeedRead:
		return func(context.Context) (string, error) { return "", nil }, nil
	case domain.FeedSlackDraft:
		var p struct {
			Channel  string `json:"channel"`
			ThreadTS string `json:"thread_ts"`
			Text     string `json:"text"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, refuse("slack_draft params: %v", err)
		}
		if p.Channel == "" || strings.TrimSpace(p.Text) == "" {
			return nil, refuse("slack_draft needs a channel and text")
		}
		if d.SlackDraft == nil {
			return nil, refuse("slack drafts are not set up on this host")
		}
		return func(ctx context.Context) (string, error) {
			link, err := d.SlackDraft(ctx, p.Channel, p.ThreadTS, p.Text)
			if err != nil {
				return "", err
			}
			return "draft saved: " + link, nil
		}, nil
	case domain.FeedTodoAdd:
		var p struct {
			Session string `json:"session"`
			Text    string `json:"text"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, refuse("todo_add params: %v", err)
		}
		if p.Session == "" || strings.TrimSpace(p.Text) == "" {
			return nil, refuse("todo_add needs a session and text")
		}
		if d.TodoAdd == nil {
			return nil, refuse("to-dos are not available on this host")
		}
		return func(ctx context.Context) (string, error) {
			if err := d.TodoAdd(ctx, p.Session, p.Text); err != nil {
				return "", err
			}
			return "to-do added to " + p.Session, nil
		}, nil
	}
	return nil, refuse("kind %s is not available yet", kind)
}
