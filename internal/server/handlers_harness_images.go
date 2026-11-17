package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/todo"
)

// HarnessImageApproveTimeout bounds harness.image_approve: verify, fetch
// again when the quarantine is gone, docker load.
const HarnessImageApproveTimeout = images.ApproveTimeout

// harnessImageParams are the parameters of the harness image methods.
type harnessImageParams struct {
	Digest     string `json:"digest"`
	Ref        string `json:"ref"`
	Quote      string `json:"quote"`
	AcceptRisk bool   `json:"accept_risk"`
	Reason     string `json:"reason"`
}

// dispatchHarnessImage answers harness.image_status and harness.image_approve.
func (s *Server) dispatchHarnessImage(ctx context.Context, req Request) Response {
	if s.harness == nil || s.harness.d.Images == nil {
		return errResp(req.ID, CodeUnavailable, "image vetting is not enabled on this serve (harness.enabled, or no docker)")
	}
	var p harnessImageParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if req.Method == "harness.image_status" {
		return s.handleHarnessImageStatus(req, p)
	}
	return s.handleHarnessImageApprove(ctx, req, p)
}

// handleHarnessImageStatus answers one record (by digest or ref) or every
// record. The quote and the override reason stay in the store and the ledger.
func (s *Server) handleHarnessImageStatus(req Request, p harnessImageParams) Response {
	v := s.harness.d.Images
	key := strings.TrimSpace(p.Digest)
	if key == "" {
		key = strings.TrimSpace(p.Ref)
	}
	if key == "" {
		recs, err := v.List()
		if err != nil {
			return errResp(req.ID, CodeUnavailable, err.Error())
		}
		out := make([]images.Record, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.View())
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "harness_images", "images": out}}
	}
	rec, err := v.Status(key)
	if errors.Is(err, images.ErrNoRecord) {
		return errResp(req.ID, CodeNotFound, "no image record for "+key)
	}
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "harness_image", "image": rec.View()}}
}

// handleHarnessImageApprove approves a digest. The operator approves a
// pending digest directly, and a blocked, failed or stale one only with
// accept_risk and a reason. A session approves only a pending digest whose
// gate its own list holds, relaying the operator's words as the quote.
func (s *Server) handleHarnessImageApprove(ctx context.Context, req Request, p harnessImageParams) Response {
	digest := strings.TrimSpace(p.Digest)
	if digest == "" {
		return errResp(req.ID, CodeInvalidParams, "digest is required")
	}
	ar := images.ApproveRequest{Digest: digest, By: images.ApprovedByOperator, Approver: "operator", Quote: p.Quote}
	if scope, isSession := s.sessionScopeOf(ctx, req); isSession {
		if p.AcceptRisk {
			return errResp(req.ID, CodeNeedsOperator, "only the operator can accept the risk of a blocked, failed or stale image, from the laptop")
		}
		if r, refused := s.sessionImageApproval(req, scope.caller, digest, p.Quote); refused {
			return r
		}
		ar.By, ar.Approver = images.ApprovedBySession, s.sessionName(ctx, scope.caller)
	} else if p.AcceptRisk {
		if strings.TrimSpace(p.Reason) == "" {
			return errResp(req.ID, CodeInvalidParams, "reason is required with accept_risk")
		}
		ar.AcceptRisk, ar.Reason = true, p.Reason
	}
	ctx, cancel := context.WithTimeout(ctx, HarnessImageApproveTimeout)
	defer cancel()
	rec, err := s.harness.d.Images.Approve(ctx, ar)
	if errors.Is(err, images.ErrNoRecord) {
		return errResp(req.ID, CodeNotFound, "no image record for "+digest)
	}
	if err != nil {
		return harnessErr(req.ID, images.ApproveError(err, rec), CodeCreateFailed)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "harness_image", "image": rec.View()}}
}

// sessionImageApproval checks a session's approval: it must hold the open
// gate for the digest and relay a non-empty quote.
func (s *Server) sessionImageApproval(req Request, caller, digest, quote string) (Response, bool) {
	if caller == "" || s.todos == nil {
		return errResp(req.ID, CodeGateNeedsOperator, "only the session whose list holds the image gate, or the operator, can approve this image"), true
	}
	list, err := s.todos.Get(caller)
	if err != nil || !todo.GateOpen(list, todo.ImageGateRef(digest)) {
		return errResp(req.ID, CodeGateNeedsOperator, "this session holds no open gate for "+images.ShortDigest(digest)+"; the session that asked for the image, or the operator, approves it"), true
	}
	if strings.TrimSpace(quote) == "" {
		return errResp(req.ID, CodeInvalidParams, "quote is required: the operator's words approving the image"), true
	}
	return Response{}, false
}

