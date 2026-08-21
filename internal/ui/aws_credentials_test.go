package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAWSCredentialsModelDeleteStartsRemovalForUnmanagedProfile(t *testing.T) {
	model := NewAWSCredentialsModel(&config.Config{}, nil)
	model.entries = []awsHostEntry{{
		key:           "host|local|dev",
		userAtHost:    "dev@example",
		remoteProfile: "dev",
		remote:        config.Remote{Host: "example", User: "dev", Root: "/home/dev"},
	}}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m := updated.(AWSCredentialsModel)
	if cmd == nil {
		t.Fatal("expected remove command")
	}
	if !strings.Contains(m.message, "Removing") {
		t.Fatalf("expected remove message, got %q", m.message)
	}
}

func TestAWSCredentialsModelDeleteStartsRemovalForManagedProfile(t *testing.T) {
	model := NewAWSCredentialsModel(&config.Config{}, nil)
	model.entries = []awsHostEntry{{
		key:           "host|local|dev",
		userAtHost:    "dev@example",
		remoteProfile: "dev",
		del:           &config.AWSDelegation{Profile: "dev"},
		remote:        config.Remote{Host: "example", User: "dev", Root: "/home/dev"},
	}}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m := updated.(AWSCredentialsModel)
	if cmd == nil {
		t.Fatal("expected remove command for a pushed temporary profile")
	}
	if !strings.Contains(m.message, "Removing") {
		t.Fatalf("expected remove message, got %q", m.message)
	}
}

func TestAWSCredentialsModelRenameStartsEditing(t *testing.T) {
	model := NewAWSCredentialsModel(&config.Config{}, nil)
	model.entries = []awsHostEntry{{
		key:           "host|local|dev",
		userAtHost:    "dev@example",
		remoteProfile: "dev",
	}}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m := updated.(AWSCredentialsModel)
	if cmd == nil {
		t.Fatal("expected rename input command")
	}
	if !m.renaming {
		t.Fatal("expected renaming mode")
	}
	if got := m.renameInput.Value(); got != "dev" {
		t.Fatalf("expected rename input prefilled, got %q", got)
	}
}

func TestAWSCredentialsModelRenameSubmitsCommand(t *testing.T) {
	model := NewAWSCredentialsModel(&config.Config{}, nil)
	model.entries = []awsHostEntry{{
		key:           "host|local|dev",
		userAtHost:    "dev@example",
		remoteProfile: "dev",
		remote:        config.Remote{Host: "example", User: "dev", Root: "/home/dev"},
	}}
	model.renaming = true
	model.renameKey = "host|local|dev"
	model.renameInput.SetValue("prod")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m := updated.(AWSCredentialsModel)
	if cmd == nil {
		t.Fatal("expected rename command")
	}
	if m.renaming {
		t.Fatal("expected renaming mode to close")
	}
	if !strings.Contains(m.message, "Renaming") {
		t.Fatalf("expected rename message, got %q", m.message)
	}
}

func TestRenameManagedDelegationProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, config.DirName)
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Remotes: []config.Remote{{
			Host: "example",
			User: "dev",
			Root: "/home/dev",
			AWSDelegations: []*config.AWSDelegation{{
				Profile:         "dev",
				SourceProfile:   "local-dev",
				SyncCredentials: true,
			}},
		}},
	}
	entry := &awsHostEntry{
		key:           "host|local|dev",
		userAtHost:    "dev@example",
		remoteProfile: "dev",
		del:           cfg.Remotes[0].AWSDelegations[0],
		remote:        cfg.Remotes[0],
	}

	if err := renameManagedDelegationProfile(cfg, entry, "dev", "prod"); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Remotes[0].AWSDelegations[0].Profile; got != "prod" {
		t.Fatalf("expected config profile renamed, got %q", got)
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, config.ConfigName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "profile: prod") {
		t.Fatalf("expected saved config to contain renamed profile, got:\n%s", string(data))
	}
}

