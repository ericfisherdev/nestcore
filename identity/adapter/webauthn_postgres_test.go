package adapter_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// newTestWebAuthnCredentialRepo returns a WebAuthnCredentialRepository
// (and the household id + member id it seeds) backed by
// NESTCORE_TEST_DATABASE_URL, mirroring newTestMFARepo's own pattern.
func newTestWebAuthnCredentialRepo(t *testing.T) (*adapter.WebAuthnCredentialRepository, domain.HouseholdID, domain.MemberID) {
	t.Helper()
	pool := newTestPool(t)
	households, members := adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool)
	m := seedMember(t, households, members, "WebAuthn Household", "Alice")
	return adapter.NewWebAuthnCredentialRepository(pool), m.HouseholdID, m.ID
}

// testWebAuthnCredential builds a fully populated WebAuthnCredential for
// memberID, ready for Create. UserHandle is unique per call (not a fixed
// literal): these tests share one database via
// NESTCORE_TEST_DATABASE_URL, and rows accumulate across runs, so a
// fixed handle would let unrelated members' credentials collide under
// FindByUserHandle in a way no test here intends to exercise.
func testWebAuthnCredential(memberID domain.MemberID, credentialID []byte, nickname string) *domain.WebAuthnCredential {
	aaguid := uuid.Must(uuid.NewRandom())
	return &domain.WebAuthnCredential{
		ID:           domain.NewWebAuthnCredentialID(),
		MemberID:     memberID,
		CredentialID: credentialID,
		PublicKey:    []byte("not-a-real-cbor-public-key"),
		SignCount:    0,
		Transports:   []string{"internal", "hybrid"},
		AAGUID:       &aaguid,
		Nickname:     nickname,
		UserHandle:   []byte("a-derived-user-handle-" + uuid.Must(uuid.NewRandom()).String()),
	}
}

func TestWebAuthnCredentialCreate_PersistsAndListByMemberReturnsIt(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-1"), "My Phone")

	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("ListByMember returned %d credentials, want 1", len(creds))
	}
	got := creds[0]
	if got.ID != cred.ID {
		t.Errorf("ID = %v, want %v", got.ID, cred.ID)
	}
	if string(got.CredentialID) != string(cred.CredentialID) {
		t.Error("CredentialID did not round-trip exactly")
	}
	if string(got.PublicKey) != string(cred.PublicKey) {
		t.Error("PublicKey did not round-trip exactly")
	}
	if got.Nickname != "My Phone" {
		t.Errorf("Nickname = %q, want %q", got.Nickname, "My Phone")
	}
	if got.HouseholdID != householdID {
		t.Errorf("HouseholdID = %v, want %v", got.HouseholdID, householdID)
	}
	if got.AAGUID == nil || *got.AAGUID != *cred.AAGUID {
		t.Errorf("AAGUID = %v, want %v", got.AAGUID, cred.AAGUID)
	}
	if !slices.Equal(got.Transports, cred.Transports) {
		t.Errorf("Transports = %v, want %v", got.Transports, cred.Transports)
	}
	if got.LastUsedAt != nil {
		t.Error("a freshly registered credential must have a nil LastUsedAt")
	}
}

func TestWebAuthnCredentialCreate_NilAAGUID_StoresNull(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-nil-aaguid"), "No AAGUID device")
	cred.AAGUID = nil

	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("ListByMember returned %d credentials, want 1", len(creds))
	}
	if creds[0].AAGUID != nil {
		t.Errorf("AAGUID = %v, want nil", creds[0].AAGUID)
	}
}

func TestWebAuthnCredentialCreate_UnknownMemberInHousehold(t *testing.T) {
	repo, householdID, _ := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(domain.NewMemberID(), []byte("credential-id-unknown-member"), "Ghost device")

	err := repo.Create(testCtx(t), householdID, cred)
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("Create for an unknown member: err = %v, want ErrMemberNotFound", err)
	}
}

