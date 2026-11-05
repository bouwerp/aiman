package coordinator

import (
	"context"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

// trackFixer watches a fixer: Ref. review.push settles a fixer that pushes, so
// this only gives up on one that never will. A fixer has no PR of its own, so
// none is probed. A row that is gone is unlinked without a retire.
func (t *Tracker) trackFixer(ctx context.Context, parent domain.Session, it domain.TodoItem, fixerID string, byID map[string]domain.Session) {
	child, ok := byID[fixerID]
	if !ok {
		t.failFixer(ctx, parent, it, fixerID)
		return
	}
	if !t.fixerGone(ctx, child) || !t.failFixer(ctx, parent, it, fixerID) {
		return
	}
	if t.Retire != nil {
		t.Retire(ctx, child)
	}
}

// fixerGone reports whether a fixer will never push: its agent ended, its
// terminal is gone, or it ran past FixTimeout.
func (t *Tracker) fixerGone(ctx context.Context, child domain.Session) bool {
	if child.AgentEnded || (t.Alive != nil && !t.Alive(ctx, child)) {
		return true
	}
	return t.FixTimeout > 0 && !child.CreatedAt.IsZero() && t.now().Sub(child.CreatedAt) > t.FixTimeout
}

// failFixer unlinks the item, which stays pending, records review.fix_failed on
// the parent's ledger and tells the parent. It reports whether the item was
// still linked, so a failure is announced once.
func (t *Tracker) failFixer(ctx context.Context, parent domain.Session, it domain.TodoItem, fixerID string) bool {
	if !t.settle(ctx, parent, it, func(item *domain.TodoItem) { item.Ref = "" }) {
		return false
	}
	if t.Ledger != nil {
		t.Ledger(ctx, parent.ID, domain.EventReviewFixFailed, map[string]any{"fixer": fixerID, "todo": it.ID})
	}
	if t.Steer != nil {
		t.Steer(parent.ID, fmt.Sprintf("[aiman] fixer %s for %s stopped without pushing. Its fix tree stays until the next round; run aiman review fix again or carry on without it.", fixerID, it.ID))
	}
	return true
}
