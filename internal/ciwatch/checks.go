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

func (w *Watcher) reactToChecks(ctx context.Context, s domain.Session, pr PRView) error {
	for _, c := range pr.Checks {
		var err error
		switch c.Conclusion {
		case ConclusionSuccess:
			err = w.checkPassed(ctx, s, c)
		case ConclusionFailure:
			err = w.checkFailed(ctx, s, pr, c)
		}
		if err != nil {
			return err
		}
	}
	return nil
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

func (w *Watcher) checkFailed(ctx context.Context, s domain.Session, pr PRView, c Check) error {
	n, err := w.d.State.RecordCheckFailure(ctx, s.ID, c.Name, pr.HeadSHA)
	if err != nil {
		return err
	}
	if n > MaxFixAttempts {
		return w.escalate(ctx, s, pr, c)
	}

	ref := CheckRef(c.Name)
	list, err := w.d.Todos.Get(s.ID)
	if err != nil {
		return err
	}
	if _, open := openTodoByRef(list, ref); open {
		return nil // the agent is already on it; one note per open to-do
	}
	added := false
	err = w.d.Todos.Mutate(ctx, s.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		if _, open := openTodoByRef(*l, ref); open {
			return nil
		}
		l.Items = append(l.Items, domain.TodoItem{Text: "Fix CI: " + c.Name, State: domain.TodoPending, Ref: ref})
		added = true
		return nil
	})
	if err != nil || !added {
		return err
	}
	list, err = w.d.Todos.Get(s.ID)
	if err != nil {
		return err
	}
	item, _ := openTodoByRef(list, ref)
	w.d.Steer(s.ID, domain.AuthorAiman, w.checkNote(ctx, s, pr, c, item.ID))
	return nil
}

func (w *Watcher) escalate(ctx context.Context, s domain.Session, pr PRView, c Check) error {
	fresh, err := w.d.State.MarkCheckEscalated(ctx, s.ID, c.Name, w.d.Now())
	if err != nil || !fresh || w.d.Notify == nil {
		return err
	}
	detail := fmt.Sprintf("CI check %s still failing after %d fix attempts on PR #%d", c.Name, MaxFixAttempts, pr.Number)
	return w.d.Notify.Emit(ctx, s.ID, notify.Label(s.IssueKey, s.Name, s.ID), domain.NotifyCIEscalation, detail)
}

// checkNote is the extra steering text for a failed check: the failing log
// tail, or where to look when gh cannot give one.
func (w *Watcher) checkNote(ctx context.Context, s domain.Session, pr PRView, c Check, todoID string) string {
	head := fmt.Sprintf("[aiman] CI check %q failed on %s (PR #%d), to-do %s.\n", c.Name, shortSHA(pr.HeadSHA), pr.Number, todoID)
	if c.JobID != 0 {
		if log, err := w.d.GitHub.FailedJobLog(ctx, sessionDir(s), c.JobID); err == nil && strings.TrimSpace(log) != "" {
			return head + fmt.Sprintf("Last %d lines of `gh run view --job %d --log-failed`:\n", LogTailLines, c.JobID) + log
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
