package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestAWSCredentialsModelDeleteRefusesManagedProfile(t *testing.T) {
	model := NewAWSCredentialsModel(&config.Config{}, nil)
	model.entries = []awsHostEntry{{
		key:           "host|local|dev",
		userAtHost:    "dev@example",
		remoteProfile: "dev",
		del:           &config.AWSDelegation{Profile: "dev"},
	}}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m := updated.(AWSCredentialsModel)
	if cmd != nil {
		t.Fatal("did not expect remove command for managed profile")
	}
	if !strings.Contains(m.message, "still managed") {
		t.Fatalf("expected managed-profile warning, got %q", m.message)
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
