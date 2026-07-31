package adapter

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	identity "github.com/ericfisherdev/nestcore/identity/domain"
)

// Postgres SQLSTATE codes this adapter reacts to.
const (
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
)

// Constraint names the canonical "photo" table shape commits to (see the
// package doc): photoHouseholdFK and photoUploaderFK are Postgres
// auto-named inline FKs (<table>_<column>_fkey), and
// photoHouseholdContentHashUniq is the partial unique index every
// consumer's own migration must declare verbatim.
const (
	photoHouseholdFK              = "photo_household_id_fkey"
	photoUploaderFK               = "photo_uploaded_by_fkey"
	photoHouseholdContentHashUniq = "photo_household_content_hash_uniq"
)

// isUniqueViolation reports whether err is a unique-constraint violation on
// the named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}

// mapFKViolation maps a photo-table FK violation to its identity sentinel,
// or nil when err is not a recognized FK violation.
func mapFKViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != foreignKeyViolation {
		return nil
	}
	switch pgErr.ConstraintName {
	case photoHouseholdFK:
		return identity.ErrHouseholdNotFound
	case photoUploaderFK:
		return identity.ErrMemberNotFound
	default:
		return nil
	}
}

// nullableText renders s as a nullable text query argument, mapping a blank
// string to SQL NULL. Used for photo.content_sha256, which is NULL for a
// legacy photo rather than an empty string — matching the partial unique
// index, which only ever applies where content_sha256 IS NOT NULL.
func nullableText(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// row is the read surface shared by pgx.Row and pgx.Rows for scan helpers.
type row interface {
	Scan(dest ...any) error
}
