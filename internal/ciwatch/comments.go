package ciwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// eventAppender is the session ledger. *sqlite.Repository, the watcher's usual
// Store, provides it.
type eventAppender interface {
	AppendSessionEvent(ctx context.Context, ev domain.SessionEvent) (int64, error)
}

// trustedAssociation reports whether a comment's author has write access to the
// repository. A steered comment is typed into an agent that can push, so anyone
// else, and a missing association, is refused.
func trustedAssociation(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

// trustedLogin reports whether author is one of Deps.TrustedLogins, ignoring
// case as GitHub logins do. An empty author never matches.
func (w *Watcher) trustedLogin(author string) bool {
	if author == "" {
		return false
	}
	for _, login := range w.d.TrustedLogins {
		if strings.EqualFold(strings.TrimSpace(login), author) {
			return true
		}
	}
	return false
}

// reactToComments turns each unseen review comment from a trusted author into a
// to-do and a note. The agent's own replies and comments older than the watch
// are marked seen without steering; an untrusted author's comment is marked seen
// and recorded, also without steering.
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
	var ignored []ReviewComment
	for _, c := range comments {
		if seen[c.ID] {
			continue
		}
		processed = append(processed, c.ID)
		if (c.InReplyTo != 0 && c.Author == viewer) || c.CreatedAt.Before(since) {
			continue
		}
		if !trustedAssociation(c.Association) && !w.trustedLogin(c.Author) {
			ignored = append(ignored, c)
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
	if err := w.d.State.MarkCommentsSeen(ctx, s.ID, processed); err != nil {
		return err
	}
	// Recorded only once seen, so a failed to-do later in the batch cannot
	// repeat these rows on every retry.
	for _, c := range ignored {
		w.recordIgnoredComment(ctx, s.ID, c)
	}
	return nil
}

// recordIgnoredComment notes a comment that was refused for its author. The
// body is attacker-controlled and stays out of both the ledger and the log.
// Failing to record never fails the poll.
func (w *Watcher) recordIgnoredComment(ctx context.Context, sessionID string, c ReviewComment) {
	ledger, ok := w.d.State.(eventAppender)
	if !ok {
		logIgnoredComment(sessionID, c, "no ledger")
		return
	}
	payload, err := json.Marshal(map[string]any{"comment_id": c.ID, "author": c.Author, "association": c.Association})
	if err != nil {
		logIgnoredComment(sessionID, c, err.Error())
		return
	}
	_, err = ledger.AppendSessionEvent(ctx, domain.SessionEvent{
		SessionID: sessionID,
		At:        w.d.Now().UTC(),
		Actor:     domain.EventActorAiman,
		Kind:      domain.EventReviewCommentIgnored,
		Payload:   payload,
	})
	if err != nil {
		logIgnoredComment(sessionID, c, err.Error())
	}
}

func logIgnoredComment(sessionID string, c ReviewComment, why string) {
	log.Printf("ciwatch: session %s ignored review comment %d by %q (association %q): not on the ledger: %s", sessionID, c.ID, c.Author, c.Association, why)
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
