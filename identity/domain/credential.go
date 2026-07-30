package domain

import (
	"context"
	"errors"
)

// Credential pairs a MemberID with the stored argon2id password hash. It
// is looked up by email (the login form's identifier) and written
// against a known member id (the add-credential / change-password flow),
// backed by identity.member's own email and password_hash columns — see
// identity/migrate's baseline migration, not a separate credential table.
type Credential struct {
	MemberID     MemberID
	PasswordHash string
}

// Domain errors returned by CredentialRepository implementations.
var (
	// ErrInvalidCredentials is returned by FindByEmail when no matching
	// credential is found. It is intentionally generic — callers must not
	// distinguish "user not found" from "wrong password" to prevent user
	// enumeration.
	ErrInvalidCredentials = errors.New("identity: invalid credentials")
	// ErrEmailAlreadyInUse is returned by SetCredential when the email is
	// already assigned to a different member (the email column is
	// unique).
	ErrEmailAlreadyInUse = errors.New("identity: email already in use")
)

// CredentialRepository is the outbound port for looking up and writing
// login credentials. Implementations live in identity/adapter.
//
// Error contracts:
//   - FindByEmail returns ErrInvalidCredentials when no member with that
//     email and a password_hash exists (no user enumeration).
//   - SetCredential returns ErrMemberNotFound when the member id does not
//     exist, and ErrEmailAlreadyInUse when the email belongs to another
//     member.
type CredentialRepository interface {
	// FindByEmail looks up the credential for the given email address. It
	// returns ErrInvalidCredentials when no active credential is found, so
	// callers cannot distinguish "no account" from "wrong password".
	FindByEmail(ctx context.Context, email string) (*Credential, error)

	// SetCredential stores (or replaces) the email and password hash on
	// the member row identified by memberID. Returns ErrMemberNotFound
	// when the member does not exist.
	SetCredential(ctx context.Context, memberID MemberID, email, passwordHash string) error
}
