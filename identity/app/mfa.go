package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ericfisherdev/nestcore/identity/domain"
)

// recoveryCodeCount is how many single-use recovery codes are generated
// at confirmation.
const recoveryCodeCount = 10

// secretCipher is the slice of the crypto cipher MFAService depends on
// (ISP), satisfied by *crypto.Cipher (github.com/ericfisherdev/nestcore/crypto).
type secretCipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// totpProvider is the minimal seam over RFC 6238 TOTP generation/
// validation MFAService depends on (ISP + DIP), satisfied by
// github.com/ericfisherdev/nestcore/totp.Provider and faked in tests so
// they never need a real clock-synchronized authenticator. MatchStep is a
// separate method from Validate rather than an additional return value on
// it: only VerifyLoginCode needs a replay-comparable step number.
type totpProvider interface {
	GenerateSecret(issuer, accountName string) (secret, otpauthURL string, err error)
	Validate(code, secret string) bool
	MatchStep(code, secret string) (step int64, ok bool)
}

// MFAService orchestrates TOTP enrollment, confirmation, single-use
// recovery codes, and replay-guarded login verification. It is the
// identity context's use-case boundary for the credential surface
// completing the identity package (NSTR-130); the app-level enrollment
// UI, and any owner-administered reset flow, are composed on top of this
// service by each app, not implemented here.
type MFAService struct {
	repo   domain.MFARepository
	cipher secretCipher
	totp   totpProvider
	hasher passwordHasher
	logger *slog.Logger
}

// NewMFAService constructs the service with injected dependencies. All
// five are required.
func NewMFAService(repo domain.MFARepository, cipher secretCipher, totp totpProvider, hasher passwordHasher, logger *slog.Logger) (*MFAService, error) {
	if repo == nil {
		return nil, errors.New("identity: NewMFAService requires a non-nil MFARepository")
	}
	if cipher == nil {
		return nil, errors.New("identity: NewMFAService requires a non-nil cipher")
	}
	if totp == nil {
		return nil, errors.New("identity: NewMFAService requires a non-nil totp provider")
	}
	if hasher == nil {
		return nil, errors.New("identity: NewMFAService requires a non-nil password hasher")
	}
	if logger == nil {
		return nil, errors.New("identity: NewMFAService requires a non-nil logger")
	}
	return &MFAService{repo: repo, cipher: cipher, totp: totp, hasher: hasher, logger: logger}, nil
}

// Status returns the member's current enrollment, or nil if none exists
// (confirmed or not). It never returns domain.ErrMFANotEnrolled — that
// sentinel is folded into the (nil, nil) result — so callers building a
// display status do not need to special-case the not-enrolled error.
func (s *MFAService) Status(ctx context.Context, memberID domain.MemberID) (*domain.MFAEnrollment, error) {
	enrollment, err := s.repo.GetEnrollment(ctx, memberID)
	if err != nil {
		if errors.Is(err, domain.ErrMFANotEnrolled) {
			return nil, nil
		}
		return nil, fmt.Errorf("mfa: get status: %w", err)
	}
	return enrollment, nil
}

// BeginEnrollment generates a fresh TOTP secret for memberID and persists
// it unconfirmed, replacing any existing unconfirmed enrollment in place
// (a re-enroll before confirming simply starts over). It returns the raw
// secret (for manual entry) and its otpauth:// URL (for QR rendering);
// neither is persisted in plaintext, and the caller must not log either.
// issuer labels the entry's issuing app (e.g. "Nestova"/"Nestorage") and
// accountName labels the member (their display name — members are not
// guaranteed to have an email).
//
// Returns domain.ErrMFAAlreadyEnrolled when the member already has a
// CONFIRMED enrollment (it must be disabled or disenrolled first).
func (s *MFAService) BeginEnrollment(ctx context.Context, memberID domain.MemberID, householdID domain.HouseholdID, issuer, accountName string) (secret, otpauthURL string, err error) {
	secret, otpauthURL, err = s.totp.GenerateSecret(issuer, accountName)
	if err != nil {
		return "", "", fmt.Errorf("mfa: generate secret: %w", err)
	}
	secretEnc, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return "", "", fmt.Errorf("mfa: encrypt secret: %w", err)
	}
	if err := s.repo.BeginEnrollment(ctx, memberID, householdID, secretEnc); err != nil {
		return "", "", err
	}
	s.logger.InfoContext(ctx, "mfa enrollment started", "member_id", memberID.String())
	return secret, otpauthURL, nil
}

