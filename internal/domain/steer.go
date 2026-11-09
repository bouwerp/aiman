package domain

import "time"

// SteerRecord is a queued steering note for a session: a change to the
// agent's to-do list or a free-text prompt, kept until it is delivered.
type SteerRecord struct {
	ID        int64
	SessionID string
	Author    TodoAuthor
	// From is the sending session of a peer note; empty for user and aiman
	// notes.
	From        string `json:"from,omitempty"`
	Text        string
	CreatedAt   time.Time
	DeliveredAt time.Time
}
