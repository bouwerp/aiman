package todo

import (
	"fmt"
	"slices"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// DesignGateRef marks the design-approval gate item. Only the operator can
	// close it, and delegation is refused while it is open.
	DesignGateRef = "gate:design"
	// DesignGateText is the fixed text of the gate item.
	DesignGateText = "Design approved"
	// ReviewGateRef marks the review-approval gate item. Only the operator can
	// close it; unlike the design gate it does not block delegation.
	ReviewGateRef = "gate:review"
	// ReviewGateText is the fixed text of the review gate item.
	ReviewGateText = "Review approved"
)

// gateTexts maps each gate ref to its fixed item text.
var gateTexts = map[string]string{
	DesignGateRef: DesignGateText,
	ReviewGateRef: ReviewGateText,
}

// ErrGateLocked wraps a change that would let a session close, drop, remove or
// edit an open gate. It wraps ErrInvalid.
var ErrGateLocked = fmt.Errorf("%w: a gate is open and only the operator can change it", ErrInvalid)

// AddGate inserts the open gate item for ref before the Done-when item, or at
// the end when there is none. It reports whether it added one; an unknown ref
// or a list that already holds the gate adds nothing.
func AddGate(l *domain.TodoList, ref string) bool {
	text, ok := gateTexts[ref]
	if !ok {
		return false
	}
	for _, it := range l.Items {
		if it.Ref == ref {
			return false
		}
	}
	gate := domain.TodoItem{Text: text, State: domain.TodoPending, Ref: ref}
	at := slices.IndexFunc(l.Items, func(it domain.TodoItem) bool { return it.Ref == DoneRef })
	if at < 0 {
		at = len(l.Items)
	}
	l.Items = slices.Insert(l.Items, at, gate)
	return true
}

func gateIsOpen(it domain.TodoItem) bool {
	_, isGate := gateTexts[it.Ref]
	return isGate && (it.State == domain.TodoPending || it.State == domain.TodoActive)
}

// GateOpen reports whether the list holds the gate for ref, still pending or active.
func GateOpen(l domain.TodoList, ref string) bool {
	return slices.ContainsFunc(l.Items, func(it domain.TodoItem) bool { return it.Ref == ref && gateIsOpen(it) })
}

// GuardGate returns an error wrapping ErrGateLocked when prev holds an open
// gate that next drops, closes, or renames. Moving the item is allowed.
func GuardGate(prev, next domain.TodoList) error {
	for _, was := range prev.Items {
		if !gateIsOpen(was) {
			continue
		}
		i := slices.IndexFunc(next.Items, func(it domain.TodoItem) bool { return it.ID == was.ID })
		if i < 0 {
			return fmt.Errorf("%w: item %s was removed", ErrGateLocked, was.ID)
		}
		now := next.Items[i]
		if !gateIsOpen(now) || now.Text != was.Text || now.Ref != was.Ref {
			return fmt.Errorf("%w: item %s was changed", ErrGateLocked, was.ID)
		}
	}
	return nil
}

// ClampGate restores an open gate's State, Text and Ref from prev after a merge
// that may have ticked, dropped or renamed it, and keeps everything else in
// next. It returns ErrGateLocked only when the gate item is gone.
func ClampGate(prev domain.TodoList, next *domain.TodoList) error {
	for _, was := range prev.Items {
		if !gateIsOpen(was) {
			continue
		}
		i := slices.IndexFunc(next.Items, func(it domain.TodoItem) bool { return it.ID == was.ID })
		if i < 0 {
			return fmt.Errorf("%w: item %s was removed", ErrGateLocked, was.ID)
		}
		next.Items[i].State, next.Items[i].Text, next.Items[i].Ref = was.State, was.Text, was.Ref
	}
	return nil
}
