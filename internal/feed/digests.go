package feed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/bouwerp/aiman/internal/domain"
)

// DigestStore keeps the digest events FoldDigests makes, in serve's
// feed_digests table. Digest ids are negative, so they never meet a feed.db
// id.
type DigestStore interface {
	// SaveFeedDigest stores e with the next negative id and returns it with
	// that id. members are the folded feed.db event ids.
	SaveFeedDigest(ctx context.Context, members []int64, e domain.FeedEvent) (domain.FeedEvent, error)
	// FeedDigests reads stored digests by their (negative) ids.
	FeedDigests(ctx context.Context, ids []int64) ([]domain.FeedEvent, error)
}

// DigestReader is an EventReader whose ByIDs also resolves digest ids.
type DigestReader struct {
	EventReader
	Digests DigestStore
}

// ByIDs reads positive ids from feed.db and negative ones from the digests.
func (d DigestReader) ByIDs(ctx context.Context, ids []int64) ([]domain.FeedEvent, error) {
	var pos, neg []int64
	for _, id := range ids {
		if id < 0 {
			neg = append(neg, id)
		} else {
			pos = append(pos, id)
		}
	}
	var out []domain.FeedEvent
	if len(pos) > 0 {
		evs, err := d.EventReader.ByIDs(ctx, pos)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	if len(neg) > 0 && d.Digests != nil {
		evs, err := d.Digests.FeedDigests(ctx, neg)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	return out, nil
}

// digestMembers are the events FoldDigests folded into the digest of channel.
func digestMembers(events []domain.FeedEvent, channel string) []int64 {
	var ids []int64
	for _, e := range events {
		if e.Class == domain.FeedEventClass("ambient") && !e.Urgent && e.Channel == channel {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// digestLinesPath is where a batch's digest lines are kept.
func digestLinesPath(dir string, batch int64) string {
	return filepath.Join(dir, "digest", strconv.FormatInt(batch, 10)+".json")
}

// writeDigestLines keeps the agent's digest lines of one batch, by event id.
func writeDigestLines(dir string, batch int64, lines []DigestLine) error {
	if len(lines) == 0 {
		return nil
	}
	m := map[int64]string{}
	for _, l := range lines {
		if t := cutBytes(oneLine(l.Line), digestMessageBytes); t != "" {
			m[l.Event] = t
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p := digestLinesPath(dir, batch)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// ReadDigestLines reads a batch's digest lines by event id; none when the
// batch has no file.
func ReadDigestLines(dir string, batch int64) map[int64]string {
	b, err := os.ReadFile(digestLinesPath(dir, batch))
	if err != nil {
		return nil
	}
	var m map[int64]string
	_ = json.Unmarshal(b, &m)
	return m
}