// ConfirmEnrollment validates code against the member's pending secret
// and, on success, marks the enrollment confirmed and generates a fresh
// batch of recoveryCodeCount recovery codes (only ever generated AFTER
// confirmation, never before), confirming and storing them in one atomic
// repository call (domain.MFARepository.ConfirmEnrollmentWithCodes — see
// its doc for why this must not be two separate writes). It returns the
// raw codes — shown to the member exactly once; only their hashes are
// persisted.
//
// Returns domain.ErrMFANotEnrolled when no enrollment exists,
// domain.ErrMFAAlreadyEnrolled when it is already confirmed (including by
// a racing concurrent confirm that won), and domain.ErrInvalidTOTPCode
// when code does not validate.
func (s *MFAService) ConfirmEnrollment(ctx context.Context, memberID domain.MemberID, code string) ([]string, error) {
	enrollment, err := s.repo.GetEnrollment(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if enrollment.Confirmed() {
		return nil, domain.ErrMFAAlreadyEnrolled
	}
	secret, err := s.cipher.Decrypt(enrollment.TOTPSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("mfa: decrypt secret: %w", err)
	}
	if !s.totp.Validate(strings.TrimSpace(code), string(secret)) {
		return nil, domain.ErrInvalidTOTPCode
	}

	// Generate and hash the recovery codes BEFORE touching the
	// repository (pure computation, no DB), so the confirm+store-codes
	// write below is a single atomic transaction.
	codes, hashes, err := s.generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.repo.ConfirmEnrollmentWithCodes(ctx, memberID, hashes); err != nil {
		return nil, fmt.Errorf("mfa: confirm enrollment: %w", err)
	}
	s.logger.InfoContext(ctx, "mfa enrollment confirmed", "member_id", memberID.String())
	return codes, nil
}

// Disenroll removes the member's own MFA enrollment (and its recovery
// codes), after verifying EITHER a current TOTP code OR an unused
// recovery code — whichever the caller supplies (exactly one of
// totpCode/recoveryCode is expected to be non-blank; if both are, the
// TOTP code is tried first). A matched recovery code is consumed even
// though the enrollment is about to be deleted entirely, so the audit
// trail (used_at) reflects that it was the credential that authorized the
// disenroll.
//
// Returns domain.ErrMFAVerificationRequired when neither is supplied,
// domain.ErrMFANotEnrolled when the member has no confirmed enrollment,
// domain.ErrInvalidTOTPCode / ErrRecoveryCodeInvalid when the supplied
// credential does not verify.
func (s *MFAService) Disenroll(ctx context.Context, memberID domain.MemberID, householdID domain.HouseholdID, totpCode, recoveryCode string) error {
	if err := s.verifyTOTPOrRecovery(ctx, memberID, totpCode, recoveryCode); err != nil {
		return err
	}
	if err := s.repo.DeleteEnrollment(ctx, householdID, memberID); err != nil {
		return fmt.Errorf("mfa: disenroll: %w", err)
	}
	s.logger.InfoContext(ctx, "mfa disenrolled", "member_id", memberID.String())
	return nil
}

// VerifyLoginCode verifies memberID's pre-auth login MFA step: EITHER a
// current TOTP code OR an unused recovery code (consumed on match) — the
// TOTP code is tried first when both are supplied, falling back to the
// recovery code only if the TOTP code was WRONG (not merely absent).
// Unlike Disenroll, a matched TOTP code's step is durably recorded
// (MFARepository.RecordLoginStep) so the SAME code can never be replayed
// for a second login — even from a different device or session, which a
// per-session guard could not prevent.
//
// Returns domain.ErrMFAVerificationRequired when neither is supplied,
// domain.ErrMFANotEnrolled when no confirmed enrollment exists, and
// domain.ErrInvalidTOTPCode / ErrRecoveryCodeInvalid when the supplied
// credential does not verify — a replayed TOTP code is reported
// identically to a never-valid one (no oracle distinguishing "used
// before" from "wrong code").
func (s *MFAService) VerifyLoginCode(ctx context.Context, memberID domain.MemberID, totpCode, recoveryCode string) error {
	totpCode = strings.TrimSpace(totpCode)
	recoveryCode = strings.TrimSpace(recoveryCode)
	if totpCode == "" && recoveryCode == "" {
		return domain.ErrMFAVerificationRequired
	}

	enrollment, err := s.requireConfirmedEnrollment(ctx, memberID)
	if err != nil {
		return err
	}

	if totpCode != "" {
		err := s.verifyLoginTOTP(ctx, memberID, enrollment, totpCode)
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, domain.ErrInvalidTOTPCode) || recoveryCode == "":
			return err
		}
		// Fell through: the TOTP code was wrong (not a hard error) AND
		// a recovery code was ALSO supplied — try it next.
	}

	codeID, ok, err := s.matchRecoveryCode(ctx, memberID, recoveryCode)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrRecoveryCodeInvalid
	}
	if err := s.repo.MarkRecoveryCodeUsed(ctx, codeID); err != nil {
		return fmt.Errorf("mfa: mark recovery code used: %w", err)
	}
	s.logger.InfoContext(ctx, "mfa login verified via recovery code", "member_id", memberID.String())
	return nil
}

// requireConfirmedEnrollment fetches memberID's enrollment and returns
// domain.ErrMFANotEnrolled when there is none or it is still unconfirmed
// (an unconfirmed enrollment must never satisfy an action that requires
// active MFA).
func (s *MFAService) requireConfirmedEnrollment(ctx context.Context, memberID domain.MemberID) (*domain.MFAEnrollment, error) {
	enrollment, err := s.repo.GetEnrollment(ctx, memberID)
	if err != nil {
		return nil, err
	}
	if !enrollment.Confirmed() {
		return nil, domain.ErrMFANotEnrolled
	}
	return enrollment, nil
}