// TestWebAuthnCredentialCreate_CrossHouseholdMemberRejected is the gated
// tenant-isolation check (mirroring
// TestMFABeginEnrollment_CrossHouseholdCannotTouchVictimRow's own
// pattern): unlike TestWebAuthnCredentialCreate_UnknownMemberInHousehold
// above, which uses a member id that does not exist AT ALL, this uses a
// REAL, existing member id — paired with a SECOND, GENUINELY EXISTING
// household that is not that member's own. A fabricated household id
// would trip the table's plain household_id FK
// (member_credential_household_id_fkey) before ever reaching the
// composite FK below, which would prove nothing about tenant isolation
// specifically. Only the composite (household_id, member_id) FK
// member_credential_member_fk correctly rejects it, because that exact
// pair does not exist in identity.member.
func TestWebAuthnCredentialCreate_CrossHouseholdMemberRejected(t *testing.T) {
	pool := newTestPool(t)
	households, members := adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool)
	victim := seedMember(t, households, members, "Victim Household", "Victim")
	attackerHousehold := seedHousehold(t, households, "Attacker Household")
	repo := adapter.NewWebAuthnCredentialRepository(pool)
	cred := testWebAuthnCredential(victim.ID, []byte("credential-id-cross-household"), "Attacker-supplied")

	err := repo.Create(testCtx(t), attackerHousehold.ID, cred)
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("Create for a real member under a real household that is not theirs: err = %v, want ErrMemberNotFound", err)
	}

	creds, err := repo.ListByMember(testCtx(t), victim.ID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 0 {
		t.Error("a rejected cross-household Create must not persist a credential under the victim member")
	}
}

// TestWebAuthnCredentialCreate_UnknownHouseholdRejected covers the OTHER
// half of member_credential's dual FK (see
// mapWebAuthnCredentialFKViolation): a householdID that does not exist
// AT ALL must trip the plain household_id FK
// (member_credential_household_id_fkey) and map to
// domain.ErrHouseholdNotFound, distinctly from the composite member FK's
// domain.ErrMemberNotFound above.
func TestWebAuthnCredentialCreate_UnknownHouseholdRejected(t *testing.T) {
	repo, _, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-unknown-household"), "Orphan device")

	err := repo.Create(testCtx(t), domain.NewHouseholdID(), cred)
	if !errors.Is(err, domain.ErrHouseholdNotFound) {
		t.Errorf("Create for an unknown household: err = %v, want ErrHouseholdNotFound", err)
	}
}

func TestWebAuthnCredentialListByMember_EmptyForNoCredentials(t *testing.T) {
	repo, _, memberID := newTestWebAuthnCredentialRepo(t)
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("got %d credentials for a member with none, want 0", len(creds))
	}
}

// TestWebAuthnCredentialListByMember_MultipleCredentials_OldestFirst
// asserts membership (both created credentials come back) AND the
// repository's actual documented ordering contract (ORDER BY
// created_at, id — see ListByMember's own doc) directly, rather than
// assuming the two Create calls' real-world timing happens to land them
// in array-index order.
func TestWebAuthnCredentialListByMember_MultipleCredentials_OldestFirst(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	first := testWebAuthnCredential(memberID, []byte("credential-id-first"), "First device")
	if err := repo.Create(testCtx(t), householdID, first); err != nil {
		t.Fatalf("Create (first): %v", err)
	}
	second := testWebAuthnCredential(memberID, []byte("credential-id-second"), "Second device")
	if err := repo.Create(testCtx(t), householdID, second); err != nil {
		t.Fatalf("Create (second): %v", err)
	}

	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("ListByMember returned %d credentials, want 2", len(creds))
	}

	returned := map[domain.WebAuthnCredentialID]bool{creds[0].ID: true, creds[1].ID: true}
	if !returned[first.ID] || !returned[second.ID] {
		t.Fatalf("ListByMember did not return both created credentials: got %v and %v, want %v and %v",
			creds[0].ID, creds[1].ID, first.ID, second.ID)
	}

	switch {
	case creds[0].CreatedAt.After(creds[1].CreatedAt):
		t.Errorf("ListByMember returned a later created_at before an earlier one: %v then %v", creds[0].CreatedAt, creds[1].CreatedAt)
	case creds[0].CreatedAt.Equal(creds[1].CreatedAt):
		idA, idB := uuid.UUID(creds[0].ID), uuid.UUID(creds[1].ID)
		if bytes.Compare(idA[:], idB[:]) > 0 {
			t.Errorf("ListByMember did not break a created_at tie by ascending id: %v then %v", creds[0].ID, creds[1].ID)
		}
	}
}

