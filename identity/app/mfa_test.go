package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/crypto/cryptotest"
	"github.com/ericfisherdev/nestcore/identity/app"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeMFARepo is an in-memory domain.MFARepository. mu guards every map
// access so tests can exercise it from concurrent goroutines (e.g. a
// concurrent replay of RecordLoginStep's atomic guard) without racing
// the fake itself — a concern distinct from the guard's own atomicity,
// which this fake still only approximates with a coarse lock.
type fakeMFARepo struct {
	mu          sync.Mutex
	enrollments map[domain.MemberID]*domain.MFAEnrollment
	codes       map[domain.MemberID][]domain.RecoveryCode
}

func newFakeMFARepo() *fakeMFARepo {
	return &fakeMFARepo{
		enrollments: make(map[domain.MemberID]*domain.MFAEnrollment),
		codes:       make(map[domain.MemberID][]domain.RecoveryCode),
	}
}

func (f *fakeMFARepo) GetEnrollment(_ context.Context, memberID domain.MemberID) (*domain.MFAEnrollment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.enrollments[memberID]
	if !ok {
		return nil, domain.ErrMFANotEnrolled
	}
	cp := *e
	return &cp, nil
}

func (f *fakeMFARepo) BeginEnrollment(_ context.Context, memberID domain.MemberID, householdID domain.HouseholdID, secretEnc []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.enrollments[memberID]; ok {
		if existing.HouseholdID != householdID {
			return domain.ErrMemberNotFound
		}
		if existing.Confirmed() {
			return domain.ErrMFAAlreadyEnrolled
		}
	}
	f.enrollments[memberID] = &domain.MFAEnrollment{MemberID: memberID, HouseholdID: householdID, TOTPSecretEnc: secretEnc}
	return nil
}

func (f *fakeMFARepo) ConfirmEnrollmentWithCodes(_ context.Context, memberID domain.MemberID, hashes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.enrollments[memberID]
	if !ok {
		return domain.ErrMFANotEnrolled
	}
	if e.Confirmed() {
		return domain.ErrMFAAlreadyEnrolled
	}
	now := time.Now()
	e.ConfirmedAt = &now

	codes := make([]domain.RecoveryCode, 0, len(hashes))
	for _, h := range hashes {
		codes = append(codes, domain.RecoveryCode{ID: domain.NewRecoveryCodeID(), MemberID: memberID, CodeHash: h})
	}
	f.codes[memberID] = codes
	return nil
}

func (f *fakeMFARepo) DeleteEnrollment(_ context.Context, householdID domain.HouseholdID, memberID domain.MemberID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.enrollments[memberID]
	if !ok || e.HouseholdID != householdID {
		return domain.ErrMFANotEnrolled
	}
	delete(f.enrollments, memberID)
	delete(f.codes, memberID)
	return nil
}

