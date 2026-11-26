// Package feedcollect is the event feed's collector: the sources that read
// aiman, GitHub and Slack, and the loop that stores what they return in
// ~/.aiman/feed.db. It is the only writer of events and cursors there.
package feedcollect

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feeddb"
)

// DefaultRetention is how long events are kept when Options.Retention is 0.
const DefaultRetention = 30 * 24 * time.Hour

// pruneEvery is how often Run prunes old events.
const pruneEvery = time.Hour

// Source reads one system. Poll returns processed events (redacted, threaded,
// classified, enriched) and the new cursors; it never stores anything.
type Source interface {
	Name() domain.FeedSource
	Poll(ctx context.Context, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string, error)
	Every() time.Duration
}

// Options configure a Collector. Sources holds every source the collector
// runs: aiman and GitHub (NewAimanSource, NewGitHubSource) and, when the
// member has a Slack login, the Slack source (NewSlackSource).
type Options struct {
	DB        *feeddb.Writer
	Sources   []Source
	Now       func() time.Time
	Retention time.Duration
	Log       func(format string, args ...any)
}

// Collector polls its sources and writes their events and cursors.
type Collector struct {
	db        *feeddb.Writer
	sources   map[domain.FeedSource]Source
	order     []domain.FeedSource
	now       func() time.Time
	retention time.Duration
	log       func(format string, args ...any)

	mu      sync.Mutex
	lastOK  map[domain.FeedSource]time.Time
	calls   map[domain.FeedSource]int
	lastErr map[domain.FeedSource]string
	polling map[domain.FeedSource]*sync.Mutex
}

// New builds a collector. It refuses a nil database and duplicate sources.
func New(o Options) (*Collector, error) {
	if o.DB == nil {
		return nil, errors.New("feedcollect: no feed database")
	}
	c := &Collector{
		db:        o.DB,
		sources:   map[domain.FeedSource]Source{},
		now:       o.Now,
		retention: o.Retention,
		log:       o.Log,
		lastOK:    map[domain.FeedSource]time.Time{},
		calls:     map[domain.FeedSource]int{},
		lastErr:   map[domain.FeedSource]string{},
		polling:   map[domain.FeedSource]*sync.Mutex{},
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.retention <= 0 {
		c.retention = DefaultRetention
	}
	if c.log == nil {
		c.log = func(string, ...any) {}
	}
	for _, s := range o.Sources {
		if s == nil {
			continue
		}
		name := s.Name()
		if _, dup := c.sources[name]; dup {
			return nil, fmt.Errorf("feedcollect: source %s given twice", name)
		}
		c.sources[name] = s
		c.order = append(c.order, name)
		c.polling[name] = &sync.Mutex{}
	}
	return c, nil
}

// Run polls every source at once, then each on its own Every, and prunes once
// an hour, until ctx ends. It returns ctx's error.
func (c *Collector) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, name := range c.order {
		wg.Add(1)
		go func(name domain.FeedSource) {
			defer wg.Done()
			c.loop(ctx, name)
		}(name)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.pruneLoop(ctx)
	}()
	wg.Wait()
	return ctx.Err()
}

func (c *Collector) loop(ctx context.Context, name domain.FeedSource) {
	every := c.sources[name].Every()
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_ = c.PollOnce(ctx, name) // health and the log carry the error
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) pruneLoop(ctx context.Context) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		c.prune(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) prune(ctx context.Context) {
	n, err := c.db.Prune(ctx, c.now().Add(-c.retention))
	switch {
	case err != nil && ctx.Err() == nil:
		c.log("feed: prune: %v", err)
	case n > 0:
		c.log("feed: pruned %d events", n)
	}
}

// PollOnce polls one source now and stores its events and cursors with one
// Writer.Append. A source error writes health, keeps the old cursors and is
// returned.
func (c *Collector) PollOnce(ctx context.Context, name domain.FeedSource) error {
	src, ok := c.sources[name]
	if !ok {
		return fmt.Errorf("feedcollect: no source %q", name)
	}
	lock := c.polling[name]
	lock.Lock()
	defer lock.Unlock()

	start := c.now()
	err := c.poll(ctx, src, start)
	c.recordHealth(ctx, name, start, err)
	return err
}

func (c *Collector) poll(ctx context.Context, src Source, now time.Time) error {
	name := src.Name()
	cursors, err := c.db.Cursors(ctx, name)
	if err != nil {
		return fmt.Errorf("read cursors: %w", err)
	}
	mutes, err := c.db.Mutes(ctx, now)
	if err != nil {
		return fmt.Errorf("read mutes: %w", err)
	}
	events, next, err := src.Poll(ctx, cursors, mutes)
	if err != nil {
		return err
	}
	for i := range events {
		events[i] = finish(events[i], name)
	}
	keys := make(map[feeddb.CursorKey]string, len(next))
	for k, v := range next {
		keys[feeddb.CursorKey{Source: name, Key: k}] = v
	}
	// A key the source left out of next is one it dropped (an idle thread).
	var drop []feeddb.CursorKey
	for k := range cursors {
		if _, kept := next[k]; !kept {
			drop = append(drop, feeddb.CursorKey{Source: name, Key: k})
		}
	}
	stored, err := c.db.Append(ctx, events, keys, drop...)
	if err != nil {
		return fmt.Errorf("store events: %w", err)
	}
	if len(stored) > 0 {
		c.log("feed: %s stored %d events", name, len(stored))
	}
	return nil
}

// recordHealth writes the poll's outcome and logs an error once per distinct
// text.
func (c *Collector) recordHealth(ctx context.Context, name domain.FeedSource, at time.Time, pollErr error) {
	c.mu.Lock()
	c.calls[name]++
	h := feeddb.Health{Source: name, LastPoll: at, Calls: c.calls[name]}
	if pollErr == nil {
		c.lastOK[name] = at
		if c.lastErr[name] != "" {
			c.log("feed: %s recovered", name)
		}
		c.lastErr[name] = ""
	} else {
		h.Error = pollErr.Error()
		if c.lastErr[name] != h.Error {
			c.log("feed: %s: %s", name, h.Error)
		}
		c.lastErr[name] = h.Error
	}
	h.LastOK = c.lastOK[name]
	c.mu.Unlock()
	if err := c.db.SetHealth(ctx, h); err != nil && ctx.Err() == nil {
		c.log("feed: %s: write health: %v", name, err)
	}
}
