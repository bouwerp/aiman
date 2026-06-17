package sqlite

import (
	"context"
)

func (r *Repository) HasActiveSessionForEvent(ctx context.Context, source string, eventID string) (bool, error) {
	query := `
		SELECT COUNT(1) 
		FROM sessions 
		WHERE trigger_source = ? 
		  AND trigger_event_id = ? 
		  AND status NOT IN ('CLEANUP', 'ERROR', 'INACTIVE')
	`
	var count int
	err := r.db.QueryRowContext(ctx, query, source, eventID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
