package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
)

const (
	// TrackInterval is how often serve checks linked children's PRs.
	TrackInterval = 2 * time.Minute
	// ghTimeout bounds one gh call.
	ghTimeout = 45 * time.Second
)

// PRState is what the tracker needs from a child's pull request.
type PRState struct {
	Base         string
	Open, Merged bool
}

// Todos is serve's to-do adapter (Server.TodoAccess).
type Todos interface {
	Get(sessionID string) (domain.TodoList, error)
	Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error
}

// Notifier records a notification (notify.Recorder).
type Notifier interface {
	Emit(ctx context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error
}

// Tracker marks a parent's to-do done when the child linked by its child:
// Ref lands a PR on the parent's branch with a report that cites evidence. Its
// only state is which items it already asked about, so a fresh Tracker resumes
// from the stored Refs.
type Tracker struct {
	Todos    Todos
	Sessions func() ([]domain.Session, error)
	PR       func(ctx context.Context, child domain.Session) (PRState, error)
	Passed   func(childID string) bool  // nil means false; nl-7's LatestFor.Passed
	BeforePR func(parentID string) bool // nil means false; nl-10 sets this from verify.before_pr
	Notify   Notifier                   // nil drops the missing-child notification
	// Report reads the child's .aiman_report.md. nil, or a read error, is no
	// evidence.
	Report func(ctx context.Context, child domain.Session) (string, error)
	// Steer queues a note for the parent's agent as aiman. nil drops it.
	Steer func(parentID, text string)

	// asked holds the items whose coordinator was already told the child
	// returned no evidence, so the note is sent once per serve run.
	mu    sync.Mutex
	asked map[string]bool
}

// Run calls TrackOnce now and then every interval until ctx ends.
func (t *Tracker) Run(ctx context.Context, interval time.Duration) {
	t.TrackOnce(ctx)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.TrackOnce(ctx)
		}
	}
}

// TrackOnce walks every session's list and settles each open item linked to
// a child: done when the child's PR has landed and its report cites evidence,
// unlinked when the child session no longer exists. A failure on one item is
// logged and skipped.
func (t *Tracker) TrackOnce(ctx context.Context) {
	sessions, err := t.Sessions()
	if err != nil {
		log.Printf("coordinator: list sessions: %v", err)
		return
	}
	byID := make(map[string]domain.Session, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}
	for _, parent := range sessions {
		list, err := t.Todos.Get(parent.ID)
		if err != nil {
			log.Printf("coordinator: to-dos of %s: %v", parent.ID, err)
			continue
		}
		for _, it := range list.Items {
			childID, ok := ChildID(it.Ref)
			if !ok || it.State == domain.TodoDone || it.State == domain.TodoDropped {
				continue
			}
			child, ok := byID[childID]
			if !ok {
				t.unlink(ctx, parent, it, childID)
				continue
			}
			if !t.landed(ctx, parent, child) {
				continue
			}
			if err := t.evidence(ctx, child); err != nil {
				t.askForReport(parent, it, child, err)
				continue
			}
			t.settle(ctx, parent, it, func(item *domain.TodoItem) { item.State = domain.TodoDone })
		}
	}
}

// landed reports whether the child's PR targets the parent's branch and is
// merged, or is open with a passing review when the parent verifies before
// a PR.
func (t *Tracker) landed(ctx context.Context, parent, child domain.Session) bool {
	pr, err := t.PR(ctx, child)
	if err != nil {
		log.Printf("coordinator: PR of %s: %v", child.ID, err)
		return false
	}
	if parent.Branch == "" || pr.Base != parent.Branch {
		return false
	}
	if pr.Merged {
		return true
	}
	return pr.Open &&
		t.BeforePR != nil && t.BeforePR(parent.ID) &&
		t.Passed != nil && t.Passed(child.ID)
}

// evidence reads and validates the child's report.
func (t *Tracker) evidence(ctx context.Context, child domain.Session) error {
	if t.Report == nil {
		return errors.New("no report reader")
	}
	md, err := t.Report(ctx, child)
	if err != nil {
		return err
	}
	return ValidateReport(md)
}

// askForReport steers the coordinator once to get the child's report. The
// item stays open: the child's PR may land, but the item is the contract.
func (t *Tracker) askForReport(parent domain.Session, it domain.TodoItem, child domain.Session, why error) {
	key := parent.ID + "/" + it.ID + "/" + child.ID
	t.mu.Lock()
	if t.asked == nil {
		t.asked = map[string]bool{}
	}
	done := t.asked[key]
	t.asked[key] = true
	t.mu.Unlock()
	if done {
		return
	}
	log.Printf("coordinator: report of %s: %v", child.ID, why)
	if t.Steer == nil {
		return
	}
	name := child.Name
	if name == "" {
		name = child.ID
	}
	t.Steer(parent.ID, fmt.Sprintf("[aiman] child %s returned no evidence; ask it for %s before marking %s done.",
		name, domain.AimanReportFileName, it.ID))
}

// unlink clears a Ref whose child session is gone and tells the user. The
// item stays open for the coordinator to pick up again.
func (t *Tracker) unlink(ctx context.Context, parent domain.Session, it domain.TodoItem, childID string) {
	if !t.settle(ctx, parent, it, func(item *domain.TodoItem) { item.Ref = "" }) || t.Notify == nil {
		return
	}
	detail := fmt.Sprintf("child session %s for to-do %s no longer exists; the item is unlinked", childID, it.ID)
	if err := t.Notify.Emit(ctx, parent.ID, notify.Label(parent.IssueKey, parent.Name, parent.ID), domain.NotifyError, detail); err != nil {
		log.Printf("coordinator: notify %s: %v", parent.ID, err)
	}
}

// settle applies change to the item as aiman, only if it still carries the
// Ref that was read, so a concurrent edit is not overwritten. It reports
// whether the change was made.
func (t *Tracker) settle(ctx context.Context, parent domain.Session, seen domain.TodoItem, change func(*domain.TodoItem)) bool {
	applied := false
	err := t.Todos.Mutate(ctx, parent.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].ID == seen.ID && l.Items[i].Ref == seen.Ref {
				change(&l.Items[i])
				applied = true
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("coordinator: update %s/%s: %v", parent.ID, seen.ID, err)
		return false
	}
	return applied
}

// GHPR reads the child's PR with gh in its worktree, so gh resolves the
// repository and branch from it. A branch with no PR is an empty PRState.
func GHPR(ctx context.Context, child domain.Session) (PRState, error) {
	dir := child.WorktreePath
	if dir == "" {
		dir = child.WorkingDirectory
	}
	if dir == "" {
		// gh would otherwise read whichever repository serve runs in.
		return PRState{}, errors.New("session has no worktree or working directory")
	}
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "state,mergedAt,baseRefName")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "no pull requests found") {
			return PRState{}, nil
		}
		return PRState{}, fmt.Errorf("gh pr view: %w: %s", err, msg)
	}
	return ParsePRState(out)
}

// ParsePRState decodes `gh pr view --json state,mergedAt,baseRefName`.
func ParsePRState(raw []byte) (PRState, error) {
	var v struct {
		State       string     `json:"state"`
		MergedAt    *time.Time `json:"mergedAt"`
		BaseRefName string     `json:"baseRefName"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return PRState{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	return PRState{
		Base:   v.BaseRefName,
		Open:   v.State == "OPEN",
		Merged: v.State == "MERGED" || (v.MergedAt != nil && !v.MergedAt.IsZero()),
	}, nil
}
