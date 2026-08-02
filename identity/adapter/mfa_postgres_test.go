package adapter_test

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/crypto/cryptotest"
	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/app"
	"github.com/ericfisherdev/nestcore/identity/domain"
	"github.com/ericfisherdev/nestcore/totp"
)

// newTestMFARepo returns an MFARepository (and the household/member id it
// seeds) backed by NESTCORE_TEST_DATABASE_URL, reusing newCredentialTestRepos'
// schema setup.
func newTestMFARepo(t *testing.T) (*adapter.MFARepository, domain.HouseholdID, domain.MemberID) {
	t.Helper()
	pool := newTestPool(t)
	households, members := adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool)
	m := seedMember(t, households, members, "MFA Household", "Alice")
	return adapter.NewMFARepository(pool), m.HouseholdID, m.ID
}

// mfaTestCipher returns a real AES-256-GCM cipher (the same construction
// production uses via crypto.NewCipher) keyed with a fixed 32-byte test
// key, for the gated tests that need actual encryption/decryption in the
// loop.
func mfaTestCipher(t *testing.T) *crypto.Cipher {
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

// discardMFALogger returns a logger that writes nowhere, for constructing
// an MFAService in tests that don't assert on log output.
func discardMFALogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMFABeginEnrollment_PersistsUnconfirmedAndEncrypted(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)

	secretEnc := []byte("not-a-real-plaintext-secret-ciphertext-bytes")
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, secretEnc); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.Confirmed() {
		t.Error("a fresh enrollment must not be confirmed")
	}
	if string(enrollment.TOTPSecretEnc) != string(secretEnc) {
		t.Error("stored ciphertext must round-trip exactly (the repository must not re-encode/mutate it)")
	}
	if enrollment.HouseholdID != householdID {
		t.Errorf("HouseholdID = %v, want %v", enrollment.HouseholdID, householdID)
	}
}

// TestMFAServiceBeginEnrollment_SecretNotStoredInPlaintext is the real
// gated check: it drives enrollment through app.MFAService with the SAME
// cipher construction production uses (crypto.NewCipher, AES-256-GCM),
// then reads the totp_secret_enc column directly (bypassing the
// repository's own Scan, which would just hand back the same opaque
// bytes) and asserts the raw secret MFAService generated is nowhere in
// those stored bytes, while still decrypting back to it exactly.
func TestMFAServiceBeginEnrollment_SecretNotStoredInPlaintext(t *testing.T) {
	pool, households, members, _ := newCredentialTestReposWithPool(t)
	m := seedMember(t, households, members, "Plaintext Guard Household", "Alice")

	cipher := mfaTestCipher(t)
	svc, err := app.NewMFAService(adapter.NewMFARepository(pool), cipher, totp.NewProvider(), cryptotest.Hasher(), discardMFALogger())
	if err != nil {
		t.Fatalf("NewMFAService: %v", err)
	}

	rawSecret, _, err := svc.BeginEnrollment(testCtx(t), m.ID, m.HouseholdID, "Nestcore", "Alice")
	if err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if rawSecret == "" {
		t.Fatal("BeginEnrollment returned an empty secret")
	}

	var storedEnc []byte
	err = pool.QueryRow(testCtx(t), `SELECT totp_secret_enc FROM identity.member_mfa WHERE member_id = $1`, m.ID.String()).Scan(&storedEnc)
	if err != nil {
		t.Fatalf("query stored totp_secret_enc: %v", err)
	}

	if bytes.Contains(storedEnc, []byte(rawSecret)) {
		t.Errorf("stored totp_secret_enc contains the raw secret as a substring — it is not actually encrypted: %x", storedEnc)
	}
	if string(storedEnc) == rawSecret {
		t.Error("stored totp_secret_enc equals the raw secret exactly")
	}

	decrypted, err := cipher.Decrypt(storedEnc)
	if err != nil {
		t.Fatalf("Decrypt stored ciphertext: %v", err)
	}
	if string(decrypted) != rawSecret {
		t.Errorf("decrypted secret = %q, want the original %q", decrypted, rawSecret)
	}
}

func TestMFABeginEnrollment_ReplacesUnconfirmedInPlace(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret-1")); err != nil {
		t.Fatalf("first BeginEnrollment: %v", err)
	}
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret-2")); err != nil {
		t.Fatalf("second BeginEnrollment (replace unconfirmed): %v", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if string(enrollment.TOTPSecretEnc) != "secret-2" {
		t.Errorf("TOTPSecretEnc = %q, want the replaced secret-2", enrollment.TOTPSecretEnc)
	}
}

