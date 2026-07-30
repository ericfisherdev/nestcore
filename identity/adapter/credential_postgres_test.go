package adapter_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestcore/identity/adapter"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

func newCredentialTestRepos(t *testing.T) (*adapter.HouseholdRepository, *adapter.MemberRepository, *adapter.CredentialRepository) {
	t.Helper()
	pool := newTestPool(t)
	return adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool), adapter.NewCredentialRepository(pool)
}

// newCredentialTestReposWithPool is newCredentialTestRepos plus the pool
// itself, for the handful of tests that need to poke a column no port
// exposes a write for (e.g. deactivating a member directly, standing in
// for the NSTR-111 guard this schema's active flag ultimately backs).
func newCredentialTestReposWithPool(t *testing.T) (*pgxpool.Pool, *adapter.HouseholdRepository, *adapter.MemberRepository, *adapter.CredentialRepository) {
	t.Helper()
	pool := newTestPool(t)
	return pool, adapter.NewHouseholdRepository(pool), adapter.NewMemberRepository(pool), adapter.NewCredentialRepository(pool)
}

func deactivateMember(t *testing.T, pool *pgxpool.Pool, memberID domain.MemberID) {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `UPDATE identity.member SET active = false WHERE id = $1`, memberID.String()); err != nil {
		t.Fatalf("deactivate member: %v", err)
	}
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

// TestFindByEmailDeactivatedMember proves a deactivated member
// (identity.member.active = false) does not authenticate even with a
// correct email and an intact password_hash — the login read path is one
// of the readers identity/migrate's package doc names for that flag.
func TestFindByEmailDeactivatedMember(t *testing.T) {
	pool, households, members, credentials := newCredentialTestReposWithPool(t)
	m := seedMember(t, households, members, "Deactivated Household", "Alice")

	const email = "deactivated@example.com"
	if err := credentials.SetCredential(testCtx(t), m.ID, email, "hash"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	deactivateMember(t, pool, m.ID)

	if _, err := credentials.FindByEmail(testCtx(t), email); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("FindByEmail(deactivated member) error = %v, want ErrInvalidCredentials", err)
	}
}

// TestSetCredentialReplacesExisting covers the "(or replaces)" half of
// SetCredential's contract — the change-password/change-email flow —
// which the single-write cases above do not exercise.
func TestSetCredentialReplacesExisting(t *testing.T) {
	households, members, credentials := newCredentialTestRepos(t)
	m := seedMember(t, households, members, "Replace Household", "Alice")

	if err := credentials.SetCredential(testCtx(t), m.ID, "old@example.com", "hash-old"); err != nil {
		t.Fatalf("SetCredential(initial): %v", err)
	}
	if err := credentials.SetCredential(testCtx(t), m.ID, "new@example.com", "hash-new"); err != nil {
		t.Fatalf("SetCredential(replacement): %v", err)
	}

	got, err := credentials.FindByEmail(testCtx(t), "new@example.com")
	if err != nil {
		t.Fatalf("FindByEmail(new address): %v", err)
	}
	if got.MemberID != m.ID || got.PasswordHash != "hash-new" {
		t.Errorf("FindByEmail = %+v, want MemberID=%v PasswordHash=%q", got, m.ID, "hash-new")
	}
	if _, err := credentials.FindByEmail(testCtx(t), "old@example.com"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("FindByEmail(old address) error = %v, want ErrInvalidCredentials", err)
	}
}
