package domain

import (
	"context"
	"errors"
	"time"
)

// MFA domain errors.
var (
	// ErrMFAAlreadyEnrolled is returned by BeginEnrollment and
	// ConfirmEnrollmentWithCodes when the member already has a CONFIRMED
	// TOTP enrollment: it must be disabled or disenrolled before a new one
	// can be confirmed. An UNCONFIRMED enrollment does NOT trigger this —
	// re-enrolling before confirming simply replaces it.
	ErrMFAAlreadyEnrolled = errors.New("identity: mfa already enrolled")
	// ErrMFANotEnrolled is returned by ConfirmEnrollmentWithCodes and
	// DeleteEnrollment when the member has no enrollment (confirmed or
	// not) on file, and by app.MFAService methods that require a
	// CONFIRMED enrollment when one does not exist.
	ErrMFANotEnrolled = errors.New("identity: mfa not enrolled")
	// ErrInvalidTOTPCode is returned when a submitted TOTP code does not
	// validate against the member's stored secret, including a
	// replayed (already-accepted) step — see MFARepository.RecordLoginStep.
	ErrInvalidTOTPCode = errors.New("identity: invalid totp code")
	// ErrRecoveryCodeInvalid is returned when a submitted recovery code
	// does not match any unused code on file for the member.
	ErrRecoveryCodeInvalid = errors.New("identity: invalid recovery code")
	// ErrMFAVerificationRequired is returned by app.MFAService.Disenroll
	// when neither a TOTP code nor a recovery code was submitted.
	ErrMFAVerificationRequired = errors.New("identity: a current totp code or recovery code is required")
)

// MFAEnrollment is a member's TOTP enrollment — at most one per member.
// The secret is stored encrypted at rest (TOTPSecretEnc); it is never
// persisted or logged in plaintext. ConfirmedAt is nil until the member
// proves control of their authenticator app by submitting one valid code
// back (app.MFAService.ConfirmEnrollment); an unconfirmed enrollment is
// inert — ignored by every check that requires an active enrollment.
type MFAEnrollment struct {
	MemberID      MemberID
	HouseholdID   HouseholdID
	TOTPSecretEnc []byte
	ConfirmedAt   *time.Time
	// LastTOTPStep is the RFC 6238 step of the most recently accepted
	// LOGIN TOTP code, or nil when the member has never completed login
	// MFA verification. It is the durable replay guard
	// MFARepository.RecordLoginStep maintains — see that method's doc.
	LastTOTPStep *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Confirmed reports whether e represents an active enrollment — i.e. the
// member has proven control of their authenticator app.
func (e *MFAEnrollment) Confirmed() bool {
	return e != nil && e.ConfirmedAt != nil
}

// RecoveryCode is one single-use recovery code. Only CodeHash (an
// argon2id PHC string, the same format and KDF as member passwords) is
// ever persisted; the raw code is shown to the member exactly once, at
// generation time, and never stored.
type RecoveryCode struct {
	ID        RecoveryCodeID
	MemberID  MemberID
	CodeHash  string
	UsedAt    *time.Time
	CreatedAt time.Time
}

// Used reports whether the code has already been consumed.
func (c RecoveryCode) Used() bool {
	return c.UsedAt != nil
}

// MFARepository is the outbound port for persisting a member's TOTP
// enrollment and recovery codes. Implementations live in identity/adapter.
//
// Error contracts:
//   - GetEnrollment returns ErrMFANotEnrolled when the member has no
//     enrollment row (confirmed or not).
//   - BeginEnrollment upserts an UNCONFIRMED enrollment for memberID,
//     replacing any existing unconfirmed row in place. It returns
//     ErrMFAAlreadyEnrolled when the existing row is already CONFIRMED
//     (the caller must disable/disenroll first), and ErrMemberNotFound
//     when memberID does not belong to householdID (no such row exists
//     yet), when an existing row belongs to a DIFFERENT household than
//     householdID (a tenant-consistency guard: implementations must
//     never touch another household's row), AND when householdID itself
//     does not exist at all — all three reported identically as
//     ErrMemberNotFound so neither leaks which one occurred.
//   - ConfirmEnrollmentWithCodes atomically, in a single transaction,
//     sets confirmed_at = now on the member's existing unconfirmed row
//     AND replaces their recovery codes with one fresh row per hash —
//     the two writes MUST be atomic (not two separate calls) so that
//     two concurrent callers racing to confirm the SAME still-unconfirmed
//     enrollment can never both "win": the loser's hashes are never
//     persisted, and it receives ErrMFAAlreadyEnrolled rather than
//     silently returning raw codes to its caller that were never
//     actually stored. Scoped to householdID as a defense-in-depth tenant
//     check, mirroring DeleteEnrollment's own: this method must never
//     confirm a row belonging to a DIFFERENT household. Returns
//     ErrMFANotEnrolled when no row exists in that household, and
//     ErrMFAAlreadyEnrolled when the row is already confirmed (including
//     by a racing, now-committed, concurrent call to this same method).
//   - DeleteEnrollment removes the member's enrollment (confirmed or
//     not), cascading its recovery codes, scoped to householdID as a
//     defense-in-depth tenant check. Returns ErrMFANotEnrolled when no
//     row exists in that household.
//   - ListUnusedRecoveryCodes returns every not-yet-used recovery code
//     for memberID (never used ones), for verifying a submitted code
//     against.
//   - MarkRecoveryCodeUsed sets used_at = now on the given code id, IF
//     AND ONLY IF the code is still unused — implementations must apply
//     this as a single conditional UPDATE (not a read-then-write), so two
//     concurrent redemptions of the same code can never both succeed.
//     Returns ErrRecoveryCodeInvalid when the code does not exist or has
//     already been used. It is the caller's responsibility to have
//     already matched the code's hash.
//   - RecordLoginStep durably persists step as memberID's last-accepted
//     login TOTP step, IF AND ONLY IF the stored value is still nil or
//     strictly less than step — an atomic, race-safe replay guard
//     (implementations must apply this as a single conditional UPDATE,
//     not a read-then-write). Returns ErrInvalidTOTPCode when the guard
//     fails: either because step has already been used or superseded (a
//     replay, including one that lost a race to a concurrent call for a
//     later step) or because memberID has no member_mfa row — both
//     reported identically, since the caller has always already
//     confirmed enrollment via GetEnrollment before calling this.
type MFARepository interface {
	GetEnrollment(ctx context.Context, memberID MemberID) (*MFAEnrollment, error)
	BeginEnrollment(ctx context.Context, memberID MemberID, householdID HouseholdID, secretEnc []byte) error
	ConfirmEnrollmentWithCodes(ctx context.Context, householdID HouseholdID, memberID MemberID, recoveryCodeHashes []string) error
	DeleteEnrollment(ctx context.Context, householdID HouseholdID, memberID MemberID) error
	ListUnusedRecoveryCodes(ctx context.Context, memberID MemberID) ([]RecoveryCode, error)
	MarkRecoveryCodeUsed(ctx context.Context, codeID RecoveryCodeID) error
	RecordLoginStep(ctx context.Context, memberID MemberID, step int64) error
}
