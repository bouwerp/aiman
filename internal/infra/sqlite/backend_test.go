package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func openBackendTestRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aiman.db")
	repo, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	return repo, path
}

func storedBackend(t *testing.T, repo *Repository, id string) sql.NullString {
	t.Helper()
	var b sql.NullString
	if err := repo.db.QueryRow(`SELECT backend FROM sessions WHERE id = ?`, id).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

// Rows written before the backend column existed hold NULL and were tmux
// sessions. Open labels them legacy tmux, leaves an explicit empty backend
// alone, and gives the same result when it runs again.
func TestOpenMarksNullBackendAsLegacyTmux(t *testing.T) {
	repo, path := openBackendTestRepo(t)
	if _, err := repo.db.Exec(`INSERT INTO sessions (id, tmux_session, remote_host, backend, created_at, updated_at)
		VALUES ('old', 'WTB-1', 'regent0', NULL, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`INSERT INTO sessions (id, tmux_session, remote_host, backend, created_at, updated_at)
		VALUES ('none', 'WTB-2', 'regent0', '', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	for i := range 2 {
		reopened, err := NewRepository(path)
		if err != nil {
			t.Fatal(err)
		}
		if b := storedBackend(t, reopened, "old"); b.String != domain.BackendLegacyTmux {
			t.Errorf("open %d: NULL backend became %+v, want %q", i, b, domain.BackendLegacyTmux)
		}
		if b := storedBackend(t, reopened, "none"); !b.Valid || b.String != "" {
			t.Errorf("open %d: empty backend became %+v, want empty", i, b)
		}
		s, err := reopened.Get(context.Background(), "old")
		if err != nil {
			t.Fatal(err)
		}
		if !s.IsLegacyTmux() {
			t.Errorf("open %d: migrated row is not legacy tmux: %q", i, s.Backend)
		}
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Discovery saves sessions with no backend. That save must not relabel a
// live pty row as tmux.
func TestSaveEmptyBackendKeepsStoredPTY(t *testing.T) {
	repo, _ := openBackendTestRepo(t)
	defer repo.Close()
	ctx := context.Background()

	if err := repo.Save(ctx, &domain.Session{ID: "s1", TmuxSession: "WTB-1", Backend: domain.BackendPTY}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{ID: "s1", TmuxSession: "WTB-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != domain.BackendPTY || !got.IsPTY() {
		t.Errorf("Backend = %q after an empty-backend save, want %q", got.Backend, domain.BackendPTY)
	}
}

func TestSaveEmptyBackendOnNewRowStaysEmpty(t *testing.T) {
	repo, _ := openBackendTestRepo(t)
	defer repo.Close()
	ctx := context.Background()

	if err := repo.Save(ctx, &domain.Session{ID: "s2", TmuxSession: "WTB-2"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "" || got.IsLegacyTmux() {
		t.Errorf("Backend = %q for a new row saved without one, want empty", got.Backend)
	}
}
