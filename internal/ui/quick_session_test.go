package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestQuickStartKeyUsesDefaultRemote(t *testing.T) {
	cfg := twoRemoteCfg()
	cfg.ActiveRemote = "10.0.1.9"
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	updated, cmd, handled := m.handleMainKeyMsg(pressKey("N"))
	if !handled {
		t.Fatal("N should be handled")
	}
	got := updated.(*Model)
	if !got.sessionCfg.Quick || !got.sessionCfg.AdHoc {
		t.Fatalf("expected quick ad-hoc config, got %+v", got.sessionCfg)
	}
	if got.selectedRemote.Host != "10.0.1.9" {
		t.Fatalf("selected remote %q", got.selectedRemote.Host)
	}
	if got.state != viewStateLoading {
		t.Fatalf("state %v want loading", got.state)
	}
	if cmd == nil {
		t.Fatal("expected fetchAgents command")
	}
}

func TestQuickStartNoRemotesStaysOnMain(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateMain}
	got := pressMainKey(m, 'N')
	if got.state != viewStateMain {
		t.Fatalf("state %v", got.state)
	}
}

func TestStartQuickSessionDoesNotSetDefaultAWSProfile(t *testing.T) {
	cfg := twoRemoteCfg()
	cfg.AWS.DefaultProfile = "prod"
	cfg.Remotes[0].AWSDelegation = &config.AWSDelegation{
		Profile: "prod", SourceProfile: "prod", SyncCredentials: true, Region: "eu-west-1",
	}
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.selectedRemote = cfg.Remotes[0]
	m.sessionCfg.Quick = true
	m.sessionCfg.Agent = &domain.Agent{Name: "claude", Command: "claude"}
	_, _ = m.startQuickSession()
	if m.sessionCfg.AWSConfig != nil && m.sessionCfg.AWSConfig.SourceProfile != "" {
		t.Fatalf("quick start must not inject a default AWS profile, got %+v", m.sessionCfg.AWSConfig)
	}
}

func TestStartQuickSessionDoesNotCopyNameOntoBranch(t *testing.T) {
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.selectedRemote = cfg.Remotes[0]
	m.sessionCfg.Quick = true
	m.sessionCfg.Agent = &domain.Agent{Name: "claude", Command: "claude"}
	_, _ = m.startQuickSession()
	if m.sessionCfg.Name == "" {
		t.Fatal("expected a generated display name")
	}
	if m.sessionCfg.Branch == m.sessionCfg.Name {
		t.Fatalf("Branch %q must not be the display name; rename has to leave the worktree/tmux identity alone", m.sessionCfg.Branch)
	}
}

func TestRenameKeyOpensInput(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TmuxSession: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, handled := m.handleMainKeyMsg(pressKey("e"))
	if !handled {
		t.Fatal("e should be handled")
	}
	got := updated.(*Model)
	if got.state != viewStateRenameSession {
		t.Fatalf("state %v", got.state)
	}
	if got.genericInput.Value() != "q1" {
		t.Fatalf("prefill %q", got.genericInput.Value())
	}
}
