package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
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

func TestSyncedDelegationsSkipsDisallowedLocalProfiles(t *testing.T) {
	only := []string{"prod"}
	cfg := &config.Config{
		AWS: config.AWSDefaults{IncludeProfiles: &only},
		Remotes: []config.Remote{
			syncedRemote(),
			{
				Host: "regent0",
				User: "code",
				AWSDelegation: &config.AWSDelegation{
					Profile: "lab", SourceProfile: "lab", SyncCredentials: true,
				},
			},
		},
	}
	got := syncedDelegations(cfg)
	if len(got) != 1 || got[0].profile != "prod" {
		t.Fatalf("deselected lab must not be a sync target, got %+v", got)
	}
}

func TestRenderAWSCredExpiryBannerIgnoresDeselectedProfiles(t *testing.T) {
	only := []string{"prod"}
	m := &Model{
		cfg: &config.Config{
			AWS: config.AWSDefaults{IncludeProfiles: &only},
			Remotes: []config.Remote{{
				Host: "regent0",
				User: "code",
				AWSDelegation: &config.AWSDelegation{
					Profile: "lab", SourceProfile: "lab", SyncCredentials: true,
				},
			}},
		},
		awsCredExpiry: []awsCredExpiryItem{
			{userAtHost: "code@regent0", profile: "lab", expiresAt: time.Now().Add(-time.Minute)},
		},
	}
	if got := m.renderAWSCredExpiryBanner(); got != "" {
		t.Fatalf("deselected lab must not appear on the banner, got %q", got)
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
		{userAtHost: "ubuntu@worker.example", profile: "prod", expiresAt: time.Now().Add(10 * time.Minute)},
	}}
	got := m.renderAWSCredExpiryBanner()
	if !strings.Contains(got, "prod") || !strings.Contains(got, "shift+R") {
		t.Fatalf("expected an actionable warning banner, got %q", got)
	}
}

// Credentials with half an hour left are not worth interrupting for; the warning window is
// deliberately narrow so the banner means "act now".
func TestRenderAWSCredExpiryBannerSilentAboveWarnWindow(t *testing.T) {
	m := &Model{awsCredExpiry: []awsCredExpiryItem{
		{userAtHost: "ubuntu@worker.example", profile: "prod", expiresAt: time.Now().Add(30 * time.Minute)},
	}}
	if got := m.renderAWSCredExpiryBanner(); got != "" {
		t.Fatalf("expected no banner 30m out, got %q", got)
	}
}

func TestUrgencyOfWarnBoundary(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		in   time.Duration
		want expiryUrgency
	}{
		{"well ahead", 4 * time.Hour, expiryOK},
		{"just outside the window", 16 * time.Minute, expiryOK},
		{"on the boundary", awsCredExpiryWarnWindow, expiryWarn},
		{"just inside the window", 14 * time.Minute, expiryWarn},
		{"lapsed", -time.Minute, expiryExpired},
	}
	for _, c := range cases {
		if got := urgencyOf(now.Add(c.in), now); got != c.want {
			t.Errorf("%s: urgencyOf(+%s) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
	if got := urgencyOf(time.Time{}, now); got != expiryUnknown {
		t.Errorf("zero expiry should be unknown, got %v", got)
	}
}

func TestAWSCredExpiryWarnWindowIsFifteenMinutes(t *testing.T) {
	if awsCredExpiryWarnWindow != 15*time.Minute {
		t.Fatalf("expected a 15m warning window, got %s", awsCredExpiryWarnWindow)
	}
}

func TestMainKeyShiftRStartsRefreshAll(t *testing.T) {
	m := &Model{cfg: &config.Config{Remotes: []config.Remote{syncedRemote()}}}
	updated, cmd, handled := m.handleMainKeyMsg(pressKey("R"))
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
	updated, _, handled := m.handleMainKeyMsg(pressKey("R"))
	if !handled {
		t.Fatal("shift+R must be handled even when there is nothing to refresh")
	}
	if model, ok := updated.(*Model); ok && model.awsCredRefreshing {
		t.Fatal("must not claim a refresh is running when there are no targets")
	}
}

func TestMainKeyShiftRIgnoredWhileRefreshing(t *testing.T) {
	m := &Model{cfg: &config.Config{Remotes: []config.Remote{syncedRemote()}}, awsCredRefreshing: true}
	_, cmd, handled := m.handleMainKeyMsg(pressKey("R"))
	if !handled || cmd == nil {
		t.Fatal("a second shift+R should be handled with a toast, not ignored")
	}
}
