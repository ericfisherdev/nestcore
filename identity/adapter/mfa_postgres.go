package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// MFARepository is the pgx-backed domain.MFARepository. UUIDs are passed
// and scanned as text, mirroring CredentialRepository's and
// MemberRepository's convention (no pgx UUID codec registration).
type MFARepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.MFARepository = (*MFARepository)(nil)

// NewMFARepository constructs the repository with an injected query
// executor (a db.TX, satisfied by both *pgxpool.Pool and pgx.Tx).
func NewMFARepository(dbtx db.TX) *MFARepository {
	if dbtx == nil {
		panic("adapter: NewMFARepository requires a non-nil db.TX")
	}
	return &MFARepository{dbtx: dbtx}
}

// GetEnrollment returns memberID's enrollment (confirmed or not), or
// domain.ErrMFANotEnrolled when no row exists.
func (r *MFARepository) GetEnrollment(ctx context.Context, memberID domain.MemberID) (*domain.MFAEnrollment, error) {
	const q = `
		SELECT household_id, totp_secret_enc, confirmed_at, last_totp_step, created_at, updated_at
		  FROM identity.member_mfa
		 WHERE member_id = $1`

	var (
		householdIDStr string
		enrollment     = &domain.MFAEnrollment{MemberID: memberID}
	)
	err := r.dbtx.QueryRow(ctx, q, memberID.String()).Scan(
		&householdIDStr, &enrollment.TOTPSecretEnc, &enrollment.ConfirmedAt, &enrollment.LastTOTPStep,
		&enrollment.CreatedAt, &enrollment.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMFANotEnrolled
		}
		return nil, fmt.Errorf("get mfa enrollment: %w", err)
	}
	householdID, err := domain.ParseHouseholdID(householdIDStr)
	if err != nil {
		return nil, fmt.Errorf("get mfa enrollment: parse household id: %w", err)
	}
	enrollment.HouseholdID = householdID
	return enrollment, nil
}

// mfaTxBeginner is the slice of a pgx executor BeginEnrollment and
// ConfirmEnrollmentWithCodes need to open their own transaction,
// satisfied by both *pgxpool.Pool and pgx.Tx.
type mfaTxBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// errMFAEnrollmentRaced is beginEnrollmentOnce's internal signal that its
// INSERT lost a race for the row to a concurrent first-time enrollment
// (see that method's own doc) — BeginEnrollment retries once against it
// rather than surfacing this to the caller.
var errMFAEnrollmentRaced = errors.New("adapter: begin mfa enrollment: lost the insert race")

// BeginEnrollment upserts an unconfirmed enrollment for memberID,
// retrying beginEnrollmentOnce a single time if the first attempt loses
// a concurrent first-time-enrollment race (see that method's own doc for
// why SELECT ... FOR UPDATE cannot protect against it) — the retry
// re-runs against the now-existing row, which the FOR UPDATE lock path
// resolves normally.
//
// Returns domain.ErrMFAAlreadyEnrolled when the existing row is already
// CONFIRMED, and domain.ErrMemberNotFound both for a genuinely unknown
// member/household (FK violation on insert) and when an existing row
// belongs to a DIFFERENT household than householdID — a defense-in-depth
// tenant guard: this method must never overwrite another household's
// pending secret, and reports both cases identically so neither leaks
// which one occurred.
func (r *MFARepository) BeginEnrollment(ctx context.Context, memberID domain.MemberID, householdID domain.HouseholdID, secretEnc []byte) error {
	err := r.beginEnrollmentOnce(ctx, memberID, householdID, secretEnc)
	if errors.Is(err, errMFAEnrollmentRaced) {
		err = r.beginEnrollmentOnce(ctx, memberID, householdID, secretEnc)
		if errors.Is(err, errMFAEnrollmentRaced) {
			// Losing the race twice in a row means a THIRD concurrent
			// caller is also racing this same first-time enrollment —
			// astronomically unlikely in practice. Surface a plain error
			// rather than retrying indefinitely or leaking the internal
			// sentinel.
			return fmt.Errorf("begin mfa enrollment: %s: repeated insert race", memberID)
		}
	}
	return err
}

