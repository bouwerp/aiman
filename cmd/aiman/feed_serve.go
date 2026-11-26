package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	harnessscanner "github.com/bouwerp/aiman/build/harness-scanner"
	"github.com/bouwerp/aiman/internal/agentjob"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/feed"
	"github.com/bouwerp/aiman/internal/feeddb"
	"github.com/bouwerp/aiman/internal/harness/images"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/slack"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/server"
)

// lazyFeedDB opens feed.db on first use and again after a failure, so serve
// can start before the collector has created the file. Until then every
// method answers feeddb.ErrNoDatabase.
type lazyFeedDB struct {
	path string
	mu   sync.Mutex
	rd   *feeddb.Reader
	mw   *feeddb.MuteWriter
}

func (l *lazyFeedDB) handles() (*feeddb.Reader, *feeddb.MuteWriter, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rd != nil && l.mw != nil {
		return l.rd, l.mw, nil
	}
	if l.rd == nil {
		rd, err := feeddb.OpenReader(l.path)
		if err != nil {
			return nil, nil, err
		}
		l.rd = rd
	}
	mw, err := feeddb.OpenMuteWriter(l.path)
	if err != nil {
		return nil, nil, err
	}
	l.mw = mw
	return l.rd, l.mw, nil
}

func (l *lazyFeedDB) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rd != nil {
		_ = l.rd.Close()
	}
	if l.mw != nil {
		_ = l.mw.Close()
	}
}

func (l *lazyFeedDB) After(ctx context.Context, afterID int64, limit int) ([]domain.FeedEvent, error) {
	rd, _, err := l.handles()
	if err != nil {
		return nil, err
	}
	return rd.After(ctx, afterID, limit)
}

func (l *lazyFeedDB) ByIDs(ctx context.Context, ids []int64) ([]domain.FeedEvent, error) {
	rd, _, err := l.handles()
	if err != nil {
		return nil, err
	}
	return rd.ByIDs(ctx, ids)
}

func (l *lazyFeedDB) Thread(ctx context.Context, key string, beforeID int64, limit int) ([]domain.FeedEvent, error) {
	rd, _, err := l.handles()
	if err != nil {
		return nil, err
	}
	return rd.Thread(ctx, key, beforeID, limit)
}

func (l *lazyFeedDB) Mutes(ctx context.Context, now time.Time) ([]domain.FeedMute, error) {
	rd, _, err := l.handles()
	if err != nil {
		return nil, err
	}
	return rd.Mutes(ctx, now)
}

func (l *lazyFeedDB) Health(ctx context.Context) ([]feeddb.Health, error) {
	rd, _, err := l.handles()
	if err != nil {
		return nil, err
	}
	return rd.Health(ctx)
}

func (l *lazyFeedDB) Unmute(ctx context.Context, scope domain.FeedMuteScope, key string) error {
	_, mw, err := l.handles()
	if err != nil {
		return err
	}
	return mw.Unmute(ctx, scope, key)
}

func (l *lazyFeedDB) Mute(ctx context.Context, m domain.FeedMute) error {
	_, mw, err := l.handles()
	if err != nil {
		return err
	}
	return mw.Mute(ctx, m)
}

func (l *lazyFeedDB) SetFloor(ctx context.Context, minEventID int64) error {
	_, mw, err := l.handles()
	if err != nil {
		return err
	}
	return mw.SetFloor(ctx, minEventID)
}

// feedEvents is what the feed RPCs read: digest ids resolved, plus health.
type feedEvents struct {
	feed.DigestReader
	db *lazyFeedDB
}

func (f feedEvents) Health(ctx context.Context) ([]feeddb.Health, error) { return f.db.Health(ctx) }

// feedAgent is feed.agent and feed.model when set, else the first entry of
// feed.tier whose agent the job container can run and is on PATH.
func feedAgent(cfg *config.Config) (string, string) {
	f := cfg.Feed
	if f.Agent != "" {
		return f.Agent, f.Model
	}
	for _, e := range cfg.Agents.Tier(f.TierOrDefault()) {
		name := filepath.Base(e.Agent)
		if !agentjob.Supported(name) {
			continue
		}
		if _, err := exec.LookPath(name); err == nil {
			return name, e.Model
		}
	}
	return "", ""
}

