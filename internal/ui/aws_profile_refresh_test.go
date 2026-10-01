package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestCredentialRescanSeesProfilesAddedWhileOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := NewAWSCredentialsModel(&config.Config{}, nil)
	if len(m.localNames) != 0 {
		t.Fatalf("expected no local profiles yet, got %v", m.localNames)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte("[lab]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	updated, _ := m.Update(pressKey("c"))
	got := updated.(AWSCredentialsModel).localNames
	if !slices.Contains(got, "lab") {
		t.Fatalf("rescan missed a profile added while the screen was open, got %v", got)
	}
}

func TestEnterAWSCredentialsShowsDelegationAddedWhileBusy(t *testing.T) {
	lab := &config.AWSDelegation{Profile: "lab", SourceProfile: "lab", SyncCredentials: true}
	cfg := &config.Config{Remotes: []config.Remote{{
		Host: "regent0", User: "code", Root: "/home/code",
		AWSDelegation: lab,
	}}}
	m := &Model{
		cfg:   cfg,
		state: viewStateMenu,
		awsCredentials: AWSCredentialsModel{
			cfg:      cfg,
			renewing: map[string]bool{"code@regent0|lab|lab": true},
			entries: []awsHostEntry{{
				key:           "code@regent0|lab|lab",
				userAtHost:    "code@regent0",
				localProfile:  "lab",
				remoteProfile: "lab",
				status:        awsCredStatusChecking,
				del:           lab,
				remote:        cfg.Remotes[0],
			}},
		},
	}
	cfg.Remotes[0].AWSDelegations = append(cfg.Remotes[0].AWSDelegations, &config.AWSDelegation{
		Profile: "dev", SourceProfile: "dev", SyncCredentials: true,
	})

	cmd := m.enterAWSCredentials()
	if !m.awsCredentials.renewing["code@regent0|lab|lab"] {
		t.Fatal("in-flight refresh must stay in place")
	}
	var saw bool
	for _, e := range m.awsCredentials.entries {
		if e.remoteProfile == "dev" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("profile added while a refresh was in flight must show without a restart, got %+v", m.awsCredentials.entries)
	}
	if cmd == nil {
		t.Fatal("the new profile should be probed")
	}
}

func TestAWSSourcePickerSeesProfileAddedWhileDialogOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := NewRemotesModel(&config.Config{Remotes: []config.Remote{{Host: "regent0"}}})
	m.awsRemoteIdx = 0
	m.initAWSDialog()
	if len(m.awsLocalProfiles) != 0 {
		t.Fatalf("expected an empty picker, got %v", m.awsLocalProfiles)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte("[lab]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m = m.onEnterSourceFocus()
	if !slices.Contains(m.awsLocalProfiles, "lab") {
		t.Fatalf("source picker must reread ~/.aws when focused, got %v", m.awsLocalProfiles)
	}
}

func TestAWSSourcePickerListsProfilesNotYetAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte("[dev]\n[lab]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	only := []string{"dev"}
	m := NewRemotesModel(&config.Config{
		AWS:     config.AWSDefaults{IncludeProfiles: &only},
		Remotes: []config.Remote{{Host: "regent0"}},
	})
	m.awsRemoteIdx = 0
	m.initAWSDialog()
	if !slices.Contains(m.awsLocalProfiles, "lab") {
		t.Fatalf("picker must offer a local profile that is not on the allow list yet, got %v", m.awsLocalProfiles)
	}
}

func TestAWSDialogSwitchesToSavedProfileWithoutRestart(t *testing.T) {
	cfg := &config.Config{Remotes: []config.Remote{{
		Host:          "regent0",
		AWSDelegation: &config.AWSDelegation{Profile: "lab", SourceProfile: "lab", RoleName: "RoleA"},
		AWSDelegations: []*config.AWSDelegation{{
			Profile: "dev", SourceProfile: "dev", RoleName: "RoleB",
		}},
	}}}
	m := NewRemotesModel(cfg)
	m.state = remotesStateAWS
	m.awsRemoteIdx = 0
	m.initAWSDialog()
	if m.awsProfile.Value() != "lab" {
		t.Fatalf("form starts on the primary profile, got %q", m.awsProfile.Value())
	}

	updated, _ := m.Update(pressKey("down"))
	got := updated.(RemotesModel)
	if got.awsProfile.Value() != "dev" || got.awsRoleName.Value() != "RoleB" {
		t.Fatalf("down should load the other saved profile, got profile %q role %q", got.awsProfile.Value(), got.awsRoleName.Value())
	}
	view := got.viewAWS()
	for _, part := range []string{"Configured profiles", "lab", "dev"} {
		if !strings.Contains(view, part) {
			t.Fatalf("view missing %q:\n%s", part, view)
		}
	}
}