// beginEnrollmentOnce is BeginEnrollment's single-attempt body: it opens
// a transaction that locks any EXISTING row (SELECT ... FOR UPDATE)
// before deciding how to proceed — closing the race a plain
// INSERT ... ON CONFLICT DO UPDATE would leave open between two
// concurrent BeginEnrollment calls for an ALREADY-enrolled member, while
// still distinguishing WHY a conflicting row blocks the write (see the
// two error returns below), which a single ON CONFLICT ... WHERE clause
// cannot do from its zero-rows-returned result alone.
//
// This locking scheme has a gap for a member with NO existing row:
// Postgres's FOR UPDATE takes no lock when zero rows match, so two
// concurrent callers racing a member's FIRST-EVER enrollment can both
// reach the INSERT branch below. The loser fails member_mfa's primary
// key (mfaMemberPK), not the FK — mapped here to errMFAEnrollmentRaced
// for BeginEnrollment to retry, rather than a case this method resolves
// itself.
func (r *MFARepository) beginEnrollmentOnce(ctx context.Context, memberID domain.MemberID, householdID domain.HouseholdID, secretEnc []byte) error {
	beginner, ok := r.dbtx.(mfaTxBeginner)
	if !ok {
		return errors.New("begin mfa enrollment: executor does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin mfa enrollment: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		existingHouseholdID string
		confirmedAt         *time.Time
	)
	lookupErr := tx.QueryRow(ctx, `SELECT household_id, confirmed_at FROM identity.member_mfa WHERE member_id = $1 FOR UPDATE`, memberID.String()).
		Scan(&existingHouseholdID, &confirmedAt)
	switch {
	case errors.Is(lookupErr, pgx.ErrNoRows):
		const insert = `
			INSERT INTO identity.member_mfa (member_id, household_id, totp_secret_enc, confirmed_at)
			VALUES ($1, $2, $3, NULL)`
		if _, err := tx.Exec(ctx, insert, memberID.String(), householdID.String(), secretEnc); err != nil {
			switch {
			case isConstraintViolation(err, foreignKeyViolation, mfaMemberFK):
				return domain.ErrMemberNotFound
			case isConstraintViolation(err, uniqueViolation, mfaMemberPK):
				return errMFAEnrollmentRaced
			}
			return fmt.Errorf("begin mfa enrollment: insert: %w", err)
		}
	case lookupErr != nil:
		return fmt.Errorf("begin mfa enrollment: lookup: %w", lookupErr)
	case existingHouseholdID != householdID.String():
		// The row exists but under a DIFFERENT household than the
		// caller supplied — never touch it. Reported the same as an
		// unknown member so no household-boundary information leaks.
		return domain.ErrMemberNotFound
	case confirmedAt != nil:
		return domain.ErrMFAAlreadyEnrolled
	default:
		const update = `
			UPDATE identity.member_mfa
			   SET totp_secret_enc = $2,
			       confirmed_at    = NULL,
			       updated_at      = now()
			 WHERE member_id = $1`
		if _, err := tx.Exec(ctx, update, memberID.String(), secretEnc); err != nil {
			return fmt.Errorf("begin mfa enrollment: update: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("begin mfa enrollment: commit: %w", err)
	}
	return nil
}

// ConfirmEnrollmentWithCodes atomically confirms memberID's enrollment
// and replaces their recovery codes with one fresh row per hash, in a
// single transaction that locks the row (SELECT ... FOR UPDATE) before
// confirming — see the port doc for why this must be one atomic
// operation: it is what makes two concurrent callers racing to confirm
// the SAME still-unconfirmed enrollment resolve to exactly one winner,
// with the loser's hashes never persisted at all.
func (r *MFARepository) ConfirmEnrollmentWithCodes(ctx context.Context, memberID domain.MemberID, recoveryCodeHashes []string) error {
	beginner, ok := r.dbtx.(mfaTxBeginner)
	if !ok {
		return errors.New("confirm mfa enrollment: executor does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("confirm mfa enrollment: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var confirmedAt *time.Time
	lookupErr := tx.QueryRow(ctx, `SELECT confirmed_at FROM identity.member_mfa WHERE member_id = $1 FOR UPDATE`, memberID.String()).Scan(&confirmedAt)
	switch {
	case errors.Is(lookupErr, pgx.ErrNoRows):
		return domain.ErrMFANotEnrolled
	case lookupErr != nil:
		return fmt.Errorf("confirm mfa enrollment: lookup: %w", lookupErr)
	case confirmedAt != nil:
		// Already confirmed — including by a concurrent racing confirm
		// that committed first while this call waited on the row lock.
		return domain.ErrMFAAlreadyEnrolled
	}

	if _, err := tx.Exec(ctx, `UPDATE identity.member_mfa SET confirmed_at = now(), updated_at = now() WHERE member_id = $1`, memberID.String()); err != nil {
		return fmt.Errorf("confirm mfa enrollment: confirm: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity.member_recovery_code WHERE member_id = $1`, memberID.String()); err != nil {
		return fmt.Errorf("confirm mfa enrollment: delete existing recovery codes: %w", err)
	}
	const insert = `
		INSERT INTO identity.member_recovery_code (id, member_id, code_hash)
		VALUES ($1, $2, $3)`
	for _, hash := range recoveryCodeHashes {
		id := domain.NewRecoveryCodeID()
		if _, err := tx.Exec(ctx, insert, id.String(), memberID.String(), hash); err != nil {
			return fmt.Errorf("confirm mfa enrollment: insert recovery code: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("confirm mfa enrollment: commit: %w", err)
	}
	return nil
}

// DeleteEnrollment removes memberID's enrollment (confirmed or not),
// cascading its recovery codes, scoped to householdID as a
// defense-in-depth tenant check. Returns domain.ErrMFANotEnrolled when
// no row exists in that household.
func (r *MFARepository) DeleteEnrollment(ctx context.Context, householdID domain.HouseholdID, memberID domain.MemberID) error {
	const q = `DELETE FROM identity.member_mfa WHERE member_id = $1 AND household_id = $2`

	tag, err := r.dbtx.Exec(ctx, q, memberID.String(), householdID.String())
	if err != nil {
		return fmt.Errorf("delete mfa enrollment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrMFANotEnrolled
	}
	return nil
}

// ListUnusedRecoveryCodes returns every not-yet-used recovery code for
// memberID, oldest first — ties on created_at (every code in one batch
// shares the same transaction-local now() value) broken deterministically
// by id, mirroring WebAuthnCredentialRepository.ListByMember's own
// tiebreaker.
func (r *MFARepository) ListUnusedRecoveryCodes(ctx context.Context, memberID domain.MemberID) ([]domain.RecoveryCode, error) {
	const q = `
		SELECT id, code_hash, created_at
		  FROM identity.member_recovery_code
		 WHERE member_id = $1
		   AND used_at IS NULL
		 ORDER BY created_at, id`

	rows, err := r.dbtx.Query(ctx, q, memberID.String())
	if err != nil {
		return nil, fmt.Errorf("list unused recovery codes: %w", err)
	}
	defer rows.Close()

	var codes []domain.RecoveryCode
	for rows.Next() {
		var (
			idStr string
			code  domain.RecoveryCode
		)
		if err := rows.Scan(&idStr, &code.CodeHash, &code.CreatedAt); err != nil {
			return nil, fmt.Errorf("list unused recovery codes: scan: %w", err)
		}
		id, err := domain.ParseRecoveryCodeID(idStr)
		if err != nil {
			return nil, fmt.Errorf("list unused recovery codes: parse id: %w", err)
		}
		code.ID = id
		code.MemberID = memberID
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unused recovery codes: %w", err)
	}
	return codes, nil
}

// MarkRecoveryCodeUsed sets used_at = now() on codeID.
func (r *MFARepository) MarkRecoveryCodeUsed(ctx context.Context, codeID domain.RecoveryCodeID) error {
	const q = `UPDATE identity.member_recovery_code SET used_at = now() WHERE id = $1 AND used_at IS NULL`

	tag, err := r.dbtx.Exec(ctx, q, codeID.String())
	if err != nil {
		return fmt.Errorf("mark recovery code used: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark recovery code used: %s: %w", codeID.String(), domain.ErrRecoveryCodeInvalid)
	}
	return nil
}

// RecordLoginStep durably persists step as memberID's last-accepted
// login TOTP step, guarded by a single conditional UPDATE so the
// check-and-set is atomic against a concurrent racing call: the WHERE
// clause only matches a row whose last_totp_step is still NULL or
// strictly less than step, closing the same race BeginEnrollment's own
// doc discusses (two concurrent login attempts must never both "win"
// and accept the same or an out-of-order step).
func (r *MFARepository) RecordLoginStep(ctx context.Context, memberID domain.MemberID, step int64) error {
	const q = `
		UPDATE identity.member_mfa
		   SET last_totp_step = $2,
		       updated_at     = now()
		 WHERE member_id = $1
		   AND (last_totp_step IS NULL OR last_totp_step < $2)`

	tag, err := r.dbtx.Exec(ctx, q, memberID.String(), step)
	if err != nil {
		return fmt.Errorf("record mfa login step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrInvalidTOTPCode
	}
	return nil
}
