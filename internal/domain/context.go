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

// ContextOpStat is cumulative timing for one store method (get, find, pack, …).
type ContextOpStat struct {
	Count  int64   `json:"count"`
	Errors int64   `json:"errors"`
	LastMs float64 `json:"last_ms"`
	AvgMs  float64 `json:"avg_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	LastAt string  `json:"last_at,omitempty"`
}

// ContextStats is a snapshot of store size and recent lookup traffic.
type ContextStats struct {
	Notes       int                      `json:"notes"`
	Bytes       int64                    `json:"bytes"`
	Namespaces  map[string]int           `json:"namespaces"`
	Ops         map[string]ContextOpStat `json:"ops"`
	CollectedAt time.Time                `json:"collected_at"`
}

// ContextStore is the shared context database. The filesystem implementation
// is the v1 backend; another package can satisfy this over HTTP later.
type ContextStore interface {
	Put(ctx context.Context, e ContextEntry) (ContextEntry, error)
	Get(ctx context.Context, id string) (*ContextEntry, error)
	List(ctx context.Context, q ContextQuery) ([]ContextEntry, error)
	Find(ctx context.Context, q ContextQuery) ([]ContextEntry, error)
	Pack(ctx context.Context, q ContextQuery) (string, error)
	Stats(ctx context.Context) (ContextStats, error)
}
