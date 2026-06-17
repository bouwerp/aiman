package ui

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type startupSessionRepo struct {
	sessions  []domain.Session
	deletedID []string
}

func (m *startupSessionRepo) Save(context.Context, *domain.Session) error { return nil }
func (m *startupSessionRepo) Get(context.Context, string) (*domain.Session, error) {
	return nil, nil
}
func (m *startupSessionRepo) List(context.Context) ([]domain.Session, error) {
	return append([]domain.Session(nil), m.sessions...), nil
}
func (m *startupSessionRepo) Delete(_ context.Context, id string) error {
	m.deletedID = append(m.deletedID, id)
	return nil
}
func (m *startupSessionRepo) Close() error { return nil }
func (m *startupSessionRepo) SaveSnapshot(context.Context, *domain.SessionSnapshot) error {
	return nil
}
func (m *startupSessionRepo) GetLatestSnapshot(context.Context, string) (*domain.SessionSnapshot, error) {
	return nil, nil
}
func (m *startupSessionRepo) ListSnapshots(context.Context, string) ([]domain.SessionSnapshot, error) {
	return nil, nil
}
func (m *startupSessionRepo) ListAllSnapshots(context.Context) ([]domain.SessionSnapshot, error) {
	return nil, nil
}
func (m *startupSessionRepo) MarkSnapshotInjected(context.Context, string) error { return nil }
func (m *startupSessionRepo) DeleteSnapshot(context.Context, string) error       { return nil }
func (m *startupSessionRepo) ListSecrets(context.Context) ([]domain.Secret, error) {
	return nil, nil
}
func (m *startupSessionRepo) SaveSecret(context.Context, domain.Secret) error { return nil }
func (m *startupSessionRepo) DeleteSecret(context.Context, string) error      { return nil }
func (m *startupSessionRepo) HasActiveSessionForEvent(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestLoadConfiguredSessions_PrunesRemovedRemoteSessions(t *testing.T) {
	repo := &startupSessionRepo{
		sessions: []domain.Session{
			{ID: "gone", RemoteHost: "devbox", TmuxSession: "PB-720"},
			{ID: "keep", RemoteHost: "still-here", TmuxSession: "PB-721"},
		},
	}
	cfg := &config.Config{
		Remotes: []config.Remote{{Host: "still-here"}},
	}

	sessions, err := loadConfiguredSessions(context.Background(), cfg, repo)
	if err != nil {
		t.Fatalf("loadConfiguredSessions returned error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "keep" {
		t.Fatalf("expected only configured-remote session to remain, got %#v", sessions)
	}
	if len(repo.deletedID) != 1 || repo.deletedID[0] != "gone" {
		t.Fatalf("expected removed remote session to be deleted from DB, got %#v", repo.deletedID)
	}
}
