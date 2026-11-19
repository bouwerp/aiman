package usage

import (
	"context"
	"sort"
	"strings"
	"time"
)

// SeenModels lists the model ids the host's transcripts have used since
// since, newest use first. A "@fast" suffix is dropped: it is a speed, not a
// model. prefix keeps only ids that start with it; empty keeps all.
func (r Reporter) SeenModels(ctx context.Context, since time.Time, prefix string) ([]string, error) {
	if r.Store == nil {
		return nil, nil
	}
	buckets, err := r.Store.UsageBuckets(ctx, nil, since)
	if err != nil {
		return nil, err
	}
	last := map[string]time.Time{}
	for _, b := range buckets {
		if b.Hour.Before(since) {
			continue
		}
		id := strings.TrimSuffix(b.Model, "@fast")
		if id == "" || id == "<synthetic>" || !strings.HasPrefix(id, prefix) {
			continue
		}
		if b.Hour.After(last[id]) {
			last[id] = b.Hour
		}
	}
	out := make([]string, 0, len(last))
	for id := range last {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		if !last[out[i]].Equal(last[out[j]]) {
			return last[out[i]].After(last[out[j]])
		}
		return out[i] < out[j]
	})
	return out, nil
}
