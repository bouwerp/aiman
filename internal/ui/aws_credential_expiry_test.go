package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func syncedRemote() config.Remote {
	return config.Remote{
		Host: "worker.example",
		User: "ubuntu",
		Root: "/home/ubuntu",
		AWSDelegation: &config.AWSDelegation{
			Profile:         "prod",
			SourceProfile:   "prod",
			SyncCredentials: true,
			DurationSeconds: 3600,
		},
	}
}

func TestSyncedDelegations(t *testing.T) {
	cfg := &config.Config{Remotes: []config.Remote{
		syncedRemote(),
		{
			Host:          "other.example",
			User:          "dev",
			AWSDelegation: &config.AWSDelegation{Profile: "dev", SyncCredentials: false},
		},
		{
			Host:          "blank.example",
			User:          "dev",
			AWSDelegation: &config.AWSDelegation{SourceProfile: "lab", SyncCredentials: true},
		},
	}}

	got := syncedDelegations(cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 synced delegations, got %d: %+v", len(got), got)
	}
	// Sorted by host, so blank.example comes first and its empty profile defaults.
	if got[0].userAtHost != "dev@blank.example" || got[0].profile != "default" {
		t.Fatalf("unexpected first target: %+v", got[0])
	}
	if got[1].userAtHost != "ubuntu@worker.example" || got[1].profile != "prod" {
		t.Fatalf("unexpected second target: %+v", got[1])
	}
}

func TestSyncedDelegationsNilConfig(t *testing.T) {
	if got := syncedDelegations(nil); len(got) != 0 {
		t.Fatalf("expected no targets for a nil config, got %+v", got)
	}
}

func TestRenderAWSCredExpiryBannerSilentWhenHealthy(t *testing.T) {
	m := &Model{awsCredExpiry: []awsCredExpiryItem{
		{userAtHost: "ubuntu@worker.example", profile: "prod", expiresAt: time.Now().Add(6 * time.Hour)},
	}}
	if got := m.renderAWSCredExpiryBanner(); got != "" {
		t.Fatalf("expected no banner for healthy credentials, got %q", got)
	}
}

func TestRenderAWSCredExpiryBannerWarnsWithinWindow(t *testing.T) {
	m := &Model{awsCredExpiry: []awsCredExpiryItem{
		{userAtHost: "ubuntu@worker.example", profile: "prod", expiresAt: time.Now().Add(20 * time.Minute)},
	}}
	got := m.renderAWSCredExpiryBanner()
	if !strings.Contains(got, "prod") || !strings.Contains(got, "shift+R") {
		t.Fatalf("expected an actionable warning banner, got %q", got)
	}
}

func TestMainKeyShiftRStartsRefreshAll(t *testing.T) {
	m := &Model{cfg: &config.Config{Remotes: []config.Remote{syncedRemote()}}}
	updated, cmd, handled := m.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if !handled {
		t.Fatal("shift+R must be handled by the main view")
	}
	if cmd == nil {
		t.Fatal("expected a refresh command")
	}
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if !model.awsCredRefreshing {
		t.Fatal("expected the refresh-in-flight flag to be set")
	}
}

func TestMainKeyShiftRWithoutSyncedDelegations(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	updated, _, handled := m.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if !handled {
		t.Fatal("shift+R must be handled even when there is nothing to refresh")
	}
	if model, ok := updated.(*Model); ok && model.awsCredRefreshing {
		t.Fatal("must not claim a refresh is running when there are no targets")
	}
}

func TestMainKeyShiftRIgnoredWhileRefreshing(t *testing.T) {
	m := &Model{cfg: &config.Config{Remotes: []config.Remote{syncedRemote()}}, awsCredRefreshing: true}
	_, cmd, handled := m.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if !handled || cmd == nil {
		t.Fatal("a second shift+R should be handled with a toast, not ignored")
	}
}
