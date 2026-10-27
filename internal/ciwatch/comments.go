package ciwatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// reviewTitleRunes caps the comment's first line in the to-do text.
	reviewTitleRunes = 80
	// reviewBodyBytes caps the comment body in the steering note.
	reviewBodyBytes = 4000
	// emptyReviewTitle stands in for a comment with no text, such as a
	// suggestion block GitHub renders from metadata.
	emptyReviewTitle = "<empty comment>"
)

// ReviewRef is the to-do Ref that links an item to a review comment.
func ReviewRef(id int64) string { return "review:" + strconv.FormatInt(id, 10) }

// viewerLogin returns the gh login, fetched once. An error leaves it unknown
// so the next poll asks again.
func (w *Watcher) viewerLogin(ctx context.Context) string {
	if w.viewer == "" {
		if login, err := w.d.GitHub.ViewerLogin(ctx); err == nil {
			w.viewer = login
		}
	}
	return w.viewer
}

// reactToComments turns each unseen review comment into a to-do and a note.
// The agent's own replies and comments older than the watch are marked seen
// without steering.
func (w *Watcher) reactToComments(ctx context.Context, s domain.Session, st domain.PRWatchState, pr PRView) error {
	// Without the viewer the agent's own replies cannot be told apart, so
	// nothing is processed until it is known.
	viewer := w.viewerLogin(ctx)
	if viewer == "" {
		return nil
	}
	comments, err := w.d.GitHub.ReviewComments(ctx, sessionDir(s), pr.Number)
	if err != nil {
		return err
	}
	seen, err := w.d.State.SeenComments(ctx, s.ID)
	if err != nil {
		return err
	}
	since := st.WatchSince
	if since.IsZero() {
		since = s.CreatedAt
	}

	var processed []int64
	for _, c := range comments {
		if seen[c.ID] {
			continue
		}
		processed = append(processed, c.ID)
		if (c.InReplyTo != 0 && c.Author == viewer) || c.CreatedAt.Before(since) {
			continue
		}
		if err := w.addReviewTodo(ctx, s, pr, c); err != nil {
			return err
		}
	}
	if len(processed) == 0 {
		return nil
	}
	// Seen is written after the to-dos: a crash in between re-reads at most
	// this batch, and the open-Ref check keeps that from duplicating.
	return w.d.State.MarkCommentsSeen(ctx, s.ID, processed)
}

func (w *Watcher) addReviewTodo(ctx context.Context, s domain.Session, pr PRView, c ReviewComment) error {
	ref := ReviewRef(c.ID)
	where := c.Path
	if c.Line != 0 {
		where = fmt.Sprintf("%s:%d", c.Path, c.Line)
	}
	added := false
	err := w.d.Todos.Mutate(ctx, s.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		if _, open := openTodoByRef(*l, ref); open {
			return nil
		}
		l.Items = append(l.Items, domain.TodoItem{
			Text:  "Address review: " + reviewTitle(c.Body) + " (" + where + ")",
			State: domain.TodoPending,
			Ref:   ref,
		})
		added = true
		return nil
	})
	if err != nil || !added {
		return err
	}
	list, err := w.d.Todos.Get(s.ID)
	if err != nil {
		return err
	}
	item, _ := openTodoByRef(list, ref)
	note := fmt.Sprintf("[aiman] New review comment on PR #%d at %s by @%s, to-do %s:\n%s", pr.Number, where, c.Author, item.ID, truncateBody(c.Body))
	w.d.Steer(s.ID, domain.AuthorAiman, note)
	return nil
}

// reviewTitle is the first non-empty line of a comment, cut to 80 runes.
func reviewTitle(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > reviewTitleRunes {
			return string([]rune(line)[:reviewTitleRunes]) + "…"
		}
		return line
	}
	return emptyReviewTitle
}

// truncateBody cuts body to reviewBodyBytes on a rune boundary.
func truncateBody(body string) string {
	if len(body) <= reviewBodyBytes {
		return body
	}
	cut := reviewBodyBytes
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut] + "\n[truncated]"
}
