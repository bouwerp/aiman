package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestArtifactShareParams(t *testing.T) {
	p, err := artifactShareParams("share", []string{"abc", "--email", "A@x.com", "--domain=x.com", "--email", "b@y.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": "abc", "emails": []string{"A@x.com", "b@y.com"}, "domains": []string{"x.com"}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %v", p)
	}
	if p, err = artifactShareParams("unshare", []string{"abc", "--all"}); err != nil || p["all"] != true {
		t.Fatalf("unshare --all: %v %v", p, err)
	}
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"share", []string{"abc"}},
		{"share", []string{"abc", "--all"}},
		{"unshare", []string{"abc"}},
		{"delete", []string{"abc", "--email", "a@b.c"}},
		{"delete", nil},
		{"share", []string{"a", "b", "--email", "x@y.z"}},
	} {
		if _, err := artifactShareParams(tc.verb, tc.args); err == nil {
			t.Fatalf("%s %v: no error", tc.verb, tc.args)
		}
	}
}

func TestArtifactSetupArgs(t *testing.T) {
	conf := config.ArtifactsConfig{Host: "aiman.fyi", AccountID: "acc", Bucket: "b1", Owners: []string{"me@x.com"}}
	got, err := parseArtifactSetup([]string{"--team", "cool-tree", "--idp", "g1", "--viewer", "v@y.com"}, conf)
	if err != nil {
		t.Fatal(err)
	}
	c := got.cfg
	if c.AccountID != "acc" || c.Host != "aiman.fyi" || c.Zone != "aiman.fyi" || c.Bucket != "b1" ||
		c.TeamDomain != "cool-tree.cloudflareaccess.com" || c.IdentityProvider != "g1" ||
		!reflect.DeepEqual(c.Owners, []string{"me@x.com"}) || !reflect.DeepEqual(c.Viewers, []string{"v@y.com"}) {
		t.Fatalf("cfg %+v", c)
	}
	if _, err := parseArtifactSetup([]string{"--idp", "g1"}, conf); err == nil || !strings.Contains(err.Error(), "--team") {
		t.Fatalf("missing team: %v", err)
	}
	if _, err := parseArtifactSetup([]string{"--token", "secret"}, conf); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("--token: %v", err)
	}
	if got, err := parseArtifactSetup([]string{"--host-key"}, config.ArtifactsConfig{}); err != nil || !got.hostKey {
		t.Fatalf("--host-key: %+v %v", got, err)
	}
	if _, err := parseArtifactSetup([]string{"--host-key", "--team", "t"}, conf); err == nil {
		t.Fatal("--host-key with another flag accepted")
	}
}

func TestWriteArtifactHostKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".aiman", artifactKeyFile)
	if err := writeArtifactHostKey(path, strings.NewReader(`{"access_key_id":"k"}`)); err == nil {
		t.Fatal("half key accepted")
	}
	if err := writeArtifactHostKey(path, strings.NewReader(`{"access_key_id":" k ","secret_access_key":"s"}`)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	raw, _ := os.ReadFile(path)
	if strings.TrimSpace(string(raw)) != `{"access_key_id":"k","secret_access_key":"s"}` {
		t.Fatalf("file %s", raw)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".artifacts-r2-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestCFOAuthToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default.json")
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	os.WriteFile(path, []byte(`{"oauth_token":"tok","expiration_time":"2026-11-26T13:00:00Z"}`), 0o600)
	if tok, err := cfOAuthToken(path, now); err != nil || tok != "tok" {
		t.Fatalf("%q %v", tok, err)
	}
	if _, err := cfOAuthToken(path, now.Add(2*time.Hour)); err == nil || strings.Contains(err.Error(), "tok\"") {
		t.Fatalf("expired: %v", err)
	}
}

func TestArtifactSetupTeamAndIdPFromConfig(t *testing.T) {
	conf := config.ArtifactsConfig{Host: "aiman.fyi", AccountID: "acc", Bucket: "b", Owners: []string{"me@x.com"}, Team: "cool-tree", IdentityProvider: "g1"}
	got, err := parseArtifactSetup(nil, conf)
	if err != nil {
		t.Fatal(err)
	}
	if got.cfg.TeamDomain != "cool-tree.cloudflareaccess.com" || got.cfg.IdentityProvider != "g1" {
		t.Fatalf("cfg %+v", got.cfg)
	}
	if got, err = parseArtifactSetup([]string{"--idp", "g2"}, conf); err != nil || got.cfg.IdentityProvider != "g2" {
		t.Fatalf("flag must win: %+v %v", got.cfg, err)
	}
}
