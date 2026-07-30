// Package adapter contains the identity bounded context's outbound
// adapters: pgx-backed implementations of the ports declared in
// identity/domain, run against the schema identity/migrate owns.
//
// Every query schema-qualifies identity. explicitly rather than relying on
// search_path resolution, because callers connect with their own app's
// search path (see identity/migrate's package doc).
package adapter

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// uniqueViolation is the PostgreSQL SQLSTATE for a unique-constraint
	// violation.
	uniqueViolation = "23505"
	// foreignKeyViolation is the PostgreSQL SQLSTATE for a foreign-key
	// violation.
	foreignKeyViolation = "23503"
	// memberEmailUnique is the unique constraint on identity.member.email
	// (named in identity/migrate's baseline migration).
	memberEmailUnique = "member_email_unique"
	// memberHouseholdNameUniq is the unique index enforcing per-household
	// display-name uniqueness.
	memberHouseholdNameUniq = "member_household_name_uniq"
	// memberHouseholdFK is the auto-named FK constraint
	// member.household_id -> household.id (identity/migrate's baseline
	// migration leaves it unnamed).
	memberHouseholdFK = "member_household_id_fkey"
)

// isConstraintViolation reports whether err is a *pgconn.PgError with the
// given SQLSTATE code and constraint name.
func isConstraintViolation(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}
