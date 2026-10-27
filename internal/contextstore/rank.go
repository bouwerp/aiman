package contextstore

import (
	"fmt"
	"sort"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Superseded returns the ids named by any entry's Supersedes field.
func Superseded(all []domain.ContextEntry) map[string]bool {
	out := make(map[string]bool)
	for _, e := range all {
		if e.Supersedes != "" {
			out[e.Supersedes] = true
		}
	}
	return out
}

// Live reports whether a note is neither superseded nor expired.
func Live(e domain.ContextEntry, superseded map[string]bool, now time.Time) bool {
	if superseded[e.ID] {
		return false
	}
	return e.ValidUntil.IsZero() || e.ValidUntil.After(now)
}

// Rank sorts in place: decisions, pitfalls and conventions first, then newest,
// then id descending.
func Rank(list []domain.ContextEntry) {
	sort.Slice(list, func(i, j int) bool {
		ti, tj := rankTier(list[i]), rankTier(list[j])
		if ti != tj {
			return ti < tj
		}
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		}
		return list[i].ID > list[j].ID
	})
}

func rankTier(e domain.ContextEntry) int {
	switch e.Type {
	case domain.ContextTypeDecision, domain.ContextTypePitfall, domain.ContextTypeConvention:
		return 0
	default:
		return 1
	}
}

// Age formats the gap from created to now for compact displays.
func Age(created, now time.Time) string {
	d := now.Sub(created)
	if d < time.Minute {
		return "0m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d < 7*24*time.Hour {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	if d < 365*24*time.Hour {
		return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	}
	return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
}
