package contextstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
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
		if list[i].Verified != list[j].Verified {
			return list[i].Verified
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

// Hits renders compact, body-free results for context.find.
func Hits(list []domain.ContextEntry, now time.Time) []domain.ContextHit {
	out := make([]domain.ContextHit, 0, len(list))
	for _, e := range list {
		typ := e.Type
		if typ == "" {
			typ = domain.ContextTypeFact
		}
		out = append(out, domain.ContextHit{
			ID:       e.ID,
			Type:     string(typ),
			Title:    e.Title,
			Abstract: e.Abstract,
			Age:      Age(e.CreatedAt, now),
			Status:   hitStatus(e),
		})
	}
	return out
}

func hitStatus(e domain.ContextEntry) string {
	if e.SourceHash != "" && sourceChanged(e.Source, e.SourceHash) {
		return "stale"
	}
	if e.Verified {
		return "verified"
	}
	return "unverified"
}

func sourceChanged(source, want string) bool {
	path, ok := strings.CutPrefix(source, "file:")
	if !ok {
		return false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) != want
}
