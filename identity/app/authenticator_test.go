package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/crypto/cryptotest"
	"github.com/ericfisherdev/nestcore/identity/app"
	"github.com/ericfisherdev/nestcore/identity/domain"
)

// fakeRepo is an in-memory CredentialRepository used for hermetic unit
// tests.
type fakeRepo struct {
	// credentials maps email -> Credential.
	credentials map[string]*domain.Credential
}

func (f *fakeRepo) FindByEmail(_ context.Context, email string) (*domain.Credential, error) {
	c, ok := f.credentials[email]
	if !ok {
		return nil, domain.ErrInvalidCredentials
	}
	return c, nil
}

func (f *fakeRepo) SetCredential(_ context.Context, _ domain.MemberID, _, _ string) error {
	return nil
}

// newFixture creates a fakeRepo with one seeded credential for email using
// the provided plaintext password. The fixture is hashed at cheap test
// parameters: it is still a realistic PHC string, and Verify reads the
// cost back out of it, so the login paths under test behave exactly as
// they do in production.
func newFixture(t *testing.T, email, password string) (*fakeRepo, domain.MemberID) {
	t.Helper()
	hash, err := cryptotest.Hasher().Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	memberID := domain.NewMemberID()
	repo := &fakeRepo{
		credentials: map[string]*domain.Credential{
			email: {MemberID: memberID, PasswordHash: hash},
		},
	}
	return repo, memberID
}

func TestLoginSuccess(t *testing.T) {
	t.Parallel()
	const (
		email    = "alice@example.com"
		password = "correct-password"
	)
	repo, wantID := newFixture(t, email, password)
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	gotID, err := authn.Login(context.Background(), email, password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotID != wantID {
		t.Errorf("Login MemberID = %v, want %v", gotID, wantID)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	t.Parallel()
	repo, _ := newFixture(t, "bob@example.com", "rightpassword")
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	_, err := authn.Login(context.Background(), "bob@example.com", "wrongpassword")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login(wrong password) error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginUnknownEmail(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{credentials: make(map[string]*domain.Credential)}
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	_, err := authn.Login(context.Background(), "nobody@example.com", "anypassword")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login(unknown email) error = %v, want ErrInvalidCredentials", err)
	}
}

// TestLoginVerifiesProductionCostFixture proves a password hash produced
// at Nestova's current argon2id production parameters
// (crypto.DefaultParams) verifies through the new Authenticator — the
// contract existing member password hashes migrate onto as-is. The
// Authenticator itself is constructed with the cheap test hasher: Verify
// reads its cost parameters back out of the PHC-encoded hash it is
// checking, not from the hasher's own configuration, so a cheap hasher
// correctly verifies a hash produced at any cost, including production's.
func TestLoginVerifiesProductionCostFixture(t *testing.T) {
	t.Parallel()
	const (
		email    = "fixture@example.com"
		password = "a-real-production-password"
	)
	productionHash, err := crypto.NewHasher(crypto.DefaultParams()).Hash(password)
	if err != nil {
		t.Fatalf("Hash at production cost: %v", err)
	}
	memberID := domain.NewMemberID()
	repo := &fakeRepo{
		credentials: map[string]*domain.Credential{
			email: {MemberID: memberID, PasswordHash: productionHash},
		},
	}
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	gotID, err := authn.Login(context.Background(), email, password)
	if err != nil {
		t.Fatalf("Login with production-cost fixture: %v", err)
	}
	if gotID != memberID {
		t.Errorf("Login MemberID = %v, want %v", gotID, memberID)
	}
}

// TestLoginMalformedStoredHashIsNotInvalidCredentials proves a stored hash
// that fails to parse (data corruption, a truncated column, or a hash
// written by something other than crypto.Hash) is surfaced as a distinct
// error, not silently folded into ErrInvalidCredentials — mirroring how a
// database-outage lookup failure is already surfaced rather than masked.
func TestLoginMalformedStoredHashIsNotInvalidCredentials(t *testing.T) {
	t.Parallel()
	const email = "corrupted@example.com"
	repo := &fakeRepo{
		credentials: map[string]*domain.Credential{
			email: {MemberID: domain.NewMemberID(), PasswordHash: "not-a-phc-hash"},
		},
	}
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	_, err := authn.Login(context.Background(), email, "anypassword")
	if err == nil {
		t.Fatal("Login(malformed stored hash) error = nil, want a wrapped error")
	}
	if errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login(malformed stored hash) error = %v, want a distinct error, not ErrInvalidCredentials", err)
	}
	if !errors.Is(err, crypto.ErrMalformedHash) {
		t.Errorf("Login(malformed stored hash) error = %v, want it to wrap crypto.ErrMalformedHash", err)
	}
}

// countingHasher wraps a real hasher and records how many derivations it
// is asked to perform, so tests can assert on argon2 usage rather than on
// wall time (which would be flaky).
type countingHasher struct {
	inner    *crypto.Hasher
	hashes   int
	verifies int
}

func newCountingHasher() *countingHasher {
	return &countingHasher{inner: cryptotest.Hasher()}
}

func (c *countingHasher) Hash(password string) (string, error) {
	c.hashes++
	return c.inner.Hash(password)
}

func (c *countingHasher) Verify(password, encoded string) (bool, error) {
	c.verifies++
	return c.inner.Verify(password, encoded)
}

// TestNewDerivesTimingDummyOncePerAuthenticator pins the dummy hash to
// being derived once per Authenticator, from the injected hasher, rather
// than at package init: merely importing this package must not cost an
// argon2 derivation.
func TestNewDerivesTimingDummyOncePerAuthenticator(t *testing.T) {
	t.Parallel()
	counter := newCountingHasher()
	repo := &fakeRepo{credentials: make(map[string]*domain.Credential)}

	app.NewAuthenticator(repo, counter)

	if counter.hashes != 1 {
		t.Errorf("New performed %d derivations, want exactly 1 (the timing dummy)", counter.hashes)
	}
}

// TestLoginUnknownEmailStillVerifiesForTiming guards the
// user-enumeration defence. The unknown-email path must perform a
// verification against the dummy hash so it costs about as much as the
// wrong-password path; if a refactor dropped that call, the two paths
// would become distinguishable by response time. Asserting on the call
// count rather than on elapsed time keeps this deterministic.
func TestLoginUnknownEmailStillVerifiesForTiming(t *testing.T) {
	t.Parallel()
	counter := newCountingHasher()
	repo := &fakeRepo{credentials: make(map[string]*domain.Credential)}
	authn := app.NewAuthenticator(repo, counter)

	before := counter.verifies
	_, err := authn.Login(context.Background(), "nobody@example.com", "anypassword")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Login(unknown email) error = %v, want ErrInvalidCredentials", err)
	}
	if got := counter.verifies - before; got != 1 {
		t.Errorf("unknown-email path performed %d verifications, want 1 (the timing equalizer)", got)
	}
}

// TestNewPanicsOnNilDependencies proves the constructor-injection
// invariant: both dependencies are required, matching the "all
// dependencies injected via constructors" acceptance criterion.
func TestNewPanicsOnNilDependencies(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{credentials: make(map[string]*domain.Credential)}

	assertPanics(t, "nil repo", func() { app.NewAuthenticator(nil, cryptotest.Hasher()) })
	assertPanics(t, "nil hasher", func() { app.NewAuthenticator(repo, nil) })
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: New did not panic, want panic", name)
		}
	}()
	fn()
}
