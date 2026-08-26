package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestParseIssueStatuses(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"Dev Ready", []string{"Dev Ready"}},
		{"Dev Ready, In Development", []string{"Dev Ready", "In Development"}},
		{" Dev Ready ,, In Development ,", []string{"Dev Ready", "In Development"}},
	}
	for _, c := range cases {
		if got := parseIssueStatuses(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseIssueStatuses(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestNewSetupModel_ShowsConfiguredStatuses(t *testing.T) {
	cfg := &config.Config{}
	cfg.Integrations.Jira.IssueStatuses = []string{"Dev Ready", "Dev Review"}
	m := NewSetupModel(cfg)
	if got := m.inputs[3].Value(); got != "Dev Ready, Dev Review" {
		t.Errorf("expected the statuses field pre-filled from config, got %q", got)
	}
}

func TestNewSetupModel_BlankStatusesShowDefaults(t *testing.T) {
	m := NewSetupModel(&config.Config{})
	// With nothing configured the field is left empty, and the placeholder names the
	// defaults so the user can see what is in effect.
	if got := m.inputs[3].Value(); got != "" {
		t.Errorf("expected an empty statuses field, got %q", got)
	}
	if m.inputs[3].Placeholder == "" {
		t.Error("expected a placeholder describing the default statuses")
	}
}

func TestSetupModel_SaveStoresStatuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, config.DirName), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	cfg := &config.Config{}
	m := NewSetupModel(cfg)
	m.inputs[0].SetValue("https://example.atlassian.net")
	m.inputs[3].SetValue("Dev Ready, In Development")

	model, _ := m.save()
	saved := model.(SetupModel)
	if saved.err != nil {
		t.Fatalf("unexpected save error: %v", saved.err)
	}
	want := []string{"Dev Ready", "In Development"}
	if !reflect.DeepEqual(cfg.Integrations.Jira.IssueStatuses, want) {
		t.Errorf("expected %#v saved, got %#v", want, cfg.Integrations.Jira.IssueStatuses)
	}
}

// A masked credential field must not cap near the credential's real length: a
// truncated token looks identical to a correct one on screen and only surfaces
// later as a 401. Atlassian's current tokens run ~192 chars, and the shared
// 128 limit silently cut them, which presented as "no JIRA issues in the
// picker" — nothing that points at the token at all.
func TestSetupModelAcceptsFullLengthAPIToken(t *testing.T) {
	cfg := &config.Config{}
	m := NewSetupModel(cfg)

	token := "ATATT" + strings.Repeat("x", 250)
	m.inputs[2].SetValue(token)
	if got := m.inputs[2].Value(); got != token {
		t.Fatalf("token truncated to %d chars (limit %d), want all %d",
			len(got), m.inputs[2].CharLimit, len(token))
	}
}

// Same hazard for env secrets, which are masked too.
func TestSecretInputsAcceptLongValues(t *testing.T) {
	inputs := makeSecretInputs()
	value := strings.Repeat("k", 1000)
	inputs[1].SetValue(value)
	if got := inputs[1].Value(); got != value {
		t.Fatalf("secret truncated to %d chars (limit %d), want all %d",
			len(got), inputs[1].CharLimit, len(value))
	}
}
