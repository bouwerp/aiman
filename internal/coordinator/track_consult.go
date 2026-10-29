package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// consultLinkGrace is how long an unlinked consult is left alone: serve
// starts the consult before it links the item.
const consultLinkGrace = time.Minute

// trackConsult settles a consult's item on its report alone: a consult has
// no branch and no PR. A missing report means the consult is still working,
// until it times out, its agent ends or its terminal is gone; then the item
// is unlinked and the consult retired. A report that does not parse asks the
// coordinator once.
func (t *Tracker) trackConsult(ctx context.Context, parent domain.Session, it domain.TodoItem, child domain.Session) {
	if t.Report == nil {
		return
	}
	md, err := t.Report(ctx, child)
	if err != nil {
		log.Printf("coordinator: consult report of %s: %v", child.ID, err)
		return
	}
	var r ConsultReport
	parseErr := errEmptyReport
	if strings.TrimSpace(md) != "" {
		r, parseErr = ParseConsultReport(md)
	}
	switch {
	case parseErr == nil:
		t.finishConsult(ctx, parent, it, child, r, md)
	case t.consultGone(ctx, child):
		t.abandonConsult(ctx, parent, it, child)
	case !errors.Is(parseErr, errEmptyReport):
		t.askConsult(parent, it, child, parseErr)
	}
}

// errEmptyReport marks a consult that has not written its report yet.
var errEmptyReport = errors.New("consult report: empty")

// finishConsult keeps the report, marks the item done, records the verdict on
// the parent's ledger, points the coordinator at the kept report and retires
// the consult. A failed keep leaves everything for the next pass.
func (t *Tracker) finishConsult(ctx context.Context, parent domain.Session, it domain.TodoItem, child domain.Session, r ConsultReport, md string) {
	path := filepath.Join(child.WorkingDirectory, domain.AimanReportFileName)
	if t.KeepReport != nil {
		kept, err := t.KeepReport(ctx, child, md)
		if err != nil {
			log.Printf("coordinator: keep consult report of %s: %v", child.ID, err)
			return
		}
		path = kept
	}
	if !t.settle(ctx, parent, it, func(item *domain.TodoItem) { item.State = domain.TodoDone }) {
		return
	}
	if t.Ledger != nil {
		t.Ledger(ctx, parent.ID, domain.EventConsultFinished, map[string]any{
			"child": child.ID, "todo": it.ID,
			"high": r.High, "medium": r.Medium, "low": r.Low, "verdict": r.Verdict,
		})
	}
	if t.Steer != nil {
		t.Steer(parent.ID, fmt.Sprintf("[aiman] consult %s for %s: verdict %s, %d high, %d medium, %d low. Report: %s. Triage every objection in the design's Decisions table.",
			child.ID, it.ID, r.Verdict, r.High, r.Medium, r.Low, path))
	}
	if t.Retire != nil {
		t.Retire(ctx, child)
	}
}

// consultGone reports whether a consult without a usable report will never
// write one: its agent ended, its terminal is gone, or it ran past
// ConsultTimeout.
func (t *Tracker) consultGone(ctx context.Context, child domain.Session) bool {
	if child.AgentEnded || (t.Alive != nil && !t.Alive(ctx, child)) {
		return true
	}
	return t.ConsultTimeout > 0 && !child.CreatedAt.IsZero() && t.now().Sub(child.CreatedAt) > t.ConsultTimeout
}

// abandonConsult unlinks the item, which stays pending, retires the consult
// and tells the coordinator.
func (t *Tracker) abandonConsult(ctx context.Context, parent domain.Session, it domain.TodoItem, child domain.Session) {
	if !t.settle(ctx, parent, it, func(item *domain.TodoItem) { item.Ref = "" }) {
		return
	}
	if t.Retire != nil {
		t.Retire(ctx, child)
	}
	if t.Steer != nil {
		t.Steer(parent.ID, fmt.Sprintf("[aiman] consult %s for %s produced no report; ask again or proceed without it.", child.ID, it.ID))
	}
}

// askConsult tells the coordinator once that the consult's report does not
// parse, so it can ask the consult to fix it.
func (t *Tracker) askConsult(parent domain.Session, it domain.TodoItem, child domain.Session, why error) {
	if !t.firstAsk(parent.ID + "/" + it.ID + "/" + child.ID) {
		return
	}
	log.Printf("coordinator: consult report of %s: %v", child.ID, why)
	if t.Steer == nil {
		return
	}
	t.Steer(parent.ID, fmt.Sprintf("[aiman] consult %s for %s wrote a report that does not parse (%v); ask it to fix %s: ## Objections with [high], [medium] or [low] bullets, and ## Verdict starting approve, revise or reject.",
		child.ID, it.ID, why, domain.AimanReportFileName))
}

// sweepConsults retires every consult whose parent's list was read and holds
// no open item linking it, once it is older than consultLinkGrace. That is a
// consult whose item was settled, dropped or unlinked without a retire, or
// one serve started but never linked.
func (t *Tracker) sweepConsults(ctx context.Context, sessions []domain.Session, linked, listed map[string]bool) {
	if t.Retire == nil {
		return
	}
	for _, s := range sessions {
		if s.Role != domain.SessionRoleConsult || linked[s.ID] || !listed[s.ParentID] {
			continue
		}
		if !s.CreatedAt.IsZero() && t.now().Sub(s.CreatedAt) < consultLinkGrace {
			continue
		}
		log.Printf("coordinator: retire consult %s: no open to-do links it", s.ID)
		t.Retire(ctx, s)
	}
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}
