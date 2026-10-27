package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRepoBootstrapForIsCaseInsensitive(t *testing.T) {
	cfg := &Config{Repos: map[string]RepoBootstrap{
		"Owner/Name": {Setup: "make deps"},
		"other/repo": {Setup: "exact"},
	}}

	got, ok := cfg.RepoBootstrapFor("owner/name")
	if !ok || got.Setup != "make deps" {
		t.Fatalf("RepoBootstrapFor(owner/name) = %+v, %v", got, ok)
	}
	got, ok = cfg.RepoBootstrapFor("other/repo")
	if !ok || got.Setup != "exact" {
		t.Fatalf("RepoBootstrapFor(other/repo) = %+v, %v", got, ok)
	}
	if _, ok := cfg.RepoBootstrapFor("missing/repo"); ok {
		t.Fatal("missing repo should not be found")
	}
	if _, ok := (*Config)(nil).RepoBootstrapFor("owner/name"); ok {
		t.Fatal("nil config should find nothing")
	}
}

func TestRepoBootstrapValidate(t *testing.T) {
	bad := []string{"/etc/passwd", "../x", "a/../../x", "", ".", ".."}
	for _, p := range bad {
		if err := (RepoBootstrap{Copy: []string{p}}).Validate(); err == nil {
			t.Errorf("copy %q: expected error", p)
		}
		err := (RepoBootstrap{Symlink: []string{p}}).Validate()
		if err == nil {
			t.Errorf("symlink %q: expected error", p)
			continue
		}
		if p != "" && !strings.Contains(err.Error(), p) {
			t.Errorf("symlink %q: error %q does not name the entry", p, err)
		}
	}
	good := RepoBootstrap{Copy: []string{".env.local"}, Symlink: []string{"web/node_modules"}, Setup: "make deps"}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid bootstrap: %v", err)
	}
}

func TestMarshalServeConfigCarriesRepos(t *testing.T) {
	cfg := &Config{Repos: map[string]RepoBootstrap{"o/n": {Copy: []string{".env.local"}, Setup: "make deps"}}}

	body, err := cfg.MarshalServeConfig("h")
	if err != nil {
		t.Fatalf("MarshalServeConfig: %v", err)
	}
	var got ServeConfig
	if err := yaml.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Repos["o/n"].Setup != "make deps" {
		t.Fatalf("repos not carried: %s", body)
	}
}

func TestLookupRepoCaseInsensitive(t *testing.T) {
	repos := map[string]RepoBootstrap{
		"Owner/Name": {Setup: "folded"},
		"owner/name": {Setup: "exact"},
		"other/repo": {Setup: "other"},
	}
	if got, ok := LookupRepo(repos, "owner/name"); !ok || got.Setup != "exact" {
		t.Fatalf("exact = %+v, %v", got, ok)
	}
	if got, ok := LookupRepo(repos, "OTHER/Repo"); !ok || got.Setup != "other" {
		t.Fatalf("folded = %+v, %v", got, ok)
	}
	if _, ok := LookupRepo(repos, "missing/repo"); ok {
		t.Fatal("missing repo should not be found")
	}
	if _, ok := LookupRepo(nil, "owner/name"); ok {
		t.Fatal("nil map should find nothing")
	}
}