func TestWebAuthnCredentialRename_UpdatesNickname(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-rename"), "Old Name")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Rename(testCtx(t), householdID, memberID, cred.ID, "New Name"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 || creds[0].Nickname != "New Name" {
		t.Errorf("Nickname after rename = %+v, want New Name", creds)
	}
}

func TestWebAuthnCredentialRename_WrongMemberRejected(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-wrong-member"), "Victim device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := repo.Rename(testCtx(t), householdID, domain.NewMemberID(), cred.ID, "Hijacked")
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("Rename with the wrong member: err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 || creds[0].Nickname != "Victim device" {
		t.Error("a rejected cross-member rename must not change the victim's nickname")
	}
}

func TestWebAuthnCredentialRename_WrongHouseholdRejected(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-rename-wrong-household"), "Victim device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := repo.Rename(testCtx(t), domain.NewHouseholdID(), memberID, cred.ID, "Hijacked")
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("Rename with a mismatched household: err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 || creds[0].Nickname != "Victim device" {
		t.Error("a rejected mismatched-household rename must not change the victim's nickname")
	}
}

func TestWebAuthnCredentialRename_NotFound(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	err := repo.Rename(testCtx(t), householdID, memberID, domain.NewWebAuthnCredentialID(), "x")
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("Rename(never created): err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
}

func TestWebAuthnCredentialDelete_RemovesImmediately(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-delete"), "Doomed device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(testCtx(t), householdID, memberID, cred.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("credentials after delete = %d, want 0", len(creds))
	}
}

func TestWebAuthnCredentialDelete_WrongMemberRejected(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-delete-wrong-member"), "Victim device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := repo.Delete(testCtx(t), householdID, domain.NewMemberID(), cred.ID)
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("Delete with a mismatched member: err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 {
		t.Error("the credential must survive a mismatched-member delete attempt")
	}
}

func TestWebAuthnCredentialDelete_WrongHouseholdRejected(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	cred := testWebAuthnCredential(memberID, []byte("credential-id-wrong-household"), "Device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := repo.Delete(testCtx(t), domain.NewHouseholdID(), memberID, cred.ID)
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("Delete with a mismatched household: err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
	if creds, err := repo.ListByMember(testCtx(t), memberID); err != nil || len(creds) != 1 {
		t.Error("the credential must survive a mismatched-household delete attempt")
	}
}

func TestWebAuthnCredentialDelete_NotFound(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	err := repo.Delete(testCtx(t), householdID, memberID, domain.NewWebAuthnCredentialID())
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("Delete(never created): err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
}

func TestWebAuthnCredentialCreate_DuplicateCredentialIDRejected(t *testing.T) {
	// Defense-in-depth: credential_id is UNIQUE — a second Create for the
	// SAME raw WebAuthn credential id must fail rather than silently
	// duplicate the row.
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	credentialID := []byte("credential-id-duplicate")
	first := testWebAuthnCredential(memberID, credentialID, "First")
	if err := repo.Create(testCtx(t), householdID, first); err != nil {
		t.Fatalf("Create (first): %v", err)
	}

	second := testWebAuthnCredential(memberID, credentialID, "Second")
	err := repo.Create(testCtx(t), householdID, second)
	if !errors.Is(err, domain.ErrWebAuthnCredentialExists) {
		t.Errorf("Create with a duplicate credential_id: err = %v, want ErrWebAuthnCredentialExists", err)
	}
}

// ---------------------------------------------------------------------------
// FindByUserHandle / UpdateAfterAssertion
// ---------------------------------------------------------------------------

// testWebAuthnCredentialWithHandle is testWebAuthnCredential plus an
// explicit UserHandle — unlike testWebAuthnCredential's own fixed
// "a-derived-user-handle" (fine for tests that only ever seed ONE
// member), FindByUserHandle's own tests need per-member,
// per-test-controlled handles to actually exercise handle-scoped
// lookup.
func testWebAuthnCredentialWithHandle(memberID domain.MemberID, credentialID, userHandle []byte, nickname string) *domain.WebAuthnCredential {
	cred := testWebAuthnCredential(memberID, credentialID, nickname)
	cred.UserHandle = userHandle
	return cred
}

func TestWebAuthnCredentialFindByUserHandle_ReturnsMemberAndAllTheirCredentials(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	handle := []byte("member-handle-multi-cred")
	first := testWebAuthnCredentialWithHandle(memberID, []byte("credential-id-handle-first"), handle, "Phone")
	if err := repo.Create(testCtx(t), householdID, first); err != nil {
		t.Fatalf("Create (first): %v", err)
	}
	second := testWebAuthnCredentialWithHandle(memberID, []byte("credential-id-handle-second"), handle, "Laptop")
	if err := repo.Create(testCtx(t), householdID, second); err != nil {
		t.Fatalf("Create (second): %v", err)
	}

	gotMemberID, creds, err := repo.FindByUserHandle(testCtx(t), handle)
	if err != nil {
		t.Fatalf("FindByUserHandle: %v", err)
	}
	if gotMemberID != memberID {
		t.Errorf("FindByUserHandle member = %v, want %v", gotMemberID, memberID)
	}
	if len(creds) != 2 {
		t.Fatalf("FindByUserHandle returned %d credentials, want 2", len(creds))
	}
	returned := map[domain.WebAuthnCredentialID]bool{creds[0].ID: true, creds[1].ID: true}
	if !returned[first.ID] || !returned[second.ID] {
		t.Errorf("FindByUserHandle did not return both credentials: got %v and %v", creds[0].ID, creds[1].ID)
	}
	for _, c := range creds {
		if c.MemberID != memberID {
			t.Errorf("credential %v MemberID = %v, want %v", c.ID, c.MemberID, memberID)
		}
		if c.HouseholdID != householdID {
			t.Errorf("credential %v HouseholdID = %v, want %v", c.ID, c.HouseholdID, householdID)
		}
	}
}

func TestWebAuthnCredentialFindByUserHandle_UnknownHandleRejected(t *testing.T) {
	repo, _, _ := newTestWebAuthnCredentialRepo(t)

	_, creds, err := repo.FindByUserHandle(testCtx(t), []byte("nobody-registered-this-handle"))
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("FindByUserHandle(unknown handle): err = %v, want ErrMemberNotFound", err)
	}
	if len(creds) != 0 {
		t.Errorf("FindByUserHandle(unknown handle) returned %d credentials, want 0", len(creds))
	}
}

// TestWebAuthnCredentialFindByUserHandle_DoesNotReturnOtherMembersCredentials
// is the tenant-isolation check for the login lookup itself: a member's
// handle must never resolve another member's credentials, even when
// both members are seeded in the SAME test run
// (member_credential_user_handle_idx is a plain, non-unique index — the
// WHERE clause, not the index alone, is what must scope correctly).
func TestWebAuthnCredentialFindByUserHandle_DoesNotReturnOtherMembersCredentials(t *testing.T) {
	pool := newTestPool(t)
	households, members := adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool)
	repo := adapter.NewWebAuthnCredentialRepository(pool)

	victim := seedMember(t, households, members, "Victim Handle Household", "Victim")
	attacker := seedMember(t, households, members, "Attacker Handle Household", "Attacker")

	victimHandle := []byte("victim-handle")
	attackerHandle := []byte("attacker-handle")
	victimCred := testWebAuthnCredentialWithHandle(victim.ID, []byte("credential-id-victim"), victimHandle, "Victim's phone")
	if err := repo.Create(testCtx(t), victim.HouseholdID, victimCred); err != nil {
		t.Fatalf("Create (victim): %v", err)
	}
	attackerCred := testWebAuthnCredentialWithHandle(attacker.ID, []byte("credential-id-attacker"), attackerHandle, "Attacker's phone")
	if err := repo.Create(testCtx(t), attacker.HouseholdID, attackerCred); err != nil {
		t.Fatalf("Create (attacker): %v", err)
	}

	gotMemberID, creds, err := repo.FindByUserHandle(testCtx(t), victimHandle)
	if err != nil {
		t.Fatalf("FindByUserHandle: %v", err)
	}
	if gotMemberID != victim.ID {
		t.Errorf("FindByUserHandle(victim's handle) resolved member = %v, want %v", gotMemberID, victim.ID)
	}
	if len(creds) != 1 || creds[0].ID != victimCred.ID {
		t.Errorf("FindByUserHandle(victim's handle) returned %+v, want only the victim's own credential", creds)
	}
}

func TestWebAuthnCredentialUpdateAfterAssertion_UpdatesSignCountAndLastUsedAt(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	credentialID := []byte("credential-id-update-assertion")
	cred := testWebAuthnCredential(memberID, credentialID, "Device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	usedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.UpdateAfterAssertion(testCtx(t), credentialID, 7, usedAt); err != nil {
		t.Fatalf("UpdateAfterAssertion: %v", err)
	}

	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("ListByMember returned %d credentials, want 1", len(creds))
	}
	got := creds[0]
	if got.SignCount != 7 {
		t.Errorf("SignCount = %d, want 7", got.SignCount)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(usedAt) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, usedAt)
	}
}

