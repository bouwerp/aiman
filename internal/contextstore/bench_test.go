package contextstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// seedNotes fills s with n notes spread over five groups, three repos and the
// host namespace. One note in fifty mentions "session cookie".
func seedNotes(tb testing.TB, s *Files, n int) []domain.ContextEntry {
	tb.Helper()
	ctx := context.Background()
	words := []string{"auth", "cookie", "deploy", "cache", "parser", "review", "schema", "worker"}
	detail := strings.Repeat("context detail ", 100)
	now := time.Now().UTC().Truncate(time.Second)
	out := make([]domain.ContextEntry, 0, n)
	for i := 0; i < n; i++ {
		e := domain.ContextEntry{
			Title:     fmt.Sprintf("%s note %d", words[i%len(words)], i),
			Abstract:  fmt.Sprintf("abstract %d for %s", i, words[(i+1)%len(words)]),
			Body:      detail,
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}
		if i%50 == 0 {
			e.Body += "session cookie"
		}
		switch i % 9 {
		case 0, 1, 2, 3, 4:
			e.Namespace = domain.ContextNSGroup
			e.Key = fmt.Sprintf("g%d", i%5+1)
		case 5, 6, 7:
			e.Namespace = domain.ContextNSRepo
			e.Key = fmt.Sprintf("owner/r%d", i%3+1)
		default:
			e.Namespace = domain.ContextNSHost
			e.Key = "host"
		}
		stored, err := s.Put(ctx, e)
		if err != nil {
			tb.Fatalf("seed note %d: %v", i, err)
		}
		out = append(out, stored)
	}
	return out
}

func BenchmarkScanFind1000(b *testing.B) {
	s := NewFiles(b.TempDir())
	seedNotes(b, s, 1000)
	ctx := context.Background()
	query := domain.ContextQuery{Text: "session cookie"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Find(ctx, query); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanPackSession1000(b *testing.B) {
	s := NewFiles(b.TempDir())
	seedNotes(b, s, 1000)
	ctx := context.Background()
	query := domain.ContextQuery{Namespace: domain.ContextNSGroup, Key: "g1"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Pack(ctx, query); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanGet1000(b *testing.B) {
	s := NewFiles(b.TempDir())
	notes := seedNotes(b, s, 1000)
	ctx := context.Background()
	id := notes[len(notes)-1].ID
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Get(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}