func (f *fakeMFARepo) ListUnusedRecoveryCodes(_ context.Context, memberID domain.MemberID) ([]domain.RecoveryCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.RecoveryCode
	for _, c := range f.codes[memberID] {
		if !c.Used() {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeMFARepo) MarkRecoveryCodeUsed(_ context.Context, codeID domain.RecoveryCodeID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for memberID, codes := range f.codes {
		for i := range codes {
			if codes[i].ID == codeID {
				now := time.Now()
				codes[i].UsedAt = &now
				f.codes[memberID] = codes
				return nil
			}
		}
	}
	return errors.New("recovery code not found")
}

func (f *fakeMFARepo) RecordLoginStep(_ context.Context, memberID domain.MemberID, step int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.enrollments[memberID]
	if !ok {
		return domain.ErrInvalidTOTPCode
	}
	if e.LastTOTPStep != nil && step <= *e.LastTOTPStep {
		return domain.ErrInvalidTOTPCode
	}
	e.LastTOTPStep = &step
	return nil
}

var _ domain.MFARepository = (*fakeMFARepo)(nil)

// fakeTOTPProvider is a controllable totpProvider fake: GenerateSecret
// always returns a fixed secret/URL pair (recording the issuer/
// accountName it was called with), and Validate reports true only for
// the configured validCode against the configured expectedSecret.
// MatchStep is separately controllable via loginCode/loginStep.
type fakeTOTPProvider struct {
	secret      string
	otpauthURL  string
	validCode   string
	lastIssuer  string
	lastAccount string

	// loginCode/loginStep configure MatchStep: it reports (loginStep,
	// true) when code == loginCode and secret == the fixture's secret;
	// otherwise (0, false).
	loginCode string
	loginStep int64
}

func (f *fakeTOTPProvider) GenerateSecret(issuer, accountName string) (string, string, error) {
	f.lastIssuer = issuer
	f.lastAccount = accountName
	return f.secret, f.otpauthURL, nil
}

func (f *fakeTOTPProvider) Validate(code, secret string) bool {
	return code == f.validCode && secret == f.secret
}

func (f *fakeTOTPProvider) MatchStep(code, secret string) (int64, bool) {
	if code == f.loginCode && secret == f.secret {
		return f.loginStep, true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// captureLogger returns a logger that writes to the returned buffer, so
// tests can assert on what was and was not logged.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

// mfaFixture bundles an MFAService with its fully controllable fakes, so
// tests can both exercise the service and assert directly against its
// dependencies' state.
type mfaFixture struct {
	svc  *app.MFAService
	repo *fakeMFARepo
	totp *fakeTOTPProvider
	logs *bytes.Buffer
}

func newMFAFixture(t *testing.T) *mfaFixture {
	t.Helper()
	repo := newFakeMFARepo()
	totpFake := &fakeTOTPProvider{secret: "JBSWY3DPEHPK3PXP", otpauthURL: "otpauth://totp/Nestcore:alice?secret=JBSWY3DPEHPK3PXP&issuer=Nestcore", validCode: "123456"}
	logger, buf := captureLogger()
	svc, err := app.NewMFAService(repo, testCipher(t), totpFake, cryptotest.Hasher(), logger)
	if err != nil {
		t.Fatalf("NewMFAService: %v", err)
	}
	return &mfaFixture{svc: svc, repo: repo, totp: totpFake, logs: buf}
}

// confirmEnrollment drives BeginEnrollment + ConfirmEnrollment to
// completion for memberID/householdID using the fixture's fixed valid
// code, returning the ten raw recovery codes.
func confirmEnrollment(t *testing.T, svc *app.MFAService, memberID domain.MemberID, householdID domain.HouseholdID) []string {
	t.Helper()
	if _, _, err := svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Member"); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	codes, err := svc.ConfirmEnrollment(context.Background(), memberID, "123456")
	if err != nil {
		t.Fatalf("ConfirmEnrollment: %v", err)
	}
	return codes
}

// ---------------------------------------------------------------------------
// Enrollment lifecycle
// ---------------------------------------------------------------------------

func TestBeginEnrollment_GeneratesAndPersistsUnconfirmedSecret(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()

	secret, otpauthURL, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice")
	if err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if secret != f.totp.secret || otpauthURL != f.totp.otpauthURL {
		t.Errorf("BeginEnrollment returned (%q, %q), want the generated (%q, %q)", secret, otpauthURL, f.totp.secret, f.totp.otpauthURL)
	}
	if f.totp.lastAccount != "Alice" || f.totp.lastIssuer != "Nestcore" {
		t.Errorf("GenerateSecret called with issuer=%q accountName=%q, want issuer=Nestcore accountName=Alice", f.totp.lastIssuer, f.totp.lastAccount)
	}

	enrollment, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment after BeginEnrollment: %v", err)
	}
	if enrollment.Confirmed() {
		t.Error("a fresh enrollment must not be confirmed")
	}
	if string(enrollment.TOTPSecretEnc) == secret {
		t.Error("the stored secret must be encrypted, not the raw secret")
	}

	status, err := f.svc.Status(context.Background(), memberID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Confirmed() {
		t.Error("Status must report an unconfirmed enrollment as not confirmed")
	}
}

func TestBeginEnrollment_ReplacesUnconfirmedEnrollment(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()

	if _, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice"); err != nil {
		t.Fatalf("first BeginEnrollment: %v", err)
	}
	first, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment after first BeginEnrollment: %v", err)
	}
	firstEnc := append([]byte(nil), first.TOTPSecretEnc...)

	f.totp.secret = "ANOTHERSECRETVALUE"
	secret, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice")
	if err != nil {
		t.Fatalf("second BeginEnrollment (re-enroll over unconfirmed): %v", err)
	}
	if secret != "ANOTHERSECRETVALUE" {
		t.Errorf("second BeginEnrollment returned secret %q, want the newly generated one", secret)
	}

	enrollment, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if bytes.Equal(enrollment.TOTPSecretEnc, firstEnc) {
		t.Error("the second BeginEnrollment must replace the stored secret, not keep the first")
	}
	if enrollment.Confirmed() {
		t.Error("a replaced enrollment must still be unconfirmed")
	}
}

func TestBeginEnrollment_AlreadyConfirmed_ReturnsErrMFAAlreadyEnrolled(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	if _, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice"); !errors.Is(err, domain.ErrMFAAlreadyEnrolled) {
		t.Errorf("BeginEnrollment over a confirmed enrollment: err = %v, want ErrMFAAlreadyEnrolled", err)
	}
}

func TestBeginEnrollment_CrossHouseholdRejected(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	victimHousehold := domain.NewHouseholdID()
	attackerHousehold := domain.NewHouseholdID()

	if _, _, err := f.svc.BeginEnrollment(context.Background(), memberID, victimHousehold, "Nestcore", "Alice"); err != nil {
		t.Fatalf("seed BeginEnrollment: %v", err)
	}
	_, _, err := f.svc.BeginEnrollment(context.Background(), memberID, attackerHousehold, "Nestcore", "Attacker")
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("cross-household BeginEnrollment: err = %v, want ErrMemberNotFound", err)
	}

	enrollment, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.HouseholdID != victimHousehold {
		t.Error("a cross-household BeginEnrollment attempt must not change the victim's household_id")
	}
}

