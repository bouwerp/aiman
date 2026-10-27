package domain

import "time"

// PRWatchState is what serve remembers about one session's watched PR between
// polls.
type PRWatchState struct {
	SessionID    string
	PRNumber     int
	PRState      string
	HeadSHA      string
	WatchSince   time.Time // comments created before this are history, not news
	LastPolledAt time.Time
	LastError    string   // last gh failure; shown by ci.get, never a to-do
	Escalated    []string // check names already escalated, sorted
}
