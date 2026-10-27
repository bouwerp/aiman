package domain

import "time"

// TokenCounts is one span of model token use.
type TokenCounts struct {
	Input        int64 `json:"input"`  // uncached input only
	Output       int64 `json:"output"` // includes thinking/reasoning
	CacheRead    int64 `json:"cache_read"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
}

// Add returns the field-wise sum of t and o.
func (t TokenCounts) Add(o TokenCounts) TokenCounts {
	return TokenCounts{
		Input:        t.Input + o.Input,
		Output:       t.Output + o.Output,
		CacheRead:    t.CacheRead + o.CacheRead,
		CacheWrite5m: t.CacheWrite5m + o.CacheWrite5m,
		CacheWrite1h: t.CacheWrite1h + o.CacheWrite1h,
	}
}

// CacheCreation is the 5-minute write count plus the 1-hour write count.
func (t TokenCounts) CacheCreation() int64 {
	return t.CacheWrite5m + t.CacheWrite1h
}

// IsZero reports whether every count is zero.
func (t TokenCounts) IsZero() bool {
	return t == TokenCounts{}
}

// CacheHitRatio is CacheRead / (Input + CacheRead + CacheCreation).
// The bool is false when the denominator is zero.
func (t TokenCounts) CacheHitRatio() (float64, bool) {
	denom := t.Input + t.CacheRead + t.CacheCreation()
	if denom == 0 {
		return 0, false
	}
	return float64(t.CacheRead) / float64(denom), true
}

// UsageRecord is one parsed usage event.
// An empty ID is never deduplicated.
type UsageRecord struct {
	ID     string
	Model  string
	At     time.Time
	Tokens TokenCounts
}

// UsageBucket is tokens rolled up for one session, UTC hour, and model.
type UsageBucket struct {
	SessionID string
	Hour      time.Time // UTC, truncated
	Model     string
	Tokens    TokenCounts
}

// UsageCursor is a parser's position in one transcript file.
type UsageCursor struct {
	SessionID   string
	Path        string
	Fingerprint string
	Offset      int64
	Size        int64
	ModTime     time.Time
	State       string // parser state, opaque JSON (Codex); "" for Claude
}

// UsageSessionMeta is the session identity copied onto usage rows.
// LastInputTokens is the latest turn's context occupancy, not a sum of turns.
// ContextWindow and ContextSource are the window chosen for that turn.
// HasContext marks a write that observed a turn (or a full reread with none).
// A write without it leaves the three occupancy columns unchanged.
// Health fields mirror usage.Health without importing it here.
// HasHealth marks a write that observed tool records. A write without it
// leaves the health columns unchanged. HasContext and HasHealth are not stored.
type UsageSessionMeta struct {
	SessionID              string
	Name                   string
	Group                  string
	IssueKey               string
	AgentName              string
	LastInputTokens        int64
	ContextWindow          int64
	ContextSource          string
	HasContext             bool
	HealthKnown            bool
	HealthToolCalls        int
	HealthReadCalls        int
	HealthEditCalls        int
	HealthEditsWithoutRead int
	HealthFirstTestAt      time.Time
	HealthRetries          int
	HealthReads            string // JSON array of normalised read paths
	HasHealth              bool
}
