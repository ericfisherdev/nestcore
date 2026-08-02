package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// WebAuthn domain errors.
var (
	// ErrWebAuthnCredentialNotFound is returned by Rename and Delete when
	// no credential matches the given id, member, and household —
	// including when the id is valid but belongs to a DIFFERENT member or
	// household (reported identically, so neither action leaks which one
	// occurred).
	ErrWebAuthnCredentialNotFound = errors.New("identity: webauthn credential not found")
	// ErrWebAuthnVerificationFailed is returned by
	// app.WebAuthnService.FinishRegistration and FinishLogin when the
	// browser's response fails verification against the pending
	// challenge — a wrong RP ID, a challenge mismatch, an expired
	// challenge, an unresolvable user handle, or a replayed (already
	// consumed) challenge are all reported identically, so a caller
	// cannot distinguish which one occurred.
	ErrWebAuthnVerificationFailed = errors.New("identity: webauthn verification failed")
	// ErrWebAuthnCredentialExists is returned by Create when
	// cred.CredentialID is already registered — the WebAuthn credential id
	// an authenticator generates is globally unique by construction, so
	// this indicates the same physical credential is being registered a
	// second time (defense in depth: the caller is expected to exclude a
	// member's own existing credentials from a registration ceremony via
	// WithExclusions, so this should not normally be reachable).
	ErrWebAuthnCredentialExists = errors.New("identity: webauthn credential already registered")
)

// WebAuthnCredential is one member's registered passkey. A member may
// register several (phone, laptop, security key); each row is
// independent — revoking one never affects the others.
type WebAuthnCredential struct {
	ID          WebAuthnCredentialID
	MemberID    MemberID
	HouseholdID HouseholdID
	// CredentialID is the WebAuthn credential id the authenticator
	// itself generated — an opaque byte handle, globally unique by
	// construction.
	CredentialID []byte
	// PublicKey is the CBOR-encoded credential public key. Not encrypted
	// at rest: a public key is not a secret.
	PublicKey []byte
	// SignCount is the authenticator's signature counter as of the last
	// successful ceremony (registration sets it once; a login/step-up
	// assertion updates it thereafter for clone detection).
	SignCount uint32
	// Transports are the authenticator's advertised transport hints
	// (e.g. "internal", "hybrid") — advisory only, never a security
	// boundary.
	Transports []string
	// AAGUID identifies the authenticator model, when the authenticator
	// reports one; nil for a model that reports none.
	AAGUID *uuid.UUID
	// Nickname is the member-chosen label shown in a "Your devices"
	// list.
	Nickname string
	// UserHandle is this member's stable, HMAC-derived WebAuthn user
	// handle (app.WebAuthnUserHandleDeriver), stored redundantly on
	// every one of the member's credential rows so a usernameless login
	// lookup is a single indexed equality query against this table
	// alone.
	UserHandle []byte
	CreatedAt  time.Time
	// LastUsedAt is nil until a login or step-up ceremony first uses
	// this credential.
	LastUsedAt *time.Time
}

// WebAuthnCredentialRepository is the outbound port for persisting and
// retrieving a member's registered WebAuthn credentials. Implementations
// live in identity/adapter.
//
// Error contracts:
//   - ListByMember never returns ErrWebAuthnCredentialNotFound for a
//     member with no credentials — it returns an empty slice.
//   - Create returns ErrHouseholdNotFound when householdID does not
//     exist, ErrMemberNotFound when cred.MemberID does not belong to
//     householdID (FK violations), and ErrWebAuthnCredentialExists when
//     cred.CredentialID is already registered (unique violation).
//   - Rename and Delete return ErrWebAuthnCredentialNotFound when no row
//     matches id scoped to BOTH memberID and householdID — a
//     defense-in-depth tenant check.
//   - FindByUserHandle returns ErrMemberNotFound when no credential row
//     carries handle at all — an unknown, forged, or stale handle, or a
//     handle for a member whose last credential was since revoked
//     (member_credential's composite tenant FK cascades on member
//     deletion, so a resolvable handle always names a member that still
//     exists). Implementations must also return an error (never silently
//     pick one) if handle's rows resolve to more than one DISTINCT
//     member — user_handle carries no uniqueness constraint (it is a
//     plain index, see the migration's own doc), so this is the
//     defense-in-depth guard against a handle collision (an HMAC-derived
//     handle colliding across members, or a derivation defect) silently
//     authenticating the wrong member.
//   - UpdateAfterAssertion returns ErrWebAuthnCredentialNotFound only
//     when credentialID matches no row at all — defense-in-depth only; a
//     caller only ever supplies a credentialID that a preceding
//     FindByUserHandle or ListByMember call in the SAME request just
//     returned. When the row exists but a monotonic guard skipped the
//     write (a concurrent, more recently issued assertion already
//     recorded fresher state), this returns nil, not an error.
type WebAuthnCredentialRepository interface {
	// ListByMember returns every credential registered by memberID,
	// oldest first.
	ListByMember(ctx context.Context, memberID MemberID) ([]WebAuthnCredential, error)

	// Create persists a newly registered credential. The caller supplies
	// a fully populated WebAuthnCredential (ID already assigned via
	// NewWebAuthnCredentialID).
	Create(ctx context.Context, householdID HouseholdID, cred *WebAuthnCredential) error

	// Rename updates the nickname on the credential identified by id,
	// scoped to memberID and householdID.
	Rename(ctx context.Context, householdID HouseholdID, memberID MemberID, id WebAuthnCredentialID, nickname string) error

	// Delete removes the credential identified by id, scoped to
	// memberID and householdID — revoking it immediately.
	Delete(ctx context.Context, householdID HouseholdID, memberID MemberID, id WebAuthnCredentialID) error

	// FindByUserHandle resolves a stable WebAuthn user handle (the value
	// WebAuthnUserHandleDeriver.Derive computes for a member, stored
	// redundantly on every one of that member's credential rows) to the
	// member that owns it and every one of that member's registered
	// credentials — the lookup a usernameless login performs after the
	// browser selects a discoverable credential and the assertion
	// response reports its userHandle.
	FindByUserHandle(ctx context.Context, handle []byte) (MemberID, []WebAuthnCredential, error)

	// UpdateAfterAssertion persists the authenticator's new signature
	// counter and last-used timestamp on the credential identified by
	// credentialID (the raw WebAuthn credential id — globally unique by
	// construction — not this row's own WebAuthnCredentialID). Called
	// after EVERY successful login or step-up assertion, regardless of
	// whether the new count triggered a clone-suspicion flag: the
	// caller (app.WebAuthnService) always advances the stored count to
	// the authenticator's latest reported value so the NEXT assertion's
	// comparison is against up-to-date state, not a stale one that
	// would otherwise re-flag a legitimate, already-seen count forever.
	//
	// The write is monotonically guarded on usedAt: when two assertions
	// on the SAME credential complete concurrently, a later-completing
	// but OLDER (smaller usedAt) one must never overwrite a newer
	// sign_count/last_used_at pair a faster concurrent assertion
	// already recorded — doing so would silently regress state
	// clone-detection depends on. Losing that race is NOT an error (see
	// the interface doc's own error contract): the assertion was
	// already verified before this call, so the caller's login/step-up
	// still succeeds either way.
	UpdateAfterAssertion(ctx context.Context, credentialID []byte, signCount uint32, usedAt time.Time) error
}
