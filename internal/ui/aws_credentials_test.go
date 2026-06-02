package ui

import (
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