// TestWebAuthnCredentialUpdateAfterAssertion_OutOfOrderDoesNotRegressState
// is the concurrency regression this method's monotonic guard exists
// for: two assertions on the SAME credential can complete out of
// real-time order — a slower request, carrying an OLDER usedAt, can
// still reach this method AFTER a faster, more-recently-issued
// assertion already won. The later (older) call must not overwrite the
// fresher state already on file, and must not error either.
func TestWebAuthnCredentialUpdateAfterAssertion_OutOfOrderDoesNotRegressState(t *testing.T) {
	repo, householdID, memberID := newTestWebAuthnCredentialRepo(t)
	credentialID := []byte("credential-id-out-of-order")
	cred := testWebAuthnCredential(memberID, credentialID, "Device")
	if err := repo.Create(testCtx(t), householdID, cred); err != nil {
		t.Fatalf("Create: %v", err)
	}

	older := time.Now().UTC().Truncate(time.Microsecond)
	newer := older.Add(time.Second)

	if err := repo.UpdateAfterAssertion(testCtx(t), credentialID, 10, newer); err != nil {
		t.Fatalf("UpdateAfterAssertion (newer, first write): %v", err)
	}
	if err := repo.UpdateAfterAssertion(testCtx(t), credentialID, 5, older); err != nil {
		t.Fatalf("UpdateAfterAssertion (older, second write): %v", err)
	}

	creds, err := repo.ListByMember(testCtx(t), memberID)
	if err != nil {
		t.Fatalf("ListByMember: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("ListByMember returned %d credentials, want 1", len(creds))
	}
	got := creds[0]
	if got.SignCount != 10 {
		t.Errorf("SignCount after an out-of-order older write = %d, want 10 (the newer write must win)", got.SignCount)
	}
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(newer) {
		t.Errorf("LastUsedAt after an out-of-order older write = %v, want %v (the newer write must win)", got.LastUsedAt, newer)
	}
}

func TestWebAuthnCredentialUpdateAfterAssertion_NotFound(t *testing.T) {
	repo, _, _ := newTestWebAuthnCredentialRepo(t)
	err := repo.UpdateAfterAssertion(testCtx(t), []byte("never-registered-credential-id"), 1, time.Now())
	if !errors.Is(err, domain.ErrWebAuthnCredentialNotFound) {
		t.Errorf("UpdateAfterAssertion(unknown credential id): err = %v, want ErrWebAuthnCredentialNotFound", err)
	}
}
