package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// seedLegacyAWSProfiles writes rows straight through SQL so the fixture keeps the
// shape old aiman versions persisted: a session-scoped profile in the dead
// aws_profile column and/or in aws_config_json.SourceProfile.
func seedLegacyAWSProfiles(t *testing.T, repo *Repository) {
	t.Helper()
	ctx := context.Background()
	rows := []struct {
		id, awsProfile, awsConfigJSON string
	}{
		{"legacy-col", "aiman-58f485ff", ""},
		{"legacy-json", "", `{"SourceProfile":"aiman-a1b2c3d4","Region":"us-east-2"}`},
		{"legacy-both", "aiman-deadbeef", `{"SourceProfile":"aiman-deadbeef","Region":"us-east-2"}`},
		{"clean", "prod", `{"SourceProfile":"prod","Region":"us-east-2"}`},
		{"no-aws", "", ""},
	}
	for _, r := range rows {
		if err := repo.Save(ctx, &domain.Session{
			ID:        r.id,
			Status:    domain.SessionStatusActive,
			CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
		var awsProfile, awsConfig any
		if r.awsProfile != "" {
			awsProfile = r.awsProfile
		}
		if r.awsConfigJSON != "" {
			awsConfig = r.awsConfigJSON
		}
		if _, err := repo.db.ExecContext(ctx,
			"UPDATE sessions SET aws_profile = ?, aws_config_json = ? WHERE id = ?",
			awsProfile, awsConfig, r.id); err != nil {
			t.Fatalf("seed columns %s: %v", r.id, err)
		}
	}
}

func newRepo(t *testing.T) *Repository {
	t.Helper()
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestFindLegacyAWSProfilesReportsWithoutMutating(t *testing.T) {
	repo := newRepo(t)
	seedLegacyAWSProfiles(t, repo)
	ctx := context.Background()

	found, err := repo.FindLegacyAWSProfiles(ctx)
	if err != nil {
		t.Fatalf("FindLegacyAWSProfiles: %v", err)
	}
	if len(found) != 4 {
		t.Fatalf("expected 4 legacy references (2 columns + 2 json), got %d: %+v", len(found), found)
	}
	for _, f := range found {
		if f.SessionID == "clean" || f.SessionID == "no-aws" {
			t.Fatalf("reported a session with no legacy profile: %+v", f)
		}
	}

	// A find must not change anything.
	again, err := repo.FindLegacyAWSProfiles(ctx)
	if err != nil {
		t.Fatalf("second FindLegacyAWSProfiles: %v", err)
	}
	if len(again) != len(found) {
		t.Fatalf("FindLegacyAWSProfiles mutated state: %d then %d", len(found), len(again))
	}
}

func TestClearLegacyAWSProfiles(t *testing.T) {
	repo := newRepo(t)
	seedLegacyAWSProfiles(t, repo)
	ctx := context.Background()

	cleared, err := repo.ClearLegacyAWSProfiles(ctx)
	if err != nil {
		t.Fatalf("ClearLegacyAWSProfiles: %v", err)
	}
	if len(cleared) != 4 {
		t.Fatalf("expected to clear 4 legacy references, got %d: %+v", len(cleared), cleared)
	}

	remaining, err := repo.FindLegacyAWSProfiles(ctx)
	if err != nil {
		t.Fatalf("FindLegacyAWSProfiles after clear: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no legacy profiles left, got %+v", remaining)
	}

	// The unaffected session keeps its profile.
	got, err := repo.Get(ctx, "clean")
	if err != nil {
		t.Fatalf("Get clean: %v", err)
	}
	if got.AWSConfig == nil || got.AWSConfig.SourceProfile != "prod" {
		t.Fatalf("clean session lost its profile: %+v", got.AWSConfig)
	}

	// A cleared session falls back to no profile but keeps the rest of its AWS config.
	got, err = repo.Get(ctx, "legacy-json")
	if err != nil {
		t.Fatalf("Get legacy-json: %v", err)
	}
	if got.AWSConfig == nil {
		t.Fatal("legacy-json lost its whole AWS config; only the profile should be cleared")
	}
	if got.AWSConfig.SourceProfile != "" {
		t.Fatalf("expected empty SourceProfile, got %q", got.AWSConfig.SourceProfile)
	}
	if got.AWSConfig.Region != "us-east-2" {
		t.Fatalf("expected region to survive, got %q", got.AWSConfig.Region)
	}

	// Clearing again is a no-op.
	cleared, err = repo.ClearLegacyAWSProfiles(ctx)
	if err != nil {
		t.Fatalf("second ClearLegacyAWSProfiles: %v", err)
	}
	if len(cleared) != 0 {
		t.Fatalf("expected second clear to be a no-op, got %+v", cleared)
	}
}

func TestNewRepositoryClearsLegacyAWSProfilesOnOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "aiman.db")
	repo, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	seedLegacyAWSProfiles(t, repo)
	repo.Close()

	reopened, err := NewRepository(dbPath)
	if err != nil {
		t.Fatalf("reopen NewRepository: %v", err)
	}
	defer reopened.Close()

	remaining, err := reopened.FindLegacyAWSProfiles(context.Background())
	if err != nil {
		t.Fatalf("FindLegacyAWSProfiles: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("opening the database must migrate legacy profiles away, got %+v", remaining)
	}

	// The migration reports what it removed so `aiman clear-aws-profiles` can print it.
	if got := reopened.LegacyAWSProfilesClearedOnOpen(); len(got) != 4 {
		t.Fatalf("expected 4 cleared references reported from open, got %d: %+v", len(got), got)
	}
	if got := repo.LegacyAWSProfilesClearedOnOpen(); len(got) != 0 {
		t.Fatalf("a freshly created database must report nothing cleared, got %+v", got)
	}
}