func TestFormatExpiresIn(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		at     time.Time
		approx bool
		want   string
	}{
		{"unknown", time.Time{}, false, "—"},
		{"hours and minutes", now.Add(4*time.Hour + 12*time.Minute), false, "4h12m"},
		{"minutes only", now.Add(42 * time.Minute), false, "42m"},
		{"under a minute", now.Add(30 * time.Second), false, "<1m"},
		{"approximate is marked", now.Add(3 * time.Hour), true, "~3h00m"},
		{"expired", now.Add(-5 * time.Minute), false, "expired"},
		{"expired exactly now", now, false, "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatExpiresIn(tc.at, tc.approx, now); got != tc.want {
				t.Fatalf("formatExpiresIn(%v, %v) = %q, want %q", tc.at, tc.approx, got, tc.want)
			}
		})
	}
}

func TestExpiryUrgency(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want expiryUrgency
	}{
		{"unknown", time.Time{}, expiryUnknown},
		{"comfortable", now.Add(6 * time.Hour), expiryOK},
		{"just over the window", now.Add(16 * time.Minute), expiryOK},
		{"inside the window", now.Add(14 * time.Minute), expiryWarn},
		{"expired", now.Add(-time.Second), expiryExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := urgencyOf(tc.at, now); got != tc.want {
				t.Fatalf("urgencyOf(%v) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}
}

func TestFormatAWSCredExpiryBanner(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	if got := formatAWSCredExpiryBanner(nil, now); got != "" {
		t.Fatalf("no items must produce no banner, got %q", got)
	}

	healthy := []awsCredExpiryItem{{userAtHost: "ubuntu@host", profile: "prod", expiresAt: now.Add(8 * time.Hour)}}
	if got := formatAWSCredExpiryBanner(healthy, now); got != "" {
		t.Fatalf("credentials far from expiry must not warn, got %q", got)
	}

	one := []awsCredExpiryItem{
		{userAtHost: "ubuntu@host", profile: "prod", expiresAt: now.Add(12 * time.Minute)},
		{userAtHost: "ubuntu@host", profile: "dev", expiresAt: now.Add(9 * time.Hour)},
	}
	got := formatAWSCredExpiryBanner(one, now)
	if !strings.Contains(got, "12m") || !strings.Contains(got, "ubuntu@host") || !strings.Contains(got, "prod") {
		t.Fatalf("expected the soonest profile named with its remaining time, got %q", got)
	}
	if strings.Contains(got, "dev") {
		t.Fatalf("must not mention profiles outside the warning window, got %q", got)
	}
	if !strings.Contains(got, "shift+R") {
		t.Fatalf("banner must tell the user how to refresh, got %q", got)
	}

	many := []awsCredExpiryItem{
		{userAtHost: "a@h", profile: "p1", expiresAt: now.Add(10 * time.Minute)},
		{userAtHost: "b@h", profile: "p2", expiresAt: now.Add(5 * time.Minute)},
		{userAtHost: "c@h", profile: "p3", expiresAt: now.Add(-time.Minute)},
	}
	got = formatAWSCredExpiryBanner(many, now)
	if !strings.Contains(got, "expired") {
		t.Fatalf("an already-expired profile must be reported as expired, got %q", got)
	}
	if !strings.Contains(got, "+2 more") {
		t.Fatalf("expected the remaining count, got %q", got)
	}
}

func TestEntriesToRenewAllIncludesValidCredentials(t *testing.T) {
	del := &config.AWSDelegation{Profile: "prod", SourceProfile: "prod", SyncCredentials: true}
	entries := []awsHostEntry{
		{key: "k1", status: awsCredStatusValid, del: del},
		{key: "k2", status: awsCredStatusExpired, del: del},
		{key: "k3", status: awsCredStatusNotPushed, del: del},
		{key: "k4", status: awsCredStatusValid, del: nil}, // no delegation config — cannot renew
		{key: "k5", status: awsCredStatusValid, del: del}, // already renewing
		{key: "k6", status: awsCredStatusNoConf, del: del},
	}
	got := entriesToRenewAll(entries, map[string]bool{"k5": true})

	var keys []string
	for _, e := range got {
		keys = append(keys, e.key)
	}
	want := []string{"k1", "k2", "k3", "k6"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("entriesToRenewAll selected %v, want %v", keys, want)
	}
}
