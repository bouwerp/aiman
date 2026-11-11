package ciwatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
)

// CheckRef is the to-do Ref that links an item to a CI check.
func CheckRef(name string) string { return "check:" + name }

// openTodoByRef finds a pending or active item with ref.
func openTodoByRef(list domain.TodoList, ref string) (domain.TodoItem, bool) {
	for _, it := range list.Items {
		if it.Ref == ref && (it.State == domain.TodoPending || it.State == domain.TodoActive) {
			return it, true
		}
	}
	return domain.TodoItem{}, false
}

// reactToChecks closes the to-dos of passed checks at once, then waits for
// the run on the PR head to finish: while any check is pending, failures add
// nothing. A finished run gets one steering note listing every failed check
// that got a new to-do in this pass.
func (w *Watcher) reactToChecks(ctx context.Context, s domain.Session, pr PRView) error {
	pending := false
	for _, c := range pr.Checks {
		switch c.Conclusion {
		case ConclusionSuccess:
			if err := w.checkPassed(ctx, s, c); err != nil {
				return err
			}
		case ConclusionPending:
			pending = true
		}
	}
	if pending {
		return nil
	}
	var failed []failedCheck
	for _, c := range pr.Checks {
		if c.Conclusion != ConclusionFailure {
			continue
		}
		f, added, err := w.checkFailed(ctx, s, pr, c)
		if err != nil {
			return err
		}
		if added {
			failed = append(failed, f)
		}
	}
	if len(failed) > 0 {
		w.d.Steer(s.ID, domain.AuthorAiman, w.runNote(ctx, s, pr, failed))
	}
	return nil
}

// failedCheck is a failed check and the to-do this pass opened for it.
type failedCheck struct {
	check  Check
	todoID string
}

// checkPassed clears the check's attempts and closes its open to-do, so a
// to-do the agent forgot to close cannot block later notes for that check.
func (w *Watcher) checkPassed(ctx context.Context, s domain.Session, c Check) error {
	if err := w.d.State.ClearCheck(ctx, s.ID, c.Name); err != nil {
		return err
	}
	list, err := w.d.Todos.Get(s.ID)
	if err != nil {
		return err
	}
	ref := CheckRef(c.Name)
	if _, open := openTodoByRef(list, ref); !open {
		return nil
	}
	return w.d.Todos.Mutate(ctx, s.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].Ref == ref && (l.Items[i].State == domain.TodoPending || l.Items[i].State == domain.TodoActive) {
				l.Items[i].State = domain.TodoDone
			}
		}
		return nil
	})
}

// checkFailed records the failure and opens the check's `Fix CI` to-do.
// added is false when a to-do for the check is already open or the check
// has escalated; such a check stays out of the run note.
func (w *Watcher) checkFailed(ctx context.Context, s domain.Session, pr PRView, c Check) (failedCheck, bool, error) {
	n, err := w.d.State.RecordCheckFailure(ctx, s.ID, c.Name, pr.HeadSHA)
	if err != nil {
		return failedCheck{}, false, err
	}
	if n > MaxFixAttempts {
		return failedCheck{}, false, w.escalate(ctx, s, pr, c)
	}
	id, err := w.addCheckTodo(ctx, s.ID, c.Name)
	if err != nil || id == "" {
		return failedCheck{}, false, err
	}
	return failedCheck{check: c, todoID: id}, true, nil
}

// addCheckTodo adds `Fix CI: <name>` unless one is open, and returns the new
// item's id, or "" when nothing was added.
func (w *Watcher) addCheckTodo(ctx context.Context, sessionID, name string) (string, error) {
	ref := CheckRef(name)
	list, err := w.d.Todos.Get(sessionID)
	if err != nil {
		return "", err
	}
	if _, open := openTodoByRef(list, ref); open {
		return "", nil // the agent is already on it
	}
	added := false
	err = w.d.Todos.Mutate(ctx, sessionID, domain.AuthorAiman, func(l *domain.TodoList) error {
		if _, open := openTodoByRef(*l, ref); open {
			return nil
		}
		l.Items = append(l.Items, domain.TodoItem{Text: "Fix CI: " + name, State: domain.TodoPending, Ref: ref})
		added = true
		return nil
	})
	if err != nil || !added {
		return "", err
	}
	list, err = w.d.Todos.Get(sessionID)
	if err != nil {
		return "", err
	}
	item, _ := openTodoByRef(list, ref)
	return item.ID, nil
}

func (w *Watcher) escalate(ctx context.Context, s domain.Session, pr PRView, c Check) error {
	fresh, err := w.d.State.MarkCheckEscalated(ctx, s.ID, c.Name, w.d.Now())
	if err != nil || !fresh || w.d.Notify == nil {
		return err
	}
	detail := fmt.Sprintf("CI check %s still failing after %d fix attempts on PR #%d", c.Name, MaxFixAttempts, pr.Number)
	return w.d.Notify.Emit(ctx, s.ID, notify.Label(s.IssueKey, s.Name, s.ID), domain.NotifyCIEscalation, detail)
}

// RunNoteEnd ends every failed-run note.
const RunNoteEnd = "Fix every failed check, run aiman ci local --wait, then push once. Pushes are refused until the gate passes for the new commit."

// runNote is the one steering note for a finished run: each failed check
// with its to-do and log tail, then the instruction to push once.
func (w *Watcher) runNote(ctx context.Context, s domain.Session, pr PRView, failed []failedCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[aiman] CI run on %s (PR #%d) finished with %d failed check(s).\n", shortSHA(pr.HeadSHA), pr.Number, len(failed))
	for _, f := range failed {
		b.WriteString("\n")
		b.WriteString(w.checkNote(ctx, s, f.check, f.todoID))
		b.WriteString("\n")
	}
	b.WriteString("\n" + RunNoteEnd)
	return b.String()
}

// checkNote is one failed check's part of the run note: the failing log
// tail, or where to look when gh cannot give one.
func (w *Watcher) checkNote(ctx context.Context, s domain.Session, c Check, todoID string) string {
	head := fmt.Sprintf("CI check %q failed, to-do %s.\n", c.Name, todoID)
	if c.JobID != 0 {
		if log, err := w.d.GitHub.FailedJobLog(ctx, sessionDir(s), c.JobID); err == nil && strings.TrimSpace(log) != "" {
			return head + fmt.Sprintf("Last %d lines of `gh run view --job %d --log-failed`:\n", LogTailLines, c.JobID) + strings.TrimRight(log, "\n")
		}
	}
	return head + "Log unavailable: " + c.DetailsURL
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
