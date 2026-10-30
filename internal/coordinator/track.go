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
	// Ledger appends a session_events row as aiman. nil drops it.
	Ledger func(ctx context.Context, sessionID string, kind domain.EventKind, payload map[string]any)
	// Retire stops and forgets a consult and removes its scratch directory.
	// nil leaves the consult running.
	Retire func(ctx context.Context, child domain.Session)
	// KeepReport copies a consult's report outside the directory Retire
	// removes and returns the copy's path. nil points the note at the
	// consult's own report, which Retire removes.
	KeepReport func(ctx context.Context, child domain.Session, md string) (string, error)
	// Alive reports whether a consult's terminal still runs. nil is alive.
	Alive func(ctx context.Context, child domain.Session) bool
	// ConsultTimeout is how long a consult may run without a report. Zero
	// waits for ever.
	ConsultTimeout time.Duration
	// ConsultTimeoutFor, when set, replaces ConsultTimeout with a limit
	// chosen by the consult's parent (ConsultTimeouts).
	ConsultTimeoutFor func(parent domain.Session) time.Duration
	// Now is the clock. nil is time.Now.
	Now func() time.Time

	// asked holds the notes already sent (a missing report, surprises), so
	// each goes out once per serve run.
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
// or, for a consult, when its report parses; unlinked when the child session
// no longer exists. It then retires every consult no open item links. A
// failure on one item is logged and skipped.
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
	linked := map[string]bool{}
	listed := map[string]bool{}
	for _, parent := range sessions {
		list, err := t.Todos.Get(parent.ID)
		if err != nil {
			log.Printf("coordinator: to-dos of %s: %v", parent.ID, err)
			continue
		}
		listed[parent.ID] = true
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
			if child.Role == domain.SessionRoleConsult {
				// Settled, abandoned or still held: the sweep leaves it.
				linked[childID] = true
				t.trackConsult(ctx, parent, it, child)
				continue
			}
			t.trackChild(ctx, parent, it, child)
		}
	}
	t.sweepConsults(ctx, sessions, linked, listed)
}

// trackChild marks a PR child's item done once its PR landed with a report
// that cites evidence.
func (t *Tracker) trackChild(ctx context.Context, parent domain.Session, it domain.TodoItem, child domain.Session) {
	if !t.landed(ctx, parent, child) {
		return
	}
	md, err := t.evidence(ctx, child)
	if err != nil {
		t.askForReport(parent, it, child, err)
		return
	}
	t.steerSurprises(parent, child, md)
	t.settle(ctx, parent, it, func(item *domain.TodoItem) { item.State = domain.TodoDone })
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

// evidence reads and validates the child's report and returns its text.
func (t *Tracker) evidence(ctx context.Context, child domain.Session) (string, error) {
	if t.Report == nil {
		return "", errors.New("no report reader")
	}
	md, err := t.Report(ctx, child)
	if err != nil {
		return "", err
	}
	return md, ValidateReport(md)
}

// steerSurprises tells the coordinator once per child when a landed child's
// report names surprises, so it re-reads the design before starting pieces
// that depend on this one.
func (t *Tracker) steerSurprises(parent domain.Session, child domain.Session, md string) {
	text, any := ReportSurprises(md)
	if !any || t.Steer == nil {
		return
	}
	if !t.firstAsk("surprises/" + parent.ID + "/" + child.ID) {
		return
	}
	t.Steer(parent.ID, fmt.Sprintf("[aiman] child %s reported surprises that may affect other pieces; re-read the design before starting dependent pieces:\n%s", child.ID, text))
}

// askForReport steers the coordinator once to get the child's report. The
// item stays open: the child's PR may land, but the item is the contract.
func (t *Tracker) askForReport(parent domain.Session, it domain.TodoItem, child domain.Session, why error) {
	if !t.firstAsk(parent.ID + "/" + it.ID + "/" + child.ID) {
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

// firstAsk records key and reports whether it was new in this serve run.
func (t *Tracker) firstAsk(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.asked == nil {
		t.asked = map[string]bool{}
	}
	if t.asked[key] {
		return false
	}
	t.asked[key] = true
	return true
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
