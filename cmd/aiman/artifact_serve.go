package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/artifact/kit"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// cfRESTBase is the Cloudflare API the cf_oauth publisher writes through.
const cfRESTBase = "https://api.cloudflare.com/client/v4"

// cfRefreshTimeout bounds one `cf auth whoami` run.
const cfRefreshTimeout = 30 * time.Second

// cfConfigPath is the cf CLI's login file under home.
func cfConfigPath(home string) string {
	return filepath.Join(home, ".config", "cloudflare", "config", "default.json")
}

// artifactAuth picks the credential kind: the configured one, else r2_key
// when the R2 key file exists, else cf_oauth when the cf config exists. The
// reason is set when it returns "".
func artifactAuth(cfg config.ArtifactsConfig, dir, home string) (string, string) {
	switch a := strings.TrimSpace(cfg.Auth); a {
	case config.ArtifactsAuthCFOAuth, config.ArtifactsAuthR2Key:
		return a, ""
	case "":
	default:
		return "", "artifacts.auth " + a + " is not cf_oauth or r2_key"
	}
	if _, err := os.Stat(filepath.Join(dir, artifactKeyFile)); err == nil {
		return config.ArtifactsAuthR2Key, ""
	}
	if _, err := os.Stat(cfConfigPath(home)); err == nil {
		return config.ArtifactsAuthCFOAuth, ""
	}
	return "", "no R2 key in " + filepath.Join(dir, artifactKeyFile) + " and no cf login in " + cfConfigPath(home)
}

// newServeArtifactPublisher builds serve's publisher for the picked
// credential kind. The reason is set when it returns nil.
func newServeArtifactPublisher(cfg config.ArtifactsConfig, dir, home string, db *sql.DB, canWrite func(owner, caller string) bool) (*artifact.Publisher, string) {
	if !cfg.Configured() {
		return nil, "artifacts.host, account_id and bucket are not all set"
	}
	auth, why := artifactAuth(cfg, dir, home)
	switch auth {
	case config.ArtifactsAuthR2Key:
		return newArtifactPublisher(cfg, dir, db, canWrite)
	case config.ArtifactsAuthCFOAuth:
		return newCFOAuthPublisher(cfg, home, db, canWrite)
	}
	return nil, why
}

// newCFOAuthPublisher publishes through the R2 REST object API with the cf
// CLI's OAuth token, renewed by `cf auth whoami`.
func newCFOAuthPublisher(cfg config.ArtifactsConfig, home string, db *sql.DB, canWrite func(owner, caller string) bool) (*artifact.Publisher, string) {
	path := cfConfigPath(home)
	if _, err := os.Stat(path); err != nil {
		return nil, "artifacts.auth is cf_oauth but there is no cf login: " + err.Error()
	}
	cf, err := exec.LookPath("cf")
	if err != nil {
		return nil, "artifacts.auth is cf_oauth but cf is not on PATH"
	}
	store, err := artifact.OpenSQLStore(db)
	if err != nil {
		return nil, err.Error()
	}
	kitFiles, err := artifactKit(kit.FS)
	if err != nil {
		return nil, "theme kit: " + err.Error()
	}
	account := strings.TrimSpace(cfg.AccountID)
	token := artifact.CFOAuthToken(path, cfRefresher(cf, account))
	return &artifact.Publisher{
		Bucket:   artifact.NewCFObjects(cfRESTBase, account, strings.TrimSpace(cfg.Bucket), token, nil),
		Store:    store,
		Host:     strings.TrimSpace(cfg.Host),
		Kit:      kitFiles,
		CanWrite: canWrite,
	}, ""
}

// cfRefresher runs `cf auth whoami`, which renews an expiring OAuth token in
// the cf config file. One run at a time; its output is discarded because it
// names the account's user.
func cfRefresher(cf, account string) func(ctx context.Context) error {
	var mu sync.Mutex
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		ctx, cancel := context.WithTimeout(ctx, cfRefreshTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, cf, "auth", "whoami")
		cmd.Env = append(os.Environ(), "CLOUDFLARE_ACCOUNT_ID="+account)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
}