func TestConfirmEnrollment_WrongCodeRejected_EnrollmentStaysUnconfirmed(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	if _, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice"); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}

	_, err := f.svc.ConfirmEnrollment(context.Background(), memberID, "000000")
	if !errors.Is(err, domain.ErrInvalidTOTPCode) {
		t.Fatalf("ConfirmEnrollment(wrong code): err = %v, want ErrInvalidTOTPCode", err)
	}

	enrollment, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.Confirmed() {
		t.Error("a wrong code must not confirm the enrollment")
	}
}

func TestConfirmEnrollment_NotEnrolled(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	_, err := f.svc.ConfirmEnrollment(context.Background(), domain.NewMemberID(), "123456")
	if !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("ConfirmEnrollment with no enrollment: err = %v, want ErrMFANotEnrolled", err)
	}
}

func TestConfirmEnrollment_ValidCode_ActivatesAndReturnsTenRecoveryCodes(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	if _, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice"); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}

	codes, err := f.svc.ConfirmEnrollment(context.Background(), memberID, "123456")
	if err != nil {
		t.Fatalf("ConfirmEnrollment: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("ConfirmEnrollment returned %d recovery codes, want 10", len(codes))
	}
	seen := make(map[string]bool, len(codes))
	for _, c := range codes {
		if seen[c] {
			t.Errorf("duplicate recovery code returned: %q", c)
		}
		seen[c] = true
	}

	enrollment, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if !enrollment.Confirmed() {
		t.Error("a valid code must confirm the enrollment")
	}

	unused, err := f.repo.ListUnusedRecoveryCodes(context.Background(), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	if len(unused) != 10 {
		t.Fatalf("stored %d unused recovery codes, want 10", len(unused))
	}
	for _, c := range unused {
		if seen[c.CodeHash] {
			t.Errorf("a raw recovery code was stored instead of its hash: %q", c.CodeHash)
		}
		if !strings.HasPrefix(c.CodeHash, "$argon2id$") {
			t.Errorf("recovery code hash %q does not look like an argon2id PHC string", c.CodeHash)
		}
	}
}

func TestConfirmEnrollment_AlreadyConfirmed(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	if _, err := f.svc.ConfirmEnrollment(context.Background(), memberID, "123456"); !errors.Is(err, domain.ErrMFAAlreadyEnrolled) {
		t.Errorf("re-confirming an already-confirmed enrollment: err = %v, want ErrMFAAlreadyEnrolled", err)
	}
}

func TestBeginEnrollment_SecretNeverLogged(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()

	secret, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice")
	if err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if strings.Contains(f.logs.String(), secret) {
		t.Errorf("BeginEnrollment logged the raw secret: %s", f.logs.String())
	}

	if _, err := f.svc.ConfirmEnrollment(context.Background(), memberID, "123456"); err != nil {
		t.Fatalf("ConfirmEnrollment: %v", err)
	}
	if strings.Contains(f.logs.String(), secret) {
		t.Errorf("ConfirmEnrollment logged the raw secret: %s", f.logs.String())
	}
}

// ---------------------------------------------------------------------------
// Recovery codes: shown once, work once
// ---------------------------------------------------------------------------

func TestDisenroll_RecoveryCode_WorksOnceThenRejected(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	codes := confirmEnrollment(t, f.svc, memberID, householdID)

	if err := f.svc.Disenroll(context.Background(), memberID, householdID, "", codes[3]); err != nil {
		t.Fatalf("Disenroll with a fresh recovery code: %v", err)
	}

	// The successful Disenroll removed the whole enrollment; re-enroll
	// so there is an active enrollment again, then confirm the OLD code
	// (from the now-deleted enrollment) cannot be reused against it.
	confirmEnrollment(t, f.svc, memberID, householdID)
	err := f.svc.Disenroll(context.Background(), memberID, householdID, "", codes[3])
	if !errors.Is(err, domain.ErrRecoveryCodeInvalid) {
		t.Errorf("reusing a code from a deleted enrollment: err = %v, want ErrRecoveryCodeInvalid", err)
	}
}

func TestMarkRecoveryCodeUsed_ExcludesCodeFromFutureVerification(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	codes := confirmEnrollment(t, f.svc, memberID, householdID)

	unused, err := f.repo.ListUnusedRecoveryCodes(context.Background(), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	target := findRecoveryCodeID(t, unused, codes[7])

	if err := f.repo.MarkRecoveryCodeUsed(context.Background(), target); err != nil {
		t.Fatalf("MarkRecoveryCodeUsed: %v", err)
	}

	stillUnused, err := f.repo.ListUnusedRecoveryCodes(context.Background(), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes after mark-used: %v", err)
	}
	if len(stillUnused) != 9 {
		t.Fatalf("unused recovery codes after marking one used = %d, want 9", len(stillUnused))
	}
	for _, c := range stillUnused {
		if c.ID == target {
			t.Error("a used recovery code must not appear in ListUnusedRecoveryCodes")
		}
	}

	err = f.svc.Disenroll(context.Background(), memberID, householdID, "", codes[7])
	if !errors.Is(err, domain.ErrRecoveryCodeInvalid) {
		t.Errorf("Disenroll with an already-used recovery code: err = %v, want ErrRecoveryCodeInvalid", err)
	}
}

func findRecoveryCodeID(t *testing.T, candidates []domain.RecoveryCode, rawCode string) domain.RecoveryCodeID {
	t.Helper()
	normalized := domain.NormalizeRecoveryCode(rawCode)
	for _, c := range candidates {
		ok, err := cryptotest.Hasher().Verify(normalized, c.CodeHash)
		if err == nil && ok {
			return c.ID
		}
	}
	t.Fatalf("no recovery code row matched %q", rawCode)
	return domain.RecoveryCodeID{}
}

func TestDisenroll_InvalidRecoveryCodeRejected(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	err := f.svc.Disenroll(context.Background(), memberID, householdID, "", "NOT-A-REAL-CODE")
	if !errors.Is(err, domain.ErrRecoveryCodeInvalid) {
		t.Errorf("Disenroll(bogus recovery code): err = %v, want ErrRecoveryCodeInvalid", err)
	}
}

func TestDisenroll_NoCredentialSupplied(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	err := f.svc.Disenroll(context.Background(), memberID, householdID, "", "")
	if !errors.Is(err, domain.ErrMFAVerificationRequired) {
		t.Errorf("Disenroll with neither code nor recovery code: err = %v, want ErrMFAVerificationRequired", err)
	}
}

func TestDisenroll_UnconfirmedEnrollment_NotEnrolled(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	if _, _, err := f.svc.BeginEnrollment(context.Background(), memberID, householdID, "Nestcore", "Alice"); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}

	err := f.svc.Disenroll(context.Background(), memberID, householdID, "123456", "")
	if !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("Disenroll against an unconfirmed enrollment: err = %v, want ErrMFANotEnrolled", err)
	}
}

// TestDisenroll_ValidTOTPCode_RemovesEnrollment covers the most common
// disenroll path — a member submitting a valid TOTP code against a
// confirmed enrollment — which every OTHER Disenroll test in this file
// bypasses (they use a recovery code, an invalid one, no credential, or
// an unconfirmed enrollment).
func TestDisenroll_ValidTOTPCode_RemovesEnrollment(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	if err := f.svc.Disenroll(context.Background(), memberID, householdID, "123456", ""); err != nil {
		t.Fatalf("Disenroll with a valid TOTP code: %v", err)
	}

	if _, err := f.repo.GetEnrollment(context.Background(), memberID); !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("GetEnrollment after Disenroll: err = %v, want ErrMFANotEnrolled", err)
	}
	unused, err := f.repo.ListUnusedRecoveryCodes(context.Background(), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	if len(unused) != 0 {
		t.Errorf("recovery codes remaining after Disenroll = %d, want 0", len(unused))
	}
}

// ---------------------------------------------------------------------------
// VerifyLoginCode — login-time TOTP/recovery verification with a durable
// replay guard (AC1).
// ---------------------------------------------------------------------------

func TestVerifyLoginCode_ValidTOTP_RecordsStep(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)
	f.totp.loginCode = "654321"
	f.totp.loginStep = 42

	if err := f.svc.VerifyLoginCode(context.Background(), memberID, "654321", ""); err != nil {
		t.Fatalf("VerifyLoginCode: %v", err)
	}

	enrollment, err := f.repo.GetEnrollment(context.Background(), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.LastTOTPStep == nil || *enrollment.LastTOTPStep != 42 {
		t.Errorf("LastTOTPStep = %v, want 42", enrollment.LastTOTPStep)
	}
}

func TestVerifyLoginCode_WrongTOTPRejected(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)
	f.totp.loginCode = "654321"
	f.totp.loginStep = 42

	err := f.svc.VerifyLoginCode(context.Background(), memberID, "000000", "")
	if !errors.Is(err, domain.ErrInvalidTOTPCode) {
		t.Errorf("VerifyLoginCode(wrong code): err = %v, want ErrInvalidTOTPCode", err)
	}
}

