package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfisherdev/nestcore/db"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// WebAuthnCredentialRepository is the pgx-backed
// domain.WebAuthnCredentialRepository. UUIDs are passed and scanned as
// text, mirroring MFARepository's and CredentialRepository's convention
// (no pgx UUID codec registration) — except AAGUID, which pgx's native
// uuid.UUID support scans directly since it is never used as a lookup
// key, only stored/returned opaquely.
type WebAuthnCredentialRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.WebAuthnCredentialRepository = (*WebAuthnCredentialRepository)(nil)

// NewWebAuthnCredentialRepository constructs the repository with an
// injected query executor (a db.TX, satisfied by both *pgxpool.Pool and
// pgx.Tx).
func NewWebAuthnCredentialRepository(dbtx db.TX) *WebAuthnCredentialRepository {
	if dbtx == nil {
		panic("adapter: NewWebAuthnCredentialRepository requires a non-nil db.TX")
	}
	return &WebAuthnCredentialRepository{dbtx: dbtx}
}

// ListByMember returns every credential registered by memberID within
// householdID, oldest first — ties on created_at (two credentials
// registered within the same clock tick) broken deterministically by id,
// its own tie-break secondary key. A member with none, or a memberID
// belonging to a different household than householdID, returns an empty
// slice, never an error.
func (r *WebAuthnCredentialRepository) ListByMember(ctx context.Context, householdID domain.HouseholdID, memberID domain.MemberID) ([]domain.WebAuthnCredential, error) {
	const q = `
		SELECT id, household_id, credential_id, public_key, sign_count, transports,
		       aaguid, nickname, user_handle, created_at, last_used_at
		  FROM identity.member_credential
		 WHERE member_id = $1 AND household_id = $2
		 ORDER BY created_at, id`

	rows, err := r.dbtx.Query(ctx, q, memberID.String(), householdID.String())
	if err != nil {
		return nil, fmt.Errorf("list webauthn credentials: %w", err)
	}
	defer rows.Close()

	creds := make([]domain.WebAuthnCredential, 0)
	for rows.Next() {
		var (
			idStr          string
			householdIDStr string
			aaguid         *uuid.UUID
			cred           = domain.WebAuthnCredential{MemberID: memberID}
		)
		if err := rows.Scan(
			&idStr, &householdIDStr, &cred.CredentialID, &cred.PublicKey, &cred.SignCount, &cred.Transports,
			&aaguid, &cred.Nickname, &cred.UserHandle, &cred.CreatedAt, &cred.LastUsedAt,
		); err != nil {
			return nil, fmt.Errorf("list webauthn credentials: scan: %w", err)
		}
		id, err := domain.ParseWebAuthnCredentialID(idStr)
		if err != nil {
			return nil, fmt.Errorf("list webauthn credentials: parse id: %w", err)
		}
		householdID, err := domain.ParseHouseholdID(householdIDStr)
		if err != nil {
			return nil, fmt.Errorf("list webauthn credentials: parse household id: %w", err)
		}
		cred.ID = id
		cred.HouseholdID = householdID
		cred.AAGUID = aaguid
		creds = append(creds, cred)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webauthn credentials: %w", err)
	}
	return creds, nil
}

