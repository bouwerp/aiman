package harness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
)

// evalCoordinator is the tracker's fake world: the PR each child has, and the
// steers and notifications the tracker raised.
type evalCoordinator struct {
	tracker *coordinator.Tracker

	mu       sync.Mutex
	prs      map[string]coordinator.PRState
	steers   []string
	notified []string
}

func (c *evalCoordinator) Emit(_ context.Context, sessionID, _ string, _ domain.NotifyKind, detail string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notified = append(c.notified, sessionID+": "+detail)
	return nil
}

// EnableCoordinator makes claude the installed worker tier, so session.create
// --for can pick an agent, and attaches the production coordinator.Tracker.
// Its PRs come from SetChildPR, its reports are the child's report file on
// disk, and it steers through serve's real steerer. It runs only on TrackOnce,
// never on a timer.
func (h *Harness) EnableCoordinator() {
	h.srv.SetAgentChoice(config.AgentsConfig{Preferred: config.PreferredFromCommands("claude")},
		func(context.Context, string) bool { return true })
	c := &evalCoordinator{prs: map[string]coordinator.PRState{}}
	c.tracker = &coordinator.Tracker{
		Todos:    h.srv.TodoAccess(),
		Sessions: func() ([]domain.Session, error) { return h.Repo.List(context.Background()) },
		PR: func(_ context.Context, child domain.Session) (coordinator.PRState, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.prs[child.ID], nil
		},
		Report: func(_ context.Context, child domain.Session) (string, error) {
			raw, err := os.ReadFile(filepath.Join(child.WorkingDirectory, domain.AimanReportFileName))
			if errors.Is(err, fs.ErrNotExist) {
				// ExecReport reads a missing file as an empty report.
				return "", nil
			}
			return string(raw), err
		},
		Steer: func(id, text string) {
			c.mu.Lock()
			c.steers = append(c.steers, id+": "+text)
			c.mu.Unlock()
			h.srv.Steerer().Enqueue(server.SteerNote{SessionID: id, Author: domain.AuthorAiman, Text: text})
		},
		Notify: c,
	}
	h.coord = c
}

// SetChildPR sets the PR the tracker sees for a child session.
func (h *Harness) SetChildPR(childID string, pr coordinator.PRState) {
	h.coord.mu.Lock()
	defer h.coord.mu.Unlock()
	h.coord.prs[childID] = pr
}

// TrackOnce runs one tracker pass.
func (h *Harness) TrackOnce() {
	h.coord.tracker.TrackOnce(context.Background())
}

// Steers is every note the tracker queued, as "<session>: <text>". The
// steerer collapses notes queued close together into one typed note, so the
// pane cannot count repeats.
func (h *Harness) Steers() []string {
	h.coord.mu.Lock()
	defer h.coord.mu.Unlock()
	return append([]string(nil), h.coord.steers...)
}

// Notified is every notification the tracker raised, as "<session>: <detail>".
func (h *Harness) Notified() []string {
	h.coord.mu.Lock()
	defer h.coord.mu.Unlock()
	return append([]string(nil), h.coord.notified...)
}

// TodoItem reads one item of a session's to-do list.
func (h *Harness) TodoItem(sessionID, itemID string) (domain.TodoItem, error) {
	list, err := h.srv.TodoAccess().Get(sessionID)
	if err != nil {
		return domain.TodoItem{}, err
	}
	for _, it := range list.Items {
		if it.ID == itemID {
			return it, nil
		}
	}
	return domain.TodoItem{}, fmt.Errorf("item %s/%s not found", sessionID, itemID)
}

// SeedTodos sets a session's list as the agent, which never steers, so any
// later note is the case's.
func (h *Harness) SeedTodos(sessionID string, items ...domain.TodoItem) error {
	return h.srv.TodoAccess().Mutate(context.Background(), sessionID, domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items = append(l.Items, items...)
		return nil
	})
}