func TestMFABeginEnrollment_AlreadyConfirmedRejected(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret-1")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}

	err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret-2"))
	if !errors.Is(err, domain.ErrMFAAlreadyEnrolled) {
		t.Errorf("BeginEnrollment over a confirmed enrollment: err = %v, want ErrMFAAlreadyEnrolled", err)
	}
	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if string(enrollment.TOTPSecretEnc) != "secret-1" {
		t.Error("a rejected BeginEnrollment must not overwrite the confirmed secret")
	}
}

func TestMFABeginEnrollment_UnknownMemberInHousehold(t *testing.T) {
	repo, householdID, _ := newTestMFARepo(t)
	err := repo.BeginEnrollment(testCtx(t), domain.NewMemberID(), householdID, []byte("secret"))
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("BeginEnrollment for an unknown member: err = %v, want ErrMemberNotFound", err)
	}
}

// TestMFABeginEnrollment_CrossHouseholdCannotTouchVictimRow is the gated
// tenant-isolation check: a BeginEnrollment call for an
// ALREADY-ENROLLED member, but supplying a DIFFERENT household id than
// the member's real one, must never overwrite that member's pending
// secret — it must be reported the same as an unknown member (no signal
// about which occurred), and the victim's row must survive completely
// untouched.
func TestMFABeginEnrollment_CrossHouseholdCannotTouchVictimRow(t *testing.T) {
	repo, victimHouseholdID, victimMemberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), victimMemberID, victimHouseholdID, []byte("victim-secret")); err != nil {
		t.Fatalf("seed victim BeginEnrollment: %v", err)
	}

	attackerHouseholdID := domain.NewHouseholdID()
	err := repo.BeginEnrollment(testCtx(t), victimMemberID, attackerHouseholdID, []byte("attacker-supplied-secret"))
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Fatalf("cross-household BeginEnrollment: err = %v, want ErrMemberNotFound", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), victimMemberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if string(enrollment.TOTPSecretEnc) != "victim-secret" {
		t.Error("a cross-household BeginEnrollment attempt must not overwrite the victim's pending secret")
	}
	if enrollment.HouseholdID != victimHouseholdID {
		t.Error("a cross-household BeginEnrollment attempt must not change the victim's household_id")
	}
}

func TestMFAGetEnrollment_NotEnrolled(t *testing.T) {
	repo, _, memberID := newTestMFARepo(t)
	_, err := repo.GetEnrollment(testCtx(t), memberID)
	if !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("GetEnrollment(never enrolled) error = %v, want ErrMFANotEnrolled", err)
	}
}

func TestMFAConfirmEnrollmentWithCodes_NotEnrolled(t *testing.T) {
	repo, _, memberID := newTestMFARepo(t)
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("ConfirmEnrollmentWithCodes(never enrolled) error = %v, want ErrMFANotEnrolled", err)
	}
}

// TestMFAConfirmEnrollmentWithCodes_StoresCodesAtomically directly
// exercises the atomic confirm+store contract's happy path: a single
// call both confirms the enrollment AND persists the given hashes.
func TestMFAConfirmEnrollmentWithCodes_StoresCodesAtomically(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}

	hashes := []string{"hash-1", "hash-2", "hash-3"}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, hashes); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if !enrollment.Confirmed() {
		t.Error("ConfirmEnrollmentWithCodes must confirm the enrollment")
	}

	codes, err := repo.ListUnusedRecoveryCodes(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	if len(codes) != len(hashes) {
		t.Fatalf("stored %d recovery codes, want %d", len(codes), len(hashes))
	}
	got := make(map[string]bool, len(codes))
	for _, c := range codes {
		got[c.CodeHash] = true
	}
	for _, h := range hashes {
		if !got[h] {
			t.Errorf("missing expected recovery code hash %q", h)
		}
	}
}