func (s *Server) sessionName(ctx context.Context, id string) string {
	if list, err := s.loadSessions(ctx); err == nil {
		if sess, ok := resolveSession(list, id); ok && sess.Name != "" {
			return sess.Name
		}
	}
	return id
}

// approveClosedImageGates approves, as the operator, the digests whose gates
// the operator closed in the TUI. It runs in the background: a load takes
// longer than a to-do write.
func (s *Server) approveClosedImageGates(digests []string) {
	if s.harness == nil || s.harness.d.Images == nil || len(digests) == 0 {
		return
	}
	s.harness.bg.Add(1)
	go func() {
		defer s.harness.bg.Done()
		for _, d := range digests {
			ctx, cancel := context.WithTimeout(context.Background(), HarnessImageApproveTimeout)
			_, err := s.harness.d.Images.Approve(ctx, images.ApproveRequest{Digest: d, By: images.ApprovedByOperator, Approver: "operator"})
			cancel()
			if err != nil {
				log.Printf("harness images: approve %s from its gate: %v", images.ShortDigest(d), err)
			}
		}
	}()
}

// WaitHarnessImages waits for background approvals (tests and shutdown).
func (s *Server) WaitHarnessImages() {
	if s.harness != nil {
		s.harness.bg.Wait()
	}
}

// harnessImageNotifier turns vetting outcomes into gates, steering notes,
// harness_image notifications and ledger rows.
type harnessImageNotifier struct{ s *Server }

// HarnessImageNotifier is the notifier the image vetter reports to.
func (s *Server) HarnessImageNotifier() images.Notifier { return harnessImageNotifier{s: s} }

var _ images.Notifier = harnessImageNotifier{}

func (n harnessImageNotifier) Decided(rec images.Record) {
	ctx := context.Background()
	n.ledger(ctx, rec, domain.EventHarnessImageVetted, vettedPayload(rec, false))
	switch rec.State {
	case images.StatePendingApproval:
		for _, id := range rec.Sessions() {
			n.gate(ctx, rec, id)
		}
		if len(rec.Sessions()) == 0 {
			n.emit(ctx, "", fmt.Sprintf("image %s (%s) needs approval: %s. Approve it with aiman harness image approve %s",
				rec.Ref, images.ShortDigest(rec.Digest), images.Summary(rec), rec.Digest))
		}
	case images.StateBlocked:
		n.ledger(ctx, rec, domain.EventHarnessImageBlocked, map[string]any{"digest": rec.Digest, "ref": rec.Ref, "reasons": rec.Reasons})
		n.tell(ctx, rec, fmt.Sprintf("[aiman] image %s (%s) is blocked: %s. Report: %s. Only the operator can override it from the laptop.",
			rec.Ref, images.ShortDigest(rec.Digest), strings.Join(rec.Reasons, "; "), rec.Scan.Report))
	case images.StateFailed:
		n.tell(ctx, rec, fmt.Sprintf("[aiman] vetting image %s (%s) failed: %s. Only the operator can override it from the laptop.",
			rec.Ref, images.ShortDigest(rec.Digest), strings.Join(rec.Reasons, "; ")))
	}
}

// Joined opens the gate for a session that asks for a pending digest.
func (n harnessImageNotifier) Joined(rec images.Record, session string) {
	n.gate(context.Background(), rec, session)
}

// Approved records the approval and closes every open gate for the digest
// on every list.
func (n harnessImageNotifier) Approved(rec images.Record) {
	ctx := context.Background()
	payload := map[string]any{"digest": rec.Digest, "ref": rec.Ref, "approved_by": rec.ApprovedBy, "approver": rec.Approver,
		"quote": rec.Quote, "accepted_highs": rec.AcceptedHighs, "accept_risk": rec.AcceptRisk, "reason": rec.Reason}
	n.ledger(ctx, rec, domain.EventHarnessImageApproved, payload)
	n.closeGates(ctx, rec)
}

// Stale records the re-scan and notifies the operator; no session owns it.
func (n harnessImageNotifier) Stale(rec images.Record, newIDs []string) {
	ctx := context.Background()
	payload := vettedPayload(rec, true)
	payload["new_ids"] = newIDs
	n.s.appendEvent(ctx, domain.HarnessImageLedger, domain.EventActorAiman, domain.EventHarnessImageVetted, payload)
	n.emit(ctx, "", fmt.Sprintf("image %s (%s) is stale: a re-scan found %s. Runs that need it are refused until the operator accepts the risk or a new tag is used",
		rec.Ref, images.ShortDigest(rec.Digest), strings.Join(newIDs, ", ")))
}

