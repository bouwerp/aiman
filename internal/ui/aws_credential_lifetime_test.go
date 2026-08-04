package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestParseCredentialLifetime(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"43200", 43200, false},
		{"900", 900, false},
		{" 3600 ", 3600, false},
		{"", 0, false}, // empty means "use the default"
		{"   ", 0, false},
		{"899", 0, true}, // below the AWS minimum
		{"43201", 0, true},
		{"0", 0, true}, // an explicit zero is a mistake, not "default"
		{"-1", 0, true},
		{"12h", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseCredentialLifetime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseCredentialLifetime(%q): expected an error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCredentialLifetime(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseCredentialLifetime(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFormatLifetime(t *testing.T) {
	cases := map[int]string{
		0:     "12h", // unset falls back to the credential layer's default
		43200: "12h",
		3600:  "1h",
		5400:  "1h30m",
		900:   "15m",
	}
	for in, want := range cases {
		if got := formatLifetime(in); got != want {
			t.Errorf("formatLifetime(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatLifetimeDefaultMatchesCredentialLayer(t *testing.T) {
	if formatLifetime(0) != formatLifetime(awsdelegation.DefaultDurationSeconds) {
		t.Error("an unset lifetime must display as the credential layer's default")
	}
}

// lifetimeCfg builds a config whose single remote has two delegation profiles, so the
// write-back has to pick the right one.
func lifetimeCfg() *config.Config {
	return &config.Config{Remotes: []config.Remote{{
		Host: "worker.example",
		User: "ubuntu",
		Root: "/home/ubuntu",
		AWSDelegation: &config.AWSDelegation{
			Profile:         "default",
			SourceProfile:   "long-lived",
			SyncCredentials: true,
		},
		AWSDelegations: []*config.AWSDelegation{{
			Profile:         "prod",
			SourceProfile:   "long-lived",
			SyncCredentials: true,
			DurationSeconds: 3600,
		}},
	}}}
}

func lifetimeEntry(cfg *config.Config, profile string) awsHostEntry {
	r := cfg.Remotes[0]
	var del *config.AWSDelegation
	for _, d := range r.AllDelegations() {
		if d.Profile == profile {
			del = d
		}
	}
	return awsHostEntry{
		key:           "ubuntu@worker.example|long-lived|" + profile,
		userAtHost:    "ubuntu@worker.example",
		localProfile:  "long-lived",
		remoteProfile: profile,
		status:        awsCredStatusValid,
		del:           del,
		remote:        r,
	}
}

func withTempHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, config.DirName), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
}

func TestSetDelegationLifetimeWritesTheMatchingProfile(t *testing.T) {
	withTempHome(t)
	cfg := lifetimeCfg()
	entry := lifetimeEntry(cfg, "prod")

	if err := setDelegationLifetime(cfg, &entry, 7200); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Remotes[0].AWSDelegations[0].DurationSeconds; got != 7200 {
		t.Errorf("expected the prod profile updated to 7200, got %d", got)
	}
	if got := cfg.Remotes[0].AWSDelegation.DurationSeconds; got != 0 {
		t.Errorf("the default profile must be left alone, got %d", got)
	}
	if entry.del.DurationSeconds != 7200 {
		t.Errorf("the entry's delegation should reflect the new value, got %d", entry.del.DurationSeconds)
	}
}

func TestSetDelegationLifetimePersists(t *testing.T) {
	withTempHome(t)
	cfg := lifetimeCfg()
	entry := lifetimeEntry(cfg, "default")

	if err := setDelegationLifetime(cfg, &entry, 1800); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}
	if got := reloaded.Remotes[0].AWSDelegation.DurationSeconds; got != 1800 {
		t.Errorf("expected 1800 persisted to disk, got %d", got)
	}
}

func TestSetDelegationLifetimeRollsBackOnSaveFailure(t *testing.T) {
	// No HOME config dir, so Save fails and the in-memory value must be restored.
	t.Setenv("HOME", filepath.Join(t.TempDir(), "missing"))
	cfg := lifetimeCfg()
	entry := lifetimeEntry(cfg, "prod")

	if err := setDelegationLifetime(cfg, &entry, 7200); err == nil {
		t.Fatal("expected a save error")
	}
	if got := cfg.Remotes[0].AWSDelegations[0].DurationSeconds; got != 3600 {
		t.Errorf("expected the original 3600 restored after a failed save, got %d", got)
	}
}

func TestSetDelegationLifetimeRejectsUnmanagedProfile(t *testing.T) {
	withTempHome(t)
	cfg := lifetimeCfg()
	entry := lifetimeEntry(cfg, "prod")
	entry.del = nil // a leftover profile discovered on the remote, not in config

	if err := setDelegationLifetime(cfg, &entry, 7200); err == nil {
		t.Fatal("expected an error for a profile with no local delegation config")
	}
}

// --- keyboard flow ---

func lifetimeModel(t *testing.T) AWSCredentialsModel {
	t.Helper()
	cfg := lifetimeCfg()
	m := NewAWSCredentialsModel(cfg, nil)
	m.entries = []awsHostEntry{lifetimeEntry(cfg, "prod")}
	return m
}

func pressKey(m AWSCredentialsModel, s string) AWSCredentialsModel {
	var msg tea.KeyMsg
	switch s {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	updated, _ := m.Update(msg)
	return updated.(AWSCredentialsModel)
}

func TestLifetimeKeyOpensEditorPrefilled(t *testing.T) {
	m := pressKey(lifetimeModel(t), "t")
	if !m.editingLifetime {
		t.Fatal("expected 't' to open the lifetime editor")
	}
	if got := m.lifetimeInput.Value(); got != "3600" {
		t.Errorf("expected the current lifetime pre-filled, got %q", got)
	}
}

func TestLifetimeEditorSavesOnEnter(t *testing.T) {
	withTempHome(t)
	m := pressKey(lifetimeModel(t), "t")
	m.lifetimeInput.SetValue("7200")
	m = pressKey(m, "enter")

	if m.editingLifetime {
		t.Error("expected the editor to close after saving")
	}
	if got := m.cfg.Remotes[0].AWSDelegations[0].DurationSeconds; got != 7200 {
		t.Errorf("expected 7200 saved, got %d", got)
	}
	if !strings.Contains(m.message, "next renew") {
		t.Errorf("expected the message to say the change applies on next renew, got %q", m.message)
	}
}

func TestLifetimeEditorRejectsOutOfRangeAndStaysOpen(t *testing.T) {
	withTempHome(t)
	m := pressKey(lifetimeModel(t), "t")
	m.lifetimeInput.SetValue("60")
	m = pressKey(m, "enter")

	if !m.editingLifetime {
		t.Error("expected the editor to stay open after an invalid value")
	}
	if got := m.cfg.Remotes[0].AWSDelegations[0].DurationSeconds; got != 3600 {
		t.Errorf("config must be untouched, got %d", got)
	}
	if m.message == "" {
		t.Error("expected an explanatory message")
	}
}

func TestLifetimeEditorCancelsOnEsc(t *testing.T) {
	m := pressKey(lifetimeModel(t), "t")
	m.lifetimeInput.SetValue("7200")
	m = pressKey(m, "esc")

	if m.editingLifetime {
		t.Error("expected the editor to close")
	}
	if got := m.cfg.Remotes[0].AWSDelegations[0].DurationSeconds; got != 3600 {
		t.Errorf("esc must not save, got %d", got)
	}
}

func TestLifetimeKeyRefusesUnmanagedProfile(t *testing.T) {
	m := lifetimeModel(t)
	m.entries[0].del = nil
	m = pressKey(m, "t")

	if m.editingLifetime {
		t.Error("a profile with no local delegation config has no lifetime to edit")
	}
	if m.message == "" {
		t.Error("expected an explanatory message")
	}
}

// Editing the lifetime must not silently re-mint credentials — the countdown only moves
// when the user asks for a renew.
func TestLifetimeEditorDoesNotRenew(t *testing.T) {
	withTempHome(t)
	m := pressKey(lifetimeModel(t), "t")
	m.lifetimeInput.SetValue("7200")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	saved := updated.(AWSCredentialsModel)
	if cmd != nil {
		t.Error("saving a lifetime should not dispatch work")
	}
	if len(saved.renewing) != 0 {
		t.Error("saving a lifetime should not mark anything as renewing")
	}
}

func TestAWSCredentialsViewShowsLifetimeColumn(t *testing.T) {
	m := lifetimeModel(t)
	m.entries[0].expiresAt = time.Now().Add(45 * time.Minute)
	out := m.View()
	if !strings.Contains(out, "Lifetime") {
		t.Error("expected a Lifetime column header")
	}
	if !strings.Contains(out, "1h") {
		t.Errorf("expected the configured lifetime rendered, got:\n%s", out)
	}
	if !strings.Contains(out, "t lifetime") {
		t.Error("expected the help line to mention the lifetime key")
	}
}
