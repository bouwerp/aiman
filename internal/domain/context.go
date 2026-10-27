package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ContextNSGroup = "group"
	ContextNSRepo  = "repo"
	ContextNSHost  = "host"
)

// ContextType is the kind of note a context entry holds.
type ContextType string

const (
	ContextTypeDecision   ContextType = "decision"
	ContextTypePitfall    ContextType = "pitfall"
	ContextTypeConvention ContextType = "convention"
	ContextTypeHabit      ContextType = "habit"
	ContextTypeFact       ContextType = "fact"
	ContextTypeHandover   ContextType = "handover"
	ContextTypeReview     ContextType = "review"
	ContextTypeSummary    ContextType = "summary"
)

// ParseContextType is case-insensitive. An empty value reads as fact.
func ParseContextType(s string) (ContextType, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "fact":
		return ContextTypeFact, true
	case "decision":
		return ContextTypeDecision, true
	case "pitfall":
		return ContextTypePitfall, true
	case "convention":
		return ContextTypeConvention, true
	case "habit":
		return ContextTypeHabit, true
	case "handover":
		return ContextTypeHandover, true
	case "review":
		return ContextTypeReview, true
	case "summary":
		return ContextTypeSummary, true
	default:
		return "", false
	}
}

// ParseContextValidUntil accepts an RFC3339 timestamp, a YYYY-MM-DD date, or
// a day duration such as 30d. Empty means no expiry.
func ParseContextValidUntil(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err == nil && days >= 0 {
			return now.UTC().Add(time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, nil
	}
	if at, err := time.Parse("2006-01-02", value); err == nil {
		return at.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid valid_until %q", value)
}

// ContextAuthor records who wrote a note.
type ContextAuthor string

const (
	ContextAuthorAgent    ContextAuthor = "agent"
	ContextAuthorHuman    ContextAuthor = "human"
	ContextAuthorAiman    ContextAuthor = "aiman"
	ContextAuthorVerifier ContextAuthor = "verifier"
	ContextAuthorUnknown  ContextAuthor = "unknown"
)

// ContextConfidence records how strongly a note is held.
type ContextConfidence string

const (
	ContextConfidenceLow    ContextConfidence = "low"
	ContextConfidenceMedium ContextConfidence = "medium"
	ContextConfidenceHigh   ContextConfidence = "high"
)

// ContextEntry is one durable note in the shared context store.
type ContextEntry struct {
	ID         string
	Namespace  string
	Key        string
	Title      string
	Abstract   string
	Body       string
	SessionID  string
	Type       ContextType
	Supersedes string
	ValidUntil time.Time
	CreatedAt  time.Time
	Author     ContextAuthor
	Verified   bool
	VerifiedAt time.Time
	Source     string
	SourceHash string
	Confidence ContextConfidence
	Evidence   string
}

// ContextHit is the compact shape returned by context.find.
type ContextHit struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Abstract string `json:"abstract"`
	Age      string `json:"age"`
	Status   string `json:"status"`
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
