package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
)

// LegacyAWSProfileRef is one stored reference to a dead session-scoped AWS profile
// (see domain.LegacyScopedAWSProfilePrefix).
type LegacyAWSProfileRef struct {
	SessionID string // sessions.id
	Field     string // "aws_profile" or "aws_config_json.SourceProfile"
	Profile   string // the stale profile name, e.g. "aiman-58f485ff"
}

func (r LegacyAWSProfileRef) String() string {
	return fmt.Sprintf("%s  %s = %s", r.SessionID, r.Field, r.Profile)
}

const (
	legacyAWSProfileColumnField = "aws_profile"
	legacyAWSProfileJSONField   = "aws_config_json.SourceProfile"
)

// FindLegacyAWSProfiles lists every session record still carrying a legacy
// session-scoped AWS profile name, without changing anything.
func (r *Repository) FindLegacyAWSProfiles(ctx context.Context) ([]LegacyAWSProfileRef, error) {
	return findLegacyAWSProfiles(ctx, r.db)
}

// LegacyAWSProfilesClearedOnOpen returns the legacy profile references the open-time
// migration removed from this database. It is how `aiman clear-aws-profiles` reports
// work that NewRepository already did before the command ran.
func (r *Repository) LegacyAWSProfilesClearedOnOpen() []LegacyAWSProfileRef {
	return r.clearedOnOpen
}

func findLegacyAWSProfiles(ctx context.Context, db *sql.DB) ([]LegacyAWSProfileRef, error) {
	like := domain.LegacyScopedAWSProfilePrefix + "%"
	query := `
	SELECT id, ? AS field, aws_profile AS profile
	FROM sessions
	WHERE aws_profile LIKE ?
	UNION ALL
	SELECT id, ? AS field, json_extract(aws_config_json, '$.SourceProfile') AS profile
	FROM sessions
	WHERE json_valid(aws_config_json)
	  AND json_extract(aws_config_json, '$.SourceProfile') LIKE ?
	ORDER BY id;`

	rows, err := db.QueryContext(ctx, query,
		legacyAWSProfileColumnField, like,
		legacyAWSProfileJSONField, like)
	if err != nil {
		return nil, fmt.Errorf("failed to find legacy AWS profiles: %w", err)
	}
	defer rows.Close()

	var refs []LegacyAWSProfileRef
	for rows.Next() {
		var ref LegacyAWSProfileRef
		var profile sql.NullString
		if err := rows.Scan(&ref.SessionID, &ref.Field, &profile); err != nil {
			return nil, fmt.Errorf("failed to scan legacy AWS profile: %w", err)
		}
		ref.Profile = profile.String
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read legacy AWS profiles: %w", err)
	}
	return refs, nil
}

// ClearLegacyAWSProfiles removes legacy session-scoped AWS profile names from stored
// sessions and returns what it cleared. Only the profile name is removed: the rest of
// each session's AWS config (region, role, policy) is left intact, so affected sessions
// fall back to the configured default profile on their next start.
func (r *Repository) ClearLegacyAWSProfiles(ctx context.Context) ([]LegacyAWSProfileRef, error) {
	refs, err := r.FindLegacyAWSProfiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if err := clearLegacyAWSProfiles(r.db); err != nil {
		return nil, err
	}
	return refs, nil
}

// clearLegacyAWSProfilesOnOpen performs the cleanup during NewRepository and reports
// what it removed, so an existing database heals itself on the next aiman start.
func clearLegacyAWSProfilesOnOpen(db *sql.DB) ([]LegacyAWSProfileRef, error) {
	ctx := context.Background()
	refs, err := findLegacyAWSProfiles(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if err := clearLegacyAWSProfiles(db); err != nil {
		return nil, err
	}
	return refs, nil
}

// clearLegacyAWSProfiles runs the cleanup statements.
func clearLegacyAWSProfiles(db *sql.DB) error {
	like := domain.LegacyScopedAWSProfilePrefix + "%"
	if _, err := db.Exec(
		"UPDATE sessions SET aws_profile = NULL WHERE aws_profile LIKE ?", like); err != nil {
		return fmt.Errorf("failed to clear legacy aws_profile values: %w", err)
	}
	if _, err := db.Exec(`
		UPDATE sessions
		SET aws_config_json = json_remove(aws_config_json, '$.SourceProfile')
		WHERE json_valid(aws_config_json)
		  AND json_extract(aws_config_json, '$.SourceProfile') LIKE ?`, like); err != nil {
		return fmt.Errorf("failed to clear legacy aws_config_json profiles: %w", err)
	}
	return nil
}
