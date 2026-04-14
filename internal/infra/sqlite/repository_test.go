package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSaveAndGetSessionTunnels(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	created := time.Now()
	s := &domain.Session{
		ID:        "sess-1",
		Status:    domain.SessionStatusActive,
		CreatedAt: created,
		Tunnels: []domain.Tunnel{
			{LocalPort: 5173, RemotePort: 5173},
			{LocalPort: 8080, RemotePort: 8080},
		},
	}
	if err := repo.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Tunnels) != 2 {
		t.Fatalf("expected 2 tunnels, got %d", len(got.Tunnels))
	}
	if got.Tunnels[0].LocalPort != 5173 || got.Tunnels[0].RemotePort != 5173 {
		t.Fatalf("unexpected first tunnel: %+v", got.Tunnels[0])
	}
}

func TestSavePreservesTunnelsWhenNil(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID:        "sess-2",
		Status:    domain.SessionStatusActive,
		CreatedAt: time.Now(),
		Tunnels:   []domain.Tunnel{{LocalPort: 3000, RemotePort: 3000}},
	}); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	if err := repo.Save(ctx, &domain.Session{
		ID:        "sess-2",
		Status:    domain.SessionStatusSyncing,
		CreatedAt: time.Now(),
		// nil tunnels means "do not overwrite stored tunnel config"
		Tunnels: nil,
	}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := repo.Get(ctx, "sess-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Tunnels) != 1 {
		t.Fatalf("expected tunnel config to be preserved, got %d tunnels", len(got.Tunnels))
	}
	if got.Tunnels[0].LocalPort != 3000 || got.Tunnels[0].RemotePort != 3000 {
		t.Fatalf("unexpected preserved tunnel: %+v", got.Tunnels[0])
	}
}
