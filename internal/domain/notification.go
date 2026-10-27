package domain

import "time"

// NotifyKind identifies a notification event type.
type NotifyKind string

const (
	NotifyWaitingInput NotifyKind = "waiting_input"
	NotifyEnded        NotifyKind = "ended"
	NotifyError        NotifyKind = "error"
	NotifyCIEscalation NotifyKind = "ci_escalation"
	NotifyReviewDone   NotifyKind = "review_done"
)

// AllNotifyKinds returns the supported notification kinds in order.
func AllNotifyKinds() []NotifyKind {
	return []NotifyKind{NotifyWaitingInput, NotifyEnded, NotifyError, NotifyCIEscalation, NotifyReviewDone}
}

// NeedsYou reports whether the user must act.
func NeedsYou(k NotifyKind) bool {
	switch k {
	case NotifyWaitingInput, NotifyError, NotifyCIEscalation, NotifyReviewDone:
		return true
	default:
		return false
	}
}

// EventLabel returns the human label for a notification kind.
func EventLabel(k NotifyKind) string {
	switch k {
	case NotifyWaitingInput:
		return "waiting for input"
	case NotifyEnded:
		return "ended"
	case NotifyError:
		return "error"
	case NotifyCIEscalation:
		return "CI needs you"
	case NotifyReviewDone:
		return "review done"
	default:
		return string(k)
	}
}

// Notification is a persisted notification row.
type Notification struct {
	Seq       int64
	SessionID string
	Kind      NotifyKind
	Label     string
	Host      string
	Text      string
	At        time.Time
}
