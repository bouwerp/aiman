package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bouwerp/aiman/internal/domain"
)

// identityRow is a stored session's identity and lifecycle columns: the ones
// whose change makes Save report ChangeIdentity.
type identityRow struct {
	name, group, parent, role, status, backend, branch, workDir, reviewPR, fixPR string
}

// readIdentity reads the identity columns of id inside tx. found is false
// when there is no such row.
func readIdentity(ctx context.Context, tx *sql.Tx, id string) (identityRow, bool, error) {
	var c [10]sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT name, group_name, parent_id, role, status, backend, branch, working_directory, review_pr, fix_pr FROM sessions WHERE id = ?`, id).
		Scan(&c[0], &c[1], &c[2], &c[3], &c[4], &c[5], &c[6], &c[7], &c[8], &c[9])
	if errors.Is(err, sql.ErrNoRows) {
		return identityRow{}, false, nil
	}
	if err != nil {
		return identityRow{}, false, err
	}
	return identityRow{
		name: c[0].String, group: c[1].String, parent: c[2].String, role: c[3].String,
		status: c[4].String, backend: c[5].String, branch: c[6].String, workDir: c[7].String,
		reviewPR: c[8].String, fixPR: c[9].String,
	}, true, nil
}

// changedBy reports whether Save(s) changes any identity column of the stored
// row. Save keeps the stored value for an empty incoming field (COALESCE),
// except status, which it always overwrites.
func (old identityRow) changedBy(s *domain.Session) bool {
	differs := func(stored, incoming string) bool { return incoming != "" && incoming != stored }
	return differs(old.name, s.Name) ||
		differs(old.group, s.Group) ||
		differs(old.parent, s.ParentID) ||
		differs(old.role, string(s.Role)) ||
		old.status != string(s.Status) ||
		differs(old.backend, s.Backend) ||
		differs(old.branch, s.Branch) ||
		differs(old.workDir, s.WorkingDirectory) ||
		differs(old.reviewPR, s.ReviewPR) ||
		differs(old.fixPR, s.FixPR)
}
