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
)

// ErrGateLocked wraps a change that would let a session close, drop, remove or
// edit an open gate. It wraps ErrInvalid.
var ErrGateLocked = fmt.Errorf("%w: the design gate is open and only the operator can change it", ErrInvalid)

// AddGate inserts the open gate item before the Done-when item, or at the end
// when there is none. It reports whether it added one.
func AddGate(l *domain.TodoList) bool {
	for _, it := range l.Items {
		if it.Ref == DesignGateRef {
			return false
		}
	}
	gate := domain.TodoItem{Text: DesignGateText, State: domain.TodoPending, Ref: DesignGateRef}
	at := slices.IndexFunc(l.Items, func(it domain.TodoItem) bool { return it.Ref == DoneRef })
	if at < 0 {
		at = len(l.Items)
	}
	l.Items = slices.Insert(l.Items, at, gate)
	return true
}

func gateIsOpen(it domain.TodoItem) bool {
	return it.Ref == DesignGateRef && (it.State == domain.TodoPending || it.State == domain.TodoActive)
}

// GateOpen reports whether the list holds a gate that is still pending or active.
func GateOpen(l domain.TodoList) bool {
	return slices.ContainsFunc(l.Items, gateIsOpen)
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
