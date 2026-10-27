package todo

import "github.com/bouwerp/aiman/internal/domain"

const (
	// DoneRef marks the evidence-backed Done-when item. It is owned by aiman.
	DoneRef = "done"
	// DoneItemText is the fixed text of the Done-when item.
	DoneItemText = "Done-when met"
)

// EnsureDoneItem appends the Done-when item when the list has none. It reports
// whether it added one.
func EnsureDoneItem(l *domain.TodoList) bool {
	for _, it := range l.Items {
		if it.Ref == DoneRef {
			return false
		}
	}
	l.Items = append(l.Items, domain.TodoItem{
		Text:  DoneItemText,
		State: domain.TodoPending,
		Ref:   DoneRef,
	})
	return true
}

// pinDoneLast stably moves the DoneRef item to the end.
func pinDoneLast(l *domain.TodoList) {
	for i, it := range l.Items {
		if it.Ref != DoneRef {
			continue
		}
		if i == len(l.Items)-1 {
			return
		}
		copy(l.Items[i:], l.Items[i+1:])
		l.Items[len(l.Items)-1] = it
		return
	}
}
