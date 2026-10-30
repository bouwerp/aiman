package usecase

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/review"
)

// ListOpenPRs lists a repository's open pull requests via gh on the remote,
// newest first.
func ListOpenPRs(ctx context.Context, remote TerminalExecutor, slug string) ([]review.PR, error) {
	gh := &review.GHCLI{
		Run: func(ctx context.Context, name string, args ...string) (string, error) {
			parts := make([]string, 0, 1+len(args))
			parts = append(parts, name)
			for _, a := range args {
				parts = append(parts, strconv.Quote(a))
			}
			// Hide stderr so SSH CombinedOutput stays valid JSON.
			return remote.Execute(ctx, strings.Join(parts, " ")+" 2>/dev/null")
		},
	}
	prs, err := gh.ListPRs(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("list open PRs %s: %w", slug, err)
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number > prs[j].Number })
	return prs, nil
}
