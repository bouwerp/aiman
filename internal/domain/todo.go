package domain

import "time"

// TodoState is the lifecycle state of a to-do item.
type TodoState string

const (
	TodoPending TodoState = "pending"
	TodoActive  TodoState = "active"
	TodoDone    TodoState = "done"
	TodoDropped TodoState = "dropped"
)

// Valid reports whether s is one of the four known states.
func (s TodoState) Valid() bool {
	switch s {
	case TodoPending, TodoActive, TodoDone, TodoDropped:
		return true
	}
	return false
}

// Glyph is the one-character marker used when rendering an item.
func (s TodoState) Glyph() string {
	switch s {
	case TodoDone:
		return "✓"
	case TodoActive:
		return "▸"
	case TodoDropped:
		return "–"
	default:
		return "○"
	}
}

// TodoAuthor identifies who last changed an item.
type TodoAuthor string

const (
	AuthorUser     TodoAuthor = "user"
	AuthorAgent    TodoAuthor = "agent"
	AuthorAiman    TodoAuthor = "aiman"
	AuthorVerifier TodoAuthor = "verifier"
)

// TodoItem is one entry of a session's to-do list.
type TodoItem struct {
	// ID is "t1", "t2", ... and is never reused within a list.
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	State     TodoState  `json:"state"`
	UpdatedBy TodoAuthor `json:"updated_by,omitempty"`
	UpdatedAt time.Time  `json:"updated_at,omitempty"`
	// Ref optionally links the item to another object, prefixed:
	// "child:<id>", "check:<name>", "review:<comment id>", "finding:<review id>/<id>".
	Ref string `json:"ref,omitempty"`
}

// TodoList is a session's ordered to-do list.
type TodoList struct {
	SessionID string     `json:"session_id"`
	Items     []TodoItem `json:"items"`
	Rev       int64      `json:"rev"`
	UpdatedAt time.Time  `json:"updated_at"`
	// NotInAgent lists item ids the agent's own task list does not mirror.
	NotInAgent []string `json:"not_in_agent,omitempty"`
}

// TodoSnapshotItem is an item as reported by an agent's own task list.
type TodoSnapshotItem struct {
	Text   string    `json:"text"`
	Status TodoState `json:"status"`
}

// NormalizeVendorTodoStatus maps a vendor status string to a TodoState.
// Unknown values become pending.
func NormalizeVendorTodoStatus(s string) TodoState {
	switch s {
	case "in_progress":
		return TodoActive
	case "completed":
		return TodoDone
	}
	if st := TodoState(s); st.Valid() {
		return st
	}
	return TodoPending
}
