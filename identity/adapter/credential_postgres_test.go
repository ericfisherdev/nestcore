package adapter_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

func newCredentialTestRepos(t *testing.T) (*adapter.HouseholdRepository, *adapter.MemberRepository, *adapter.CredentialRepository) {
	t.Helper()
	pool := newTestPool(t)
	return adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool), adapter.NewCredentialRepository(pool)
}

func seedMember(t *testing.T, households *adapter.HouseholdRepository, members *adapter.MemberRepository, householdName, displayName string) *domain.Member {
	t.Helper()
	h := seedHousehold(t, households, householdName)
	m := &domain.Member{ID: domain.NewMemberID(), HouseholdID: h.ID, DisplayName: displayName, Role: domain.RoleAdult}
	if err := members.CreateMember(testCtx(t), m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	return m
}

func TestSetCredentialAndFindByEmail(t *testing.T) {
	households, members, credentials := newCredentialTestRepos(t)
	m := seedMember(t, households, members, "Credential Household", "Alice")

	const (
		email = "alice@example.com"
		hash  = "argon2id-hash-placeholder"
	)
	if err := credentials.SetCredential(testCtx(t), m.ID, email, hash); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	got, err := credentials.FindByEmail(testCtx(t), email)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if got.MemberID != m.ID || got.PasswordHash != hash {
		t.Errorf("FindByEmail = %+v, want MemberID=%v PasswordHash=%q", got, m.ID, hash)
	}
}

func TestSetCredentialDuplicateEmail(t *testing.T) {
	households, members, credentials := newCredentialTestRepos(t)
	first := seedMember(t, households, members, "Duplicate Email Household", "Alice")
	second := seedMember(t, households, members, "Duplicate Email Household", "Bob")

	const email = "shared@example.com"
	if err := credentials.SetCredential(testCtx(t), first.ID, email, "hash-one"); err != nil {
		t.Fatalf("SetCredential(first): %v", err)
	}
	err := credentials.SetCredential(testCtx(t), second.ID, email, "hash-two")
	if !errors.Is(err, domain.ErrEmailAlreadyInUse) {
		t.Errorf("SetCredential(duplicate email) error = %v, want ErrEmailAlreadyInUse", err)
	}
}

func TestSetCredentialUnknownMember(t *testing.T) {
	_, _, credentials := newCredentialTestRepos(t)
	err := credentials.SetCredential(testCtx(t), domain.NewMemberID(), "nobody@example.com", "hash")
	if !errors.Is(err, domain.ErrMemberNotFound) {
		t.Errorf("SetCredential(unknown member) error = %v, want ErrMemberNotFound", err)
	}
}

func TestFindByEmailUnknown(t *testing.T) {
	_, _, credentials := newCredentialTestRepos(t)
	if _, err := credentials.FindByEmail(testCtx(t), "nobody@example.com"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("FindByEmail(unknown) error = %v, want ErrInvalidCredentials", err)
	}
}

// TestFindByEmailNoPasswordSet proves a member with no credential set
// (email and password_hash both NULL, the child-member case) does not
// leak through FindByEmail even by accident — there is no email to query
// by in the first place, so this simply confirms the not-found path.
func TestFindByEmailNoPasswordSet(t *testing.T) {
	households, members, credentials := newCredentialTestRepos(t)
	seedMember(t, households, members, "No Credential Household", "Child")

	if _, err := credentials.FindByEmail(testCtx(t), ""); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("FindByEmail(no credential set) error = %v, want ErrInvalidCredentials", err)
	}
}
