package adapter

// The SQLSTATE codes and constraint names identity/migrate's schema
// commits to, and the helper that maps a pgx error onto them.

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
	// mfaMemberFK is the composite tenant FK on member_mfa (identity/
	// migrate's 00002 migration); a violation means memberID does not
	// belong to householdID.
	mfaMemberFK = "member_mfa_member_fk"
	// mfaMemberPK is member_mfa's primary key (member_id). BeginEnrollment
	// can race it: SELECT ... FOR UPDATE takes no lock on a row that does
	// not exist yet, so two concurrent first-time enrollments for the
	// SAME member can both reach the INSERT branch; the loser fails this
	// constraint, not mfaMemberFK.
	mfaMemberPK = "member_mfa_pkey"
	// webauthnCredentialHouseholdFK is the auto-named FK
	// member_credential.household_id -> household(id) (00003 migration).
	webauthnCredentialHouseholdFK = "member_credential_household_id_fkey"
	// webauthnCredentialMemberFK is the composite tenant FK on
	// member_credential (00003 migration); a violation means memberID
	// does not belong to householdID, mirroring mfaMemberFK.
	webauthnCredentialMemberFK = "member_credential_member_fk"
	// webauthnCredentialIDUnique is the UNIQUE constraint on
	// member_credential.credential_id (00003 migration) — defense in
	// depth, not the primary replay guard (see the migration's own doc).
	webauthnCredentialIDUnique = "member_credential_credential_id_key"
)

// isConstraintViolation reports whether err is a *pgconn.PgError with the
// given SQLSTATE code and constraint name.
func isConstraintViolation(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}