// TestVerifyLoginCode_ReplayedStepRejected is the direct AC1 coverage:
// the SAME code (and therefore the SAME step) accepted once must be
// rejected on a second submission, even though totpProvider.MatchStep
// would report a match again — the replay guard is enforced by
// VerifyLoginCode comparing against LastTOTPStep, not by the TOTP math
// itself.
func TestVerifyLoginCode_ReplayedStepRejected(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)
	f.totp.loginCode = "654321"
	f.totp.loginStep = 42

	if err := f.svc.VerifyLoginCode(context.Background(), memberID, "654321", ""); err != nil {
		t.Fatalf("first VerifyLoginCode: %v", err)
	}
	err := f.svc.VerifyLoginCode(context.Background(), memberID, "654321", "")
	if !errors.Is(err, domain.ErrInvalidTOTPCode) {
		t.Errorf("replayed VerifyLoginCode: err = %v, want ErrInvalidTOTPCode", err)
	}
}

func TestVerifyLoginCode_LowerStepRejectedAfterHigherAccepted(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	f.totp.loginCode = "222222"
	f.totp.loginStep = 100
	if err := f.svc.VerifyLoginCode(context.Background(), memberID, "222222", ""); err != nil {
		t.Fatalf("VerifyLoginCode at step 100: %v", err)
	}

	f.totp.loginCode = "111111"
	f.totp.loginStep = 99
	err := f.svc.VerifyLoginCode(context.Background(), memberID, "111111", "")
	if !errors.Is(err, domain.ErrInvalidTOTPCode) {
		t.Errorf("VerifyLoginCode at an earlier step than already accepted: err = %v, want ErrInvalidTOTPCode", err)
	}

	// The guard must also still ADMIT a fresh, strictly-later step — a
	// regression that rejected every step after the first would
	// permanently lock a member out while this suite stayed green if
	// this case were never exercised.
	f.totp.loginCode = "333333"
	f.totp.loginStep = 101
	if err := f.svc.VerifyLoginCode(context.Background(), memberID, "333333", ""); err != nil {
		t.Fatalf("VerifyLoginCode at a later step than already accepted: %v", err)
	}
}

