package domain

import (
	"context"
	"time"
)

const (
	ContextNSGroup = "group"
	ContextNSRepo  = "repo"
	ContextNSHost  = "host"
)

// ContextEntry is one durable note in the shared context store.
type ContextEntry struct {
	ID        string
	Namespace string
	Key       string
	Title     string
	Abstract  string
	Body      string
	SessionID string
	CreatedAt time.Time
}

// ContextQuery selects notes. Empty fields are wildcards. Text filters
// title, abstract, and body (substring). Limit 0 means a default cap.
type ContextQuery struct {
	Namespace string
	Key       string
	Text      string
	Limit     int
}

// ContextStore is the shared context database. The filesystem implementation
// is the v1 backend; another package can satisfy this over HTTP later.
type ContextStore interface {
	Put(ctx context.Context, e ContextEntry) (ContextEntry, error)
	Get(ctx context.Context, id string) (*ContextEntry, error)
	List(ctx context.Context, q ContextQuery) ([]ContextEntry, error)
	Find(ctx context.Context, q ContextQuery) ([]ContextEntry, error)
	Pack(ctx context.Context, q ContextQuery) (string, error)
}