// TestMFAConfirmEnrollmentWithCodes_ConcurrentRace_ExactlyOneWins is the
// gated concurrency check: two callers racing to confirm the SAME
// still-unconfirmed enrollment with DIFFERENT code sets must resolve to
// exactly one winner — the loser gets ErrMFAAlreadyEnrolled, and its
// hashes are never persisted at all.
func TestMFAConfirmEnrollmentWithCodes_ConcurrentRace_ExactlyOneWins(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	ctx := testCtx(t)

	hashesA := []string{"race-hash-a1", "race-hash-a2"}
	hashesB := []string{"race-hash-b1", "race-hash-b2"}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = repo.ConfirmEnrollmentWithCodes(ctx, memberID, hashesA)
	}()
	go func() {
		defer wg.Done()
		errs[1] = repo.ConfirmEnrollmentWithCodes(ctx, memberID, hashesB)
	}()
	wg.Wait()

	successes := 0
	var winnerHashes []string
	for i, err := range errs {
		switch {
		case err == nil:
			successes++
			if i == 0 {
				winnerHashes = hashesA
			} else {
				winnerHashes = hashesB
			}
		case errors.Is(err, domain.ErrMFAAlreadyEnrolled):
			// Expected outcome for the loser.
		default:
			t.Errorf("unexpected error from racing ConfirmEnrollmentWithCodes: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("racing ConfirmEnrollmentWithCodes: %d succeeded, want exactly 1", successes)
	}

	codes, err := repo.ListUnusedRecoveryCodes(ctx, memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	if len(codes) != len(winnerHashes) {
		t.Fatalf("stored %d recovery codes, want %d (the winner's set)", len(codes), len(winnerHashes))
	}
	gotHashes := make(map[string]bool, len(codes))
	for _, c := range codes {
		gotHashes[c.CodeHash] = true
	}
	for _, h := range winnerHashes {
		if !gotHashes[h] {
			t.Errorf("missing winner's hash %q in stored recovery codes", h)
		}
	}
}

func TestMFADeleteEnrollment_CascadesRecoveryCodes(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, []string{"hash-a", "hash-b", "hash-c"}); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}

	if err := repo.DeleteEnrollment(testCtx(t), householdID, memberID); err != nil {
		t.Fatalf("DeleteEnrollment: %v", err)
	}

	if _, err := repo.GetEnrollment(testCtx(t), memberID); !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("GetEnrollment after delete: err = %v, want ErrMFANotEnrolled", err)
	}
	codes, err := repo.ListUnusedRecoveryCodes(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes after delete: %v", err)
	}
	if len(codes) != 0 {
		t.Errorf("recovery codes survived DeleteEnrollment: got %d, want 0 (should cascade)", len(codes))
	}
}

func TestMFADeleteEnrollment_WrongHouseholdRejected(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}

	err := repo.DeleteEnrollment(testCtx(t), domain.NewHouseholdID(), memberID)
	if !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("DeleteEnrollment with a mismatched household: err = %v, want ErrMFANotEnrolled", err)
	}
	if _, err := repo.GetEnrollment(testCtx(t), memberID); err != nil {
		t.Errorf("the enrollment must survive a mismatched-household delete attempt, got: %v", err)
	}
}

func TestMFADeleteEnrollment_NotEnrolled(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	err := repo.DeleteEnrollment(testCtx(t), householdID, memberID)
	if !errors.Is(err, domain.ErrMFANotEnrolled) {
		t.Errorf("DeleteEnrollment(never enrolled) error = %v, want ErrMFANotEnrolled", err)
	}
}

func TestMFAMarkRecoveryCodeUsed_ExcludesFromUnusedList(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, []string{"hash-1", "hash-2", "hash-3"}); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}
	codes, err := repo.ListUnusedRecoveryCodes(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	target := codes[0].ID

	if err := repo.MarkRecoveryCodeUsed(testCtx(t), target); err != nil {
		t.Fatalf("MarkRecoveryCodeUsed: %v", err)
	}

	remaining, err := repo.ListUnusedRecoveryCodes(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes after mark-used: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("unused recovery codes after marking one used = %d, want 2", len(remaining))
	}
	for _, c := range remaining {
		if c.ID == target {
			t.Error("a used recovery code must not appear in ListUnusedRecoveryCodes")
		}
	}

	// A used code cannot be marked used again by a second, independent
	// call (idempotency guard: the WHERE used_at IS NULL clause means a
	// repeat call affects zero rows and reports the failure rather than
	// silently succeeding).
	if err := repo.MarkRecoveryCodeUsed(testCtx(t), target); !errors.Is(err, domain.ErrRecoveryCodeInvalid) {
		t.Errorf("marking an already-used code used again: err = %v, want ErrRecoveryCodeInvalid", err)
	}
}

// ---------------------------------------------------------------------------
// RecordLoginStep: the durable login-MFA replay guard.
// ---------------------------------------------------------------------------

func TestMFAGetEnrollment_LastTOTPStepNilBeforeAnyLogin(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.LastTOTPStep != nil {
		t.Errorf("LastTOTPStep = %v, want nil before any login MFA verification", *enrollment.LastTOTPStep)
	}
}