// TestVerifyLoginCode_ConcurrentReplay_ExactlyOneWins exercises
// RecordLoginStep's atomic guard concurrently — the last line of defense
// against two logins racing the SAME captured code (e.g. a replayed
// request landing on two backend instances simultaneously): both
// goroutines read the enrollment's LastTOTPStep as nil (their own
// GetEnrollment snapshot, taken before either write), so the
// service-level check at verifyLoginTOTP alone cannot reject either one
// — only the repository's serialized, conditional write can, and
// verifyLoginTOTP must map the loser's rejection back to
// domain.ErrInvalidTOTPCode (not a wrapped internal error) per the
// no-oracle contract fixed alongside this test.
func TestVerifyLoginCode_ConcurrentReplay_ExactlyOneWins(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)
	f.totp.loginCode = "654321"
	f.totp.loginStep = 42

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := range errs {
		go func() {
			defer wg.Done()
			errs[i] = f.svc.VerifyLoginCode(context.Background(), memberID, "654321", "")
		}()
	}
	wg.Wait()

	successes := 0
	for i, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrInvalidTOTPCode):
			// Expected outcome for the loser.
		default:
			t.Errorf("racing VerifyLoginCode call %d: unexpected error %v (want nil or ErrInvalidTOTPCode, never a wrapped internal error)", i, err)
		}
	}
	if successes != 1 {
		t.Fatalf("racing VerifyLoginCode with the identical replayed code: %d succeeded, want exactly 1", successes)
	}
}

