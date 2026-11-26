package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestArtifactAuthPick(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	cfg := config.ArtifactsConfig{}
	if a, why := artifactAuth(cfg, dir, home); a != "" || why == "" {
		t.Fatalf("nothing present: %q %q", a, why)
	}
	cf := cfConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(cf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cf, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a, _ := artifactAuth(cfg, dir, home); a != config.ArtifactsAuthCFOAuth {
		t.Fatalf("cf config only: %q", a)
	}
	if err := os.WriteFile(filepath.Join(dir, artifactKeyFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if a, _ := artifactAuth(cfg, dir, home); a != config.ArtifactsAuthR2Key {
		t.Fatalf("both present: %q", a)
	}
	cfg.Auth = config.ArtifactsAuthCFOAuth
	if a, _ := artifactAuth(cfg, dir, home); a != config.ArtifactsAuthCFOAuth {
		t.Fatalf("configured cf_oauth: %q", a)
	}
	cfg.Auth = "bogus"
	if a, why := artifactAuth(cfg, dir, home); a != "" || why == "" {
		t.Fatalf("bogus: %q %q", a, why)
	}
}