// Create persists a newly registered credential. Returns
// domain.ErrHouseholdNotFound when householdID does not exist,
// domain.ErrMemberNotFound when cred.MemberID does not belong to
// householdID (FK violations), and domain.ErrWebAuthnCredentialExists
// when cred.CredentialID is already registered (unique violation).
func (r *WebAuthnCredentialRepository) Create(ctx context.Context, householdID domain.HouseholdID, cred *domain.WebAuthnCredential) error {
	const q = `
		INSERT INTO identity.member_credential
			(id, household_id, member_id, credential_id, public_key, sign_count,
			 transports, aaguid, nickname, user_handle)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	_, err := r.dbtx.Exec(ctx, q,
		cred.ID.String(), householdID.String(), cred.MemberID.String(), cred.CredentialID, cred.PublicKey, cred.SignCount,
		cred.Transports, cred.AAGUID, cred.Nickname, cred.UserHandle,
	)
	if err != nil {
		if mapped := mapWebAuthnCredentialViolation(err); mapped != nil {
			return mapped
		}
		return fmt.Errorf("create webauthn credential: %w", err)
	}
	return nil
}

// mapWebAuthnCredentialViolation maps a member_credential constraint
// violation to its domain sentinel, or nil when err is not one of the
// recognized violations.
func mapWebAuthnCredentialViolation(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	switch {
	case pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == webauthnCredentialHouseholdFK:
		return domain.ErrHouseholdNotFound
	case pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == webauthnCredentialMemberFK:
		return domain.ErrMemberNotFound
	case pgErr.Code == uniqueViolation && pgErr.ConstraintName == webauthnCredentialIDUnique:
		return domain.ErrWebAuthnCredentialExists
	default:
		return nil
	}
}

// Rename updates the nickname on the credential identified by id,
// scoped to memberID and householdID. Returns
// domain.ErrWebAuthnCredentialNotFound when no row matches all three.
func (r *WebAuthnCredentialRepository) Rename(ctx context.Context, householdID domain.HouseholdID, memberID domain.MemberID, id domain.WebAuthnCredentialID, nickname string) error {
	const q = `
		UPDATE identity.member_credential
		   SET nickname = $4
		 WHERE id = $1 AND member_id = $2 AND household_id = $3`

	tag, err := r.dbtx.Exec(ctx, q, id.String(), memberID.String(), householdID.String(), nickname)
	if err != nil {
		return fmt.Errorf("rename webauthn credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrWebAuthnCredentialNotFound
	}
	return nil
}

// Delete removes the credential identified by id, scoped to memberID
// and householdID. Returns domain.ErrWebAuthnCredentialNotFound when no
// row matches all three.
func (r *WebAuthnCredentialRepository) Delete(ctx context.Context, householdID domain.HouseholdID, memberID domain.MemberID, id domain.WebAuthnCredentialID) error {
	const q = `DELETE FROM identity.member_credential WHERE id = $1 AND member_id = $2 AND household_id = $3`

	tag, err := r.dbtx.Exec(ctx, q, id.String(), memberID.String(), householdID.String())
	if err != nil {
		return fmt.Errorf("delete webauthn credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrWebAuthnCredentialNotFound
	}
	return nil
}

// FindByUserHandle resolves handle to its owning member and every one of
// that member's registered credentials — the usernameless login lookup,
// driven by member_credential_user_handle_idx. Returns
// domain.ErrMemberNotFound when handle matches no row.
func (r *WebAuthnCredentialRepository) FindByUserHandle(ctx context.Context, handle []byte) (domain.MemberID, []domain.WebAuthnCredential, error) {
	const q = `
		SELECT id, household_id, member_id, credential_id, public_key, sign_count, transports,
		       aaguid, nickname, user_handle, created_at, last_used_at
		  FROM identity.member_credential
		 WHERE user_handle = $1
		 ORDER BY created_at, id`

	rows, err := r.dbtx.Query(ctx, q, handle)
	if err != nil {
		return domain.MemberID{}, nil, fmt.Errorf("find webauthn credentials by user handle: %w", err)
	}
	defer rows.Close()

	var (
		memberID domain.MemberID
		creds    []domain.WebAuthnCredential
	)
	for rows.Next() {
		var (
			idStr          string
			householdIDStr string
			memberIDStr    string
			aaguid         *uuid.UUID
			cred           domain.WebAuthnCredential
		)
		if err := rows.Scan(
			&idStr, &householdIDStr, &memberIDStr, &cred.CredentialID, &cred.PublicKey, &cred.SignCount, &cred.Transports,
			&aaguid, &cred.Nickname, &cred.UserHandle, &cred.CreatedAt, &cred.LastUsedAt,
		); err != nil {
			return domain.MemberID{}, nil, fmt.Errorf("find webauthn credentials by user handle: scan: %w", err)
		}
		id, err := domain.ParseWebAuthnCredentialID(idStr)
		if err != nil {
			return domain.MemberID{}, nil, fmt.Errorf("find webauthn credentials by user handle: parse id: %w", err)
		}
		householdID, err := domain.ParseHouseholdID(householdIDStr)
		if err != nil {
			return domain.MemberID{}, nil, fmt.Errorf("find webauthn credentials by user handle: parse household id: %w", err)
		}
		parsedMemberID, err := domain.ParseMemberID(memberIDStr)
		if err != nil {
			return domain.MemberID{}, nil, fmt.Errorf("find webauthn credentials by user handle: parse member id: %w", err)
		}
		cred.ID = id
		cred.HouseholdID = householdID
		cred.MemberID = parsedMemberID
		cred.AAGUID = aaguid
		// user_handle carries no uniqueness constraint (a plain index —
		// see the migration's own doc), so a collision across TWO
		// members must fail loudly rather than silently resolve to
		// whichever row happened to scan last: that would authenticate
		// the wrong member.
		if len(creds) > 0 && parsedMemberID != memberID {
			return domain.MemberID{}, nil, fmt.Errorf(
				"find webauthn credentials by user handle: handle resolves to more than one member (%s and %s)",
				memberID, parsedMemberID)
		}
		memberID = parsedMemberID
		creds = append(creds, cred)
	}
	if err := rows.Err(); err != nil {
		return domain.MemberID{}, nil, fmt.Errorf("find webauthn credentials by user handle: %w", err)
	}
	if len(creds) == 0 {
		return domain.MemberID{}, nil, domain.ErrMemberNotFound
	}
	return memberID, creds, nil
}

// UpdateAfterAssertion persists the authenticator's new signature
// counter and last-used timestamp for the credential identified by its
// raw WebAuthn credential id (credential_id, globally unique — not this
// row's own WebAuthnCredentialID) — but ONLY when usedAt is not older
// than whatever last_used_at is already on file (NULL, on a credential's
// first assertion, always qualifies). This is a monotonic guard against
// a race between two concurrently successful assertions on the SAME
// credential, mirroring MFARepository.RecordLoginStep's own
// last_totp_step guard, though the caller contract differs (see below).
//
// Returns domain.ErrWebAuthnCredentialNotFound only when credentialID
// matches no row AT ALL. When the row exists but the guard skipped the
// write (a newer assertion already won the race), this returns nil, NOT
// an error: the assertion that reaches this method has ALREADY been
// cryptographically verified by WebAuthnService before this call —
// losing the bookkeeping race here must never fail an otherwise-valid
// login or step-up, only skip a write that would have regressed stored
// state backward in time.
func (r *WebAuthnCredentialRepository) UpdateAfterAssertion(ctx context.Context, credentialID []byte, signCount uint32, usedAt time.Time) error {
	// The guard is monotonic in last_used_at. Two assertions can carry
	// the SAME usedAt (e.g. a clock with coarser-than-actual
	// resolution), and a plain "<=" would let a lower sign_count win
	// that tie purely by arrival order. Comparing sign_count on an exact
	// tie removes that arrival-order dependence. A strictly newer usedAt
	// always wins even with a lower sign_count: the counter policy
	// belongs to WebAuthnService, which has already verified the
	// assertion before this method is ever called.
	const q = `
		UPDATE identity.member_credential
		   SET sign_count = $2, last_used_at = $3
		 WHERE credential_id = $1
		   AND (last_used_at IS NULL
		        OR last_used_at < $3
		        OR (last_used_at = $3 AND sign_count <= $2))`

	tag, err := r.dbtx.Exec(ctx, q, credentialID, signCount, usedAt)
	if err != nil {
		return fmt.Errorf("update webauthn credential after assertion: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	const existsQ = `SELECT EXISTS (SELECT 1 FROM identity.member_credential WHERE credential_id = $1)`
	var exists bool
	if err := r.dbtx.QueryRow(ctx, existsQ, credentialID).Scan(&exists); err != nil {
		return fmt.Errorf("update webauthn credential after assertion: check existence: %w", err)
	}
	if !exists {
		return domain.ErrWebAuthnCredentialNotFound
	}
	return nil
}