func TestMFARecordLoginStep_PersistsAndIsReadableViaGetEnrollment(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}

	if err := repo.RecordLoginStep(testCtx(t), memberID, 1000); err != nil {
		t.Fatalf("RecordLoginStep: %v", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.LastTOTPStep == nil || *enrollment.LastTOTPStep != 1000 {
		t.Errorf("LastTOTPStep = %v, want 1000", enrollment.LastTOTPStep)
	}
}

// TestMFARecordLoginStep_RejectsEqualOrLowerStep is the direct
// replay-guard check: a step that is <= the already-stored value must
// be rejected (ErrInvalidTOTPCode), and the stored value must be
// unchanged.
func TestMFARecordLoginStep_RejectsEqualOrLowerStep(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}
	if err := repo.RecordLoginStep(testCtx(t), memberID, 1000); err != nil {
		t.Fatalf("seed RecordLoginStep: %v", err)
	}

	for _, replay := range []int64{999, 1000} {
		if err := repo.RecordLoginStep(testCtx(t), memberID, replay); !errors.Is(err, domain.ErrInvalidTOTPCode) {
			t.Errorf("RecordLoginStep(%d) after 1000: err = %v, want ErrInvalidTOTPCode", replay, err)
		}
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.LastTOTPStep == nil || *enrollment.LastTOTPStep != 1000 {
		t.Errorf("LastTOTPStep after rejected replays = %v, want unchanged 1000", enrollment.LastTOTPStep)
	}
}

func TestMFARecordLoginStep_AcceptsStrictlyIncreasingSteps(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}

	if err := repo.RecordLoginStep(testCtx(t), memberID, 1000); err != nil {
		t.Fatalf("RecordLoginStep(1000): %v", err)
	}
	if err := repo.RecordLoginStep(testCtx(t), memberID, 1001); err != nil {
		t.Fatalf("RecordLoginStep(1001): %v", err)
	}

	enrollment, err := repo.GetEnrollment(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.LastTOTPStep == nil || *enrollment.LastTOTPStep != 1001 {
		t.Errorf("LastTOTPStep = %v, want 1001", enrollment.LastTOTPStep)
	}
}

// TestMFARecordLoginStep_ConcurrentRace_OnlyTheHigherStepWins is the
// gated concurrency check mirroring ConfirmEnrollmentWithCodes's own
// race test: two callers racing to record DIFFERENT steps for the same
// member must resolve so that only the strictly-increasing write can
// ever win, whichever order they are attempted in.
func TestMFARecordLoginStep_ConcurrentRace_OnlyTheHigherStepWins(t *testing.T) {
	repo, householdID, memberID := newTestMFARepo(t)
	if err := repo.BeginEnrollment(testCtx(t), memberID, householdID, []byte("secret")); err != nil {
		t.Fatalf("BeginEnrollment: %v", err)
	}
	if err := repo.ConfirmEnrollmentWithCodes(testCtx(t), memberID, nil); err != nil {
		t.Fatalf("ConfirmEnrollmentWithCodes: %v", err)
	}
	ctx := testCtx(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = repo.RecordLoginStep(ctx, memberID, 2000)
	}()
	go func() {
		defer wg.Done()
		errs[1] = repo.RecordLoginStep(ctx, memberID, 2001)
	}()
	wg.Wait()

	enrollment, err := repo.GetEnrollment(ctx, memberID)
	if err != nil {
		t.Fatalf("GetEnrollment: %v", err)
	}
	if enrollment.LastTOTPStep == nil || *enrollment.LastTOTPStep != 2001 {
		t.Errorf("LastTOTPStep after racing RecordLoginStep calls = %v, want 2001", enrollment.LastTOTPStep)
	}
	for i, err := range errs {
		if err != nil && !errors.Is(err, domain.ErrInvalidTOTPCode) {
			t.Errorf("RecordLoginStep call %d: unexpected error %v", i, err)
		}
	}
}

func TestMFARecordLoginStep_UnenrolledMemberRejected(t *testing.T) {
	repo, _, memberID := newTestMFARepo(t)
	if err := repo.RecordLoginStep(testCtx(t), memberID, 1000); !errors.Is(err, domain.ErrInvalidTOTPCode) {
		t.Errorf("RecordLoginStep for an unenrolled member: err = %v, want ErrInvalidTOTPCode", err)
	}
}

func TestMFAListUnusedRecoveryCodes_EmptyForNoEnrollment(t *testing.T) {
	repo, _, memberID := newTestMFARepo(t)
	codes, err := repo.ListUnusedRecoveryCodes(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListUnusedRecoveryCodes: %v", err)
	}
	if len(codes) != 0 {
		t.Errorf("got %d recovery codes for a member with no enrollment, want 0", len(codes))
	}
}