// verifyTOTPOrRecovery checks totpCode first (if non-blank), falling
// back to recoveryCode (if non-blank); a matched recovery code is marked
// used. See Disenroll's doc for the precedence and error contract.
func (s *MFAService) verifyTOTPOrRecovery(ctx context.Context, memberID domain.MemberID, totpCode, recoveryCode string) error {
	totpCode = strings.TrimSpace(totpCode)
	recoveryCode = strings.TrimSpace(recoveryCode)
	if totpCode == "" && recoveryCode == "" {
		return domain.ErrMFAVerificationRequired
	}

	enrollment, err := s.requireConfirmedEnrollment(ctx, memberID)
	if err != nil {
		return err
	}

	if totpCode != "" {
		secret, err := s.cipher.Decrypt(enrollment.TOTPSecretEnc)
		if err != nil {
			return fmt.Errorf("mfa: decrypt secret: %w", err)
		}
		if s.totp.Validate(totpCode, string(secret)) {
			return nil
		}
		if recoveryCode == "" {
			return domain.ErrInvalidTOTPCode
		}
	}

	codeID, ok, err := s.matchRecoveryCode(ctx, memberID, recoveryCode)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrRecoveryCodeInvalid
	}
	if err := s.repo.MarkRecoveryCodeUsed(ctx, codeID); err != nil {
		return fmt.Errorf("mfa: mark recovery code used: %w", err)
	}
	return nil
}

// verifyLoginTOTP decrypts enrollment's secret and checks code against
// it WITH replay protection: totpProvider.MatchStep reports WHICH RFC
// 6238 step (if any) matched, and that step must be strictly greater
// than the member's last-accepted login step (enrollment.LastTOTPStep)
// or the code is rejected as though it never matched at all — closing
// the replay window a plain Validate bool result cannot (it cannot
// distinguish "valid code, never used" from "valid code, already used
// this login"). A successful match is persisted via RecordLoginStep,
// whose own atomic guard closes the remaining race between two
// concurrent login attempts (see that method's doc).
func (s *MFAService) verifyLoginTOTP(ctx context.Context, memberID domain.MemberID, enrollment *domain.MFAEnrollment, code string) error {
	secret, err := s.cipher.Decrypt(enrollment.TOTPSecretEnc)
	if err != nil {
		return fmt.Errorf("mfa: decrypt secret: %w", err)
	}
	step, matched := s.totp.MatchStep(code, string(secret))
	if !matched {
		return domain.ErrInvalidTOTPCode
	}
	if enrollment.LastTOTPStep != nil && step <= *enrollment.LastTOTPStep {
		return domain.ErrInvalidTOTPCode
	}
	if err := s.repo.RecordLoginStep(ctx, memberID, step); err != nil {
		return fmt.Errorf("mfa: record login step: %w", err)
	}
	s.logger.InfoContext(ctx, "mfa login verified via totp", "member_id", memberID.String())
	return nil
}

// matchRecoveryCode normalizes raw and compares it (via the injected
// hasher, same argon2id KDF as member passwords) against every unused
// recovery code on file for memberID, returning the matched code's id.
// The unused set is bounded by recoveryCodeCount (ten), so a linear scan
// of argon2id verifications is an acceptable, bounded cost per attempt.
func (s *MFAService) matchRecoveryCode(ctx context.Context, memberID domain.MemberID, raw string) (domain.RecoveryCodeID, bool, error) {
	normalized := domain.NormalizeRecoveryCode(raw)
	if normalized == "" {
		return domain.RecoveryCodeID{}, false, nil
	}
	codes, err := s.repo.ListUnusedRecoveryCodes(ctx, memberID)
	if err != nil {
		return domain.RecoveryCodeID{}, false, fmt.Errorf("mfa: list recovery codes: %w", err)
	}
	for _, c := range codes {
		ok, err := s.hasher.Verify(normalized, c.CodeHash)
		if err != nil {
			// A malformed stored hash should never happen (every hash
			// this service writes comes from the same hasher); skip
			// defensively rather than failing the whole lookup for one
			// bad row.
			s.logger.ErrorContext(ctx, "mfa: malformed recovery code hash", "recovery_code_id", c.ID.String())
			continue
		}
		if ok {
			return c.ID, true, nil
		}
	}
	return domain.RecoveryCodeID{}, false, nil
}

// generateRecoveryCodes generates recoveryCodeCount fresh raw codes and
// their argon2id hashes (via the injected hasher), performing no
// repository writes — the caller decides how to persist hashes. codes
// are for one-time display; only hashes are ever persisted.
func (s *MFAService) generateRecoveryCodes() (codes, hashes []string, err error) {
	codes = make([]string, 0, recoveryCodeCount)
	hashes = make([]string, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		raw, err := domain.GenerateRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		hash, err := s.hasher.Hash(domain.NormalizeRecoveryCode(raw))
		if err != nil {
			return nil, nil, fmt.Errorf("mfa: hash recovery code: %w", err)
		}
		codes = append(codes, raw)
		hashes = append(hashes, hash)
	}
	return codes, hashes, nil
}