func TestVerifyLoginCode_RecoveryCode_ConsumesIt(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	codes := confirmEnrollment(t, f.svc, memberID, householdID)

	if err := f.svc.VerifyLoginCode(context.Background(), memberID, "", codes[2]); err != nil {
		t.Fatalf("VerifyLoginCode with a recovery code: %v", err)
	}

	err := f.svc.VerifyLoginCode(context.Background(), memberID, "", codes[2])
	if !errors.Is(err, domain.ErrRecoveryCodeInvalid) {
		t.Errorf("reusing a login recovery code: err = %v, want ErrRecoveryCodeInvalid", err)
	}
}

func TestVerifyLoginCode_WrongTOTPFallsBackToRecoveryCode(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	codes := confirmEnrollment(t, f.svc, memberID, householdID)

	if err := f.svc.VerifyLoginCode(context.Background(), memberID, "000000", codes[5]); err != nil {
		t.Fatalf("VerifyLoginCode(wrong totp + valid recovery): %v", err)
	}
}

func TestVerifyLoginCode_HardTOTPErrorPropagates(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)
	corruptStoredSecret(t, f.repo, memberID)

	err := f.svc.VerifyLoginCode(context.Background(), memberID, "654321", "")
	if err == nil {
		t.Fatal("VerifyLoginCode must propagate a hard decrypt error, not silently succeed")
	}
	if errors.Is(err, domain.ErrInvalidTOTPCode) || errors.Is(err, domain.ErrRecoveryCodeInvalid) || errors.Is(err, domain.ErrMFAVerificationRequired) {
		t.Errorf("VerifyLoginCode masked a hard decrypt failure as a well-known sentinel: %v", err)
	}
	if !errors.Is(err, crypto.ErrMalformedCiphertext) {
		t.Errorf("VerifyLoginCode error = %v, want it to wrap crypto.ErrMalformedCiphertext", err)
	}
}

func TestVerifyLoginCode_HardTOTPErrorDoesNotFallBackToRecoveryCode(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	codes := confirmEnrollment(t, f.svc, memberID, householdID)
	corruptStoredSecret(t, f.repo, memberID)

	err := f.svc.VerifyLoginCode(context.Background(), memberID, "654321", codes[0])
	if err == nil {
		t.Fatal("VerifyLoginCode must propagate the hard decrypt error, not silently succeed via the recovery code")
	}
	if errors.Is(err, domain.ErrInvalidTOTPCode) || errors.Is(err, domain.ErrRecoveryCodeInvalid) {
		t.Errorf("a hard (non-ErrInvalidTOTPCode) TOTP error must be returned as-is, not masked as a wrong-code or recovery-code sentinel: %v", err)
	}
	if !errors.Is(err, crypto.ErrMalformedCiphertext) {
		t.Errorf("VerifyLoginCode error = %v, want it to wrap crypto.ErrMalformedCiphertext", err)
	}

	stillUnused, err := f.repo.ListUnusedRecoveryCodes(context.Background(), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	if len(stillUnused) != 10 {
		t.Errorf("unused recovery codes after a hard TOTP error = %d, want still 10 (the recovery code must not have been attempted)", len(stillUnused))
	}
}

func TestVerifyLoginCode_NoCredentialSupplied(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	confirmEnrollment(t, f.svc, memberID, householdID)

	err := f.svc.VerifyLoginCode(context.Background(), memberID, "", "")
	if !errors.Is(err, domain.ErrMFAVerificationRequired) {
		t.Errorf("VerifyLoginCode with neither code: err = %v, want ErrMFAVerificationRequired", err)
	}
}

func TestVerifyLoginCode_UnenrolledMemberRejected(t *testing.T) {
	t.Parallel()
	f := newMFAFixture(t)
	err := f.svc.VerifyLoginCode(context.Background(), domain.NewMemberID(), "123456", "")
	if !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("VerifyLoginCode for an unenrolled member: err = %v, want ErrMFANotEnrolled", err)
	}
}

