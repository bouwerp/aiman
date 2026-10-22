package ui

import (
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// todoSummary is one session's progress as todo.summary reports it.
// Done and Total do not include dropped items.
type todoSummary struct {
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	Active string `json:"active"`
	Rev    int64  `json:"rev"`
}

// todoSnapshot is a session's to-do list plus its steering queue.
// HasList is set when the list has items or Rev > 0. Rev 0 with no items
// is a session that never had a list, which todo.summary leaves out; a
// later Rev with an empty item slice is a list that was cleared.
type todoSnapshot struct {
	List    domain.TodoList
	Steer   server.SteerStatus
	HasList bool
}

func newTodoSnapshot(list domain.TodoList, st server.SteerStatus) todoSnapshot {
	return todoSnapshot{
		List:    list,
		Steer:   st,
		HasList: len(list.Items) > 0 || list.Rev > 0,
	}
}

// todoProgress counts done and total the same way as todo.summary:
// a dropped item is in neither number. Any other state counts toward total.
func todoProgress(l domain.TodoList) (done, total int) {
	for _, it := range l.Items {
		if it.State == domain.TodoDropped {
			continue
		}
		if it.State == domain.TodoDone {
			done++
		}
		total++
	}
	return done, total
}

// promptMarker reports how a prompt sent at sentAt stands against the queue.
// A zero sentAt means nothing was sent. Held "ended" means the agent will
// not take the note. Otherwise an empty queue whose last delivery is not
// older than the send (Before is false, so an equal time counts) is
// delivered; anything else is still queued.
func promptMarker(sentAt time.Time, st server.SteerStatus) string {
	if sentAt.IsZero() {
		return ""
	}
	if st.Held == "ended" {
		return "not delivered"
	}
	if st.Queued == 0 && !st.LastDeliveredAt.Before(sentAt) {
		return "delivered"
	}
	return "queued"
}