func vettedPayload(rec images.Record, rescan bool) map[string]any {
	return map[string]any{
		"digest": rec.Digest, "ref": rec.Ref, "state": string(rec.State),
		"critical": len(rec.Scan.Critical), "high": len(rec.Scan.High), "static_hits": len(rec.Scan.StaticHits),
		"mal_ids": len(rec.Scan.MalIDs), "model_verdict": rec.Scan.ModelVerdict, "signature": rec.Signature,
		"report": rec.Scan.Report, "rescan": rescan,
	}
}

// ledger writes a row on the host-wide image ledger and on each requesting
// session's ledger.
func (n harnessImageNotifier) ledger(ctx context.Context, rec images.Record, kind domain.EventKind, payload map[string]any) {
	n.s.appendEvent(ctx, domain.HarnessImageLedger, domain.EventActorAiman, kind, payload)
	for _, id := range rec.Sessions() {
		n.s.appendEvent(ctx, id, domain.EventActorAiman, kind, payload)
	}
}

func (n harnessImageNotifier) session(ctx context.Context, id string) (domain.Session, bool) {
	list, err := n.s.loadSessions(ctx)
	if err != nil {
		return domain.Session{}, false
	}
	return resolveSession(list, id)
}

// gate adds the image gate to the session's list, steers the approval note
// and emits harness_image.
func (n harnessImageNotifier) gate(ctx context.Context, rec images.Record, id string) {
	sess, ok := n.session(ctx, id)
	if !ok || n.s.todos == nil {
		return
	}
	text := fmt.Sprintf("Image approved: %s (%s)", rec.Ref, images.ShortDigest(rec.Digest))
	if _, err := n.s.mutateQuiet(sess, func(l *domain.TodoList) error {
		todo.AddImageGate(l, rec.Digest, text)
		return nil
	}); err != nil {
		log.Printf("harness images: gate on %s: %v", id, err)
	}
	n.steer(sess.ID, fmt.Sprintf("[aiman] image %s (%s) needs approval: %s. Report: %s. Ask the operator. When they approve in this terminal, run aiman harness approve-image %s --quote \"<their words>\".",
		rec.Ref, images.ShortDigest(rec.Digest), images.Summary(rec), rec.Scan.Report, rec.Digest))
	n.emit(ctx, sess.ID, fmt.Sprintf("image %s (%s) needs approval", rec.Ref, images.ShortDigest(rec.Digest)))
}

// tell steers text to every requesting session and emits harness_image.
func (n harnessImageNotifier) tell(ctx context.Context, rec images.Record, text string) {
	ids := rec.Sessions()
	for _, id := range ids {
		n.steer(id, text)
		n.emit(ctx, id, fmt.Sprintf("image %s (%s) is %s", rec.Ref, images.ShortDigest(rec.Digest), rec.State))
	}
	if len(ids) == 0 {
		n.emit(ctx, "", strings.TrimPrefix(text, "[aiman] "))
	}
}

func (n harnessImageNotifier) steer(id, text string) {
	if n.s.steer != nil {
		n.s.steer.Enqueue(SteerNote{SessionID: id, Author: domain.AuthorAiman, Text: text})
	}
}

func (n harnessImageNotifier) emit(ctx context.Context, id, detail string) {
	rec := n.s.NotifyRecorder()
	if rec == nil {
		return
	}
	label := ""
	if id != "" {
		if sess, ok := n.session(ctx, id); ok {
			label = notify.Label(sess.IssueKey, sess.Name, sess.ID)
		}
	}
	if err := rec.Emit(ctx, id, label, domain.NotifyHarnessImage, detail); err != nil {
		log.Printf("harness images: notify: %v", err)
	}
}

// closeGates marks done every open gate for the digest on every list and
// tells those sessions to start their run again.
func (n harnessImageNotifier) closeGates(ctx context.Context, rec images.Record) {
	if n.s.todos == nil {
		return
	}
	list, err := n.s.loadSessions(ctx)
	if err != nil {
		log.Printf("harness images: close gates: %v", err)
		return
	}
	ref := todo.ImageGateRef(rec.Digest)
	by := rec.Approver
	if by == "" {
		by = rec.ApprovedBy
	}
	for _, sess := range list {
		l, err := n.s.todos.Get(sess.ID)
		if err != nil || !todo.GateOpen(l, ref) {
			continue
		}
		if _, err := n.s.mutateQuiet(sess, func(l *domain.TodoList) error {
			for i := range l.Items {
				if l.Items[i].Ref == ref && l.Items[i].State != domain.TodoDone {
					l.Items[i].State = domain.TodoDone
				}
			}
			return nil
		}); err != nil {
			log.Printf("harness images: close gate on %s: %v", sess.ID, err)
			continue
		}
		n.steer(sess.ID, fmt.Sprintf("[aiman] image %s (%s) approved by %s. Run aiman harness up again.", rec.Ref, images.ShortDigest(rec.Digest), by))
	}
}
