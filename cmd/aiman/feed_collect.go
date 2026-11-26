package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bouwerp/aiman/internal/feedcollect"
	"github.com/bouwerp/aiman/internal/feeddb"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"gopkg.in/yaml.v3"
)

// feedCollectConfig is the part of the pushed `feed` block the collector
// reads.
type feedCollectConfig struct {
	Feed struct {
		Enabled   *bool  `yaml:"enabled"`
		Retention string `yaml:"retention"`
	} `yaml:"feed"`
}

// runFeedCollect is `aiman feed collect` (hidden): the feed collector daemon.
// It runs until SIGINT or SIGTERM, and exits 0 at once when feed.enabled is
// not true.
func runFeedCollect(args []string) error {
	if len(args) > 0 {
		if args[0] == "--help" || args[0] == "-h" {
			fmt.Println("usage: aiman feed collect (run by aiman-feed.service)")
			return nil
		}
		return fmt.Errorf("aiman feed collect takes no arguments")
	}
	dir, err := config.GetDir()
	if err != nil {
		return err
	}
	cfg, err := readFeedCollectConfig(filepath.Join(dir, config.ConfigName))
	if err != nil {
		return err
	}
	if cfg.Feed.Enabled == nil || !*cfg.Feed.Enabled {
		log.Printf("feed: feed.enabled is not true; the collector does not run")
		return nil
	}
	retention, err := parseFeedRetention(cfg.Feed.Retention)
	if err != nil {
		return fmt.Errorf("feed.retention: %w", err)
	}

	db, err := feeddb.OpenWriter(feeddb.Path(dir))
	if err != nil {
		return err
	}
	defer db.Close()

	sock := server.SocketPath(dir)
	sources := []feedcollect.Source{
		feedcollect.NewAimanSource(feedServeRPC(sock), true),
		feedcollect.NewGitHubSource(feedGH),
		// The Slack source (P3b) joins here when the member has a Slack login.
	}
	c, err := feedcollect.New(feedcollect.Options{
		DB: db, Sources: sources, Retention: retention, Log: log.Printf,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("feed: collector started (retention %s)", retention)
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Printf("feed: collector stopped")
	return nil
}

func readFeedCollectConfig(path string) (feedCollectConfig, error) {
	var cfg feedCollectConfig
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// parseFeedRetention reads a retention such as 30d or 720h; empty is the
// default.
func parseFeedRetention(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return feedcollect.DefaultRetention, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid retention %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid retention %q", s)
	}
	return d, nil
}

// feedServeRPC calls serve's socket as the operator (no caller), whatever
// AIMAN_ID the collector's environment holds.
func feedServeRPC(sock string) feedcollect.RPCFunc {
	client := server.Client{Socket: sock}
	return func(ctx context.Context, method string, params any, out any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Stat(sock); err != nil {
			return fmt.Errorf("%w: %s", server.ErrServerNotRunning, sock)
		}
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		resp, err := client.CallRaw(method, raw)
		if err != nil {
			return err
		}
		if resp.Error != nil {
			return resp.Error
		}
		b, err := json.Marshal(resp.Result)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
}

// feedGH runs the host's gh and returns its stdout even on a non-zero exit,
// because `gh api --include` prints a 304's status line before it exits 1.
func feedGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if msg := strings.TrimSpace(string(exit.Stderr)); msg != "" {
			err = fmt.Errorf("%w: %s", err, strings.SplitN(msg, "\n", 2)[0])
		}
	}
	return out, err
}
