package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// ci-minutes rev 3: repos.<slug>.ci (local override list, drafts_skip_ci) and
// coordinator.child_prs (default false: children land locally).

func TestRepoCIParses(t *testing.T) {
	var c Config
	src := `
repos:
  realfi-co/treasury-admin-dapp:
    ci:
      drafts_skip_ci: true
      local:
        - name: lint
          run: npm run lint
          dir: web
          timeout: 20m
coordinator:
  child_prs: true
`
	if err := yaml.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	b, ok := c.RepoBootstrapFor("realfi-co/treasury-admin-dapp")
	if !ok || !b.CI.DraftsSkipCI || len(b.CI.Local) != 1 {
		t.Fatalf("repo ci = %+v", b.CI)
	}
	if l := b.CI.Local[0]; l.Name != "lint" || l.Run != "npm run lint" || l.Dir != "web" || l.Timeout != "20m" {
		t.Fatalf("local = %+v", l)
	}
	if !c.CoordinatorChildPRs() {
		t.Fatal("child_prs: true not read")
	}
}

func TestChildPRsDefaultsToFalse(t *testing.T) {
	var c Config
	if c.CoordinatorChildPRs() {
		t.Fatal("child_prs must default to false: children land locally")
	}
	var nilCfg *Config
	if nilCfg.CoordinatorChildPRs() {
		t.Fatal("nil config must read as false")
	}
}

func TestRepoCIValidateRejectsEscapingDir(t *testing.T) {
	b := RepoBootstrap{CI: RepoCI{Local: []RepoCICommand{{Name: "x", Run: "ls", Dir: "../up"}}}}
	if err := b.Validate(); err == nil {
		t.Fatal("Validate accepted a ci.local dir that escapes the repo")
	}
}
