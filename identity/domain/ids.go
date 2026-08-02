package domain

import (
	"fmt"

	"github.com/google/uuid"
)

// HouseholdID uniquely identifies a household.
type HouseholdID uuid.UUID

// MemberID uniquely identifies a member.
type MemberID uuid.UUID

// RecoveryCodeID uniquely identifies one MFA recovery code row.
type RecoveryCodeID uuid.UUID

// WebAuthnCredentialID uniquely identifies one registered WebAuthn
// credential row. Distinct from the credential's own id
// (WebAuthnCredential.CredentialID, an opaque handle the authenticator
// itself generates) — this is identity's own row identifier, used for
// rename/revoke.
type WebAuthnCredentialID uuid.UUID

// NewHouseholdID returns a new time-ordered (UUIDv7) household id, which
// gives better B-tree index locality than random v4 ids. uuid.NewV7 only
// errors if the crypto random source is unavailable — the same failure
// under which uuid.New itself panics — so Must is appropriate here.
func NewHouseholdID() HouseholdID { return HouseholdID(uuid.Must(uuid.NewV7())) }

// NewMemberID returns a new time-ordered (UUIDv7) member id.
func NewMemberID() MemberID { return MemberID(uuid.Must(uuid.NewV7())) }

// NewRecoveryCodeID returns a new time-ordered (UUIDv7) recovery code id.
func NewRecoveryCodeID() RecoveryCodeID { return RecoveryCodeID(uuid.Must(uuid.NewV7())) }

// NewWebAuthnCredentialID returns a new time-ordered (UUIDv7) credential
// row id.
func NewWebAuthnCredentialID() WebAuthnCredentialID {
	return WebAuthnCredentialID(uuid.Must(uuid.NewV7()))
}

// String returns the canonical UUID string.
func (id HouseholdID) String() string { return uuid.UUID(id).String() }

// String returns the canonical UUID string.
func (id MemberID) String() string { return uuid.UUID(id).String() }

// String returns the canonical UUID string.
func (id RecoveryCodeID) String() string { return uuid.UUID(id).String() }

// String returns the canonical UUID string.
func (id WebAuthnCredentialID) String() string { return uuid.UUID(id).String() }

// ParseHouseholdID parses a canonical UUID string into a HouseholdID.
func ParseHouseholdID(s string) (HouseholdID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return HouseholdID{}, fmt.Errorf("parse household id: %w", err)
	}
	return HouseholdID(u), nil
}

// ParseMemberID parses a canonical UUID string into a MemberID.
func ParseMemberID(s string) (MemberID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return MemberID{}, fmt.Errorf("parse member id: %w", err)
	}
	return MemberID(u), nil
}

// ParseRecoveryCodeID parses a canonical UUID string into a RecoveryCodeID.
func ParseRecoveryCodeID(s string) (RecoveryCodeID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return RecoveryCodeID{}, fmt.Errorf("parse recovery code id: %w", err)
	}
	return RecoveryCodeID(u), nil
}

// ParseWebAuthnCredentialID parses a canonical UUID string into a
// WebAuthnCredentialID.
func ParseWebAuthnCredentialID(s string) (WebAuthnCredentialID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return WebAuthnCredentialID{}, fmt.Errorf("parse webauthn credential id: %w", err)
	}
	return WebAuthnCredentialID(u), nil
}
