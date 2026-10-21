package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrStaleReferences: the database holds references to the structures written before they were logical URLs (ADR
// 0154 §9: the owner and the project of a version, the owner unit and the project of a change). The storage is
// greenfield, nothing migrates them: such a database is started again from zero.
var ErrStaleReferences = errors.New("the database holds owner / project references written as bare keys, before they became logical URLs (ADR 0154 §9): start it from zero")

// CheckReferences refuses a database written before the references became logical URLs, rather than serve reads that
// fail on them. Both dialects.
func CheckReferences(ctx context.Context, db *sql.DB) error {
	for _, q := range []string{
		`SELECT 1 FROM change WHERE owner_org NOT LIKE 'lnk:%' OR project_id NOT LIKE 'lnk:%' LIMIT 1`,
		`SELECT 1 FROM node_version WHERE owner NOT LIKE 'lnk:%' LIMIT 1`,
		`SELECT 1 FROM node WHERE project NOT LIKE 'lnk:%' LIMIT 1`,
	} {
		var one int
		switch err := db.QueryRowContext(ctx, q).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("check references: %w", err)
		default:
			return ErrStaleReferences
		}
	}
	return nil
}