// slackConfigured is true when a Slack login file or token env var exists.
func slackConfigured(path string) bool {
	if strings.TrimSpace(os.Getenv("AIMAN_SLACK_USER_TOKEN")) != "" {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}

// wireFeed wires the feed RPCs and, when feed.enabled, starts the manager.
// It returns a closer for the feed.db handles, or nil.
func wireFeed(ctx context.Context, srv *server.Server, cfg *config.Config, dir string, db *sqlite.Repository) func() {
	f := cfg.Feed
	if !f.EnabledOrDefault() {
		srv.SetFeed(server.FeedDeps{Settings: f})
		return nil
	}
	fdir := filepath.Join(dir, "feed")
	lazy := &lazyFeedDB{path: feeddb.Path(dir)}
	if _, _, err := lazy.handles(); errors.Is(err, feeddb.ErrNoDatabase) {
		log.Printf("feed: the collector has not started yet (no %s)", lazy.path)
	} else if err != nil {
		log.Printf("feed: open %s: %v", lazy.path, err)
	}
	events := feedEvents{DigestReader: feed.DigestReader{EventReader: lazy, Digests: db}, db: lazy}
	agent, model := feedAgent(cfg)
	bin, err := os.Executable()
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		log.Printf("feed: locate the aiman binary: %v", err)
	}
	args, warns := cfg.Harness.Images.ScannerBuildArgs()
	for _, w := range warns {
		log.Printf("config: %s", w)
	}
	image := &images.ScannerImage{Docker: images.ExecDockerCLI, Dockerfile: harnessscanner.Dockerfile, BuildArgs: args,
		Dir: filepath.Join(dir, "harness")}
	runJob := feed.NewJobRunner(agentjob.Options{
		Image: image.Ensure, AimanBin: bin, UID: os.Getuid(), GID: os.Getgid(),
	})
	groups := f.Sessions.Groups
	m, err := feed.NewManager(feed.ManagerOptions{
		Events: events, Store: db, RunJob: runJob, Dir: fdir,
		Agent: agent, Model: model,
		Interval: f.IntervalOrDefault(), UrgentGap: f.UrgentGapOrDefault(),
		DailyBatches: f.DailyBatchesOrDefault(), RunTimeout: f.RunTimeoutOrDefault(), ActionTTL: f.ActionTTLOrDefault(),
		Digests:  db,
		Context:  func(ctx context.Context) ([]feed.SessionFacts, error) { return srv.FeedContext(ctx, groups) },
		SetFloor: lazy.SetFloor,
		Gate:     srv.PressureOpen,
		OnBatch:  srv.FeedBatchChanged,
		OnAction: srv.FeedActionChanged,
		Log:      log.Printf,
	})
	if err != nil {
		log.Printf("feed unavailable: %v", err)
		lazy.Close()
		return nil
	}
	act := feed.ActDeps{Mutes: lazy, MuteDays: f.MuteDaysOrDefault(), TodoAdd: srv.FeedTodoAdd, TTL: f.ActionTTLOrDefault()}
	if slackPath := slack.TokenPath(dir); slackConfigured(slackPath) {
		client := &slack.Client{Tokens: slack.Tokens(slackPath)}
		act.SlackDraft = func(ctx context.Context, channel, threadTS, text string) (string, error) {
			d, err := client.Draft(ctx, slack.DraftRequest{Channel: channel, Thread: threadTS, Text: text})
			if err != nil {
				return "", err
			}
			return "slack draft " + d.ID + " in " + channel + " (Drafts & Sent)", nil
		}
	}
	srv.SetFeed(server.FeedDeps{Enabled: true, Store: db, Events: events, Mutes: lazy, Act: act, Memory: m.Memory(), Dir: fdir, Settings: f})
	if agent == "" {
		log.Printf("feed: no agent the job container can run in feed.agent or the %s tier; batches wait", f.TierOrDefault())
	} else {
		go m.Run(ctx)
	}
	return lazy.Close
}