// corruptStoredSecret flips a byte in memberID's stored TOTPSecretEnc
// directly in repo (bypassing the service entirely), so a SUBSEQUENT
// cipher.Decrypt against it fails with crypto.ErrMalformedCiphertext —
// the GCM authentication tag no longer matches.
func corruptStoredSecret(t *testing.T, repo *fakeMFARepo, memberID domain.MemberID) {
	t.Helper()
	e, ok := repo.enrollments[memberID]
	if !ok {
		t.Fatalf("corruptStoredSecret: no enrollment on file for %s", memberID)
	}
	corrupted := append([]byte(nil), e.TOTPSecretEnc...)
	corrupted[0] ^= 0xFF
	e.TOTPSecretEnc = corrupted
}

// ---------------------------------------------------------------------------
// AC: a totp_secret_enc value sealed with Nestova's current AES-GCM
// cipher parameters decrypts and verifies through the new MFAService.
// ---------------------------------------------------------------------------

// TestConfirmEnrollment_DecryptsFixtureSealedWithMatchingAESGCMParameters
// proves format compatibility, not merely that nestcore's own Cipher can
// read back its own output: sealedFixture below was produced by a
// standalone AES-256-GCM Seal call (crypto/aes + crypto/cipher directly,
// NOT this package's Cipher.Encrypt), using the same key/nonce-prepended
// layout Nestova's identical internal/platform/crypto.Cipher.Encrypt
// implementation produces (nonce || ciphertext+tag, 12-byte GCM nonce,
// no AAD). A pre-NSTR-130 enrollment migrating as-is must decrypt and
// verify unchanged through the new MFAService.
func TestConfirmEnrollment_DecryptsFixtureSealedWithMatchingAESGCMParameters(t *testing.T) {
	t.Parallel()

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	// sealedFixture = AES-256-GCM-Seal(key, nonce=[100..111], "JBSWY3DPEHPK3PXP"),
	// computed independently of this package via the Go standard library.
	sealedFixture := []byte{
		0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f,
		0x2, 0x59, 0x8d, 0x31, 0x20, 0xda, 0x12, 0xce, 0x7b, 0x2a, 0xf, 0xa3,
		0xe9, 0x35, 0x32, 0xad, 0x3a, 0x6e, 0xc5, 0xb1, 0xea, 0xbe, 0x83, 0x83,
		0xd2, 0x30, 0xc5, 0xa, 0xa6, 0x42, 0x68, 0x89,
	}
	const fixtureSecret = "JBSWY3DPEHPK3PXP"

	cipher, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	repo := newFakeMFARepo()
	memberID := domain.NewMemberID()
	householdID := domain.NewHouseholdID()
	repo.enrollments[memberID] = &domain.MFAEnrollment{
		MemberID:      memberID,
		HouseholdID:   householdID,
		TOTPSecretEnc: sealedFixture,
	}
	totpFake := &fakeTOTPProvider{secret: fixtureSecret, validCode: "123456"}
	logger, _ := captureLogger()
	svc, err := app.NewMFAService(repo, cipher, totpFake, cryptotest.Hasher(), logger)
	if err != nil {
		t.Fatalf("NewMFAService: %v", err)
	}

	codes, err := svc.ConfirmEnrollment(context.Background(), memberID, "123456")
	if err != nil {
		t.Fatalf("ConfirmEnrollment against a fixture-sealed secret: %v", err)
	}
	if len(codes) != 10 {
		t.Errorf("ConfirmEnrollment returned %d recovery codes, want 10", len(codes))
	}
}
