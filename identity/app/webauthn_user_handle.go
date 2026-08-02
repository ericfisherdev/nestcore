package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"github.com/google/uuid"

	"github.com/ericfisherdev/nestcore/identity/domain"
)

// WebAuthnUserHandleDeriver derives each member's stable WebAuthn user
// handle — the opaque "user.id" a WebAuthn ceremony uses (the WebAuthn
// spec recommends this value be completely random) — deterministically
// from a purpose-scoped HMAC key.
//
// The handle is deliberately DETERMINISTIC (HMAC(key, memberID), not a
// stored random value) for two reasons: it lets the webAuthnUser adapter
// compute WebAuthnID() purely from the member id, with no extra database
// round-trip or per-member row to keep in sync; and it gives a
// usernameless login ceremony a value to look member_credential.user_handle
// up by without ever storing (or needing to reverse) anything that maps
// directly back to the member's real database id — the handle is a
// one-way, per-relying-party pseudonym for the member, exactly as the
// WebAuthn spec recommends. Because it is deterministic rather than
// random, the SAME member always derives the SAME handle across every
// one of their registered credentials, which is also what
// webauthn.WebAuthn.CreateCredential's own user-id-matches-session check
// (session.UserID, set from WebAuthnID() at BeginRegistration) requires.
type WebAuthnUserHandleDeriver struct {
	key []byte
}

// NewWebAuthnUserHandleDeriver constructs a deriver from a non-empty HMAC
// key.
func NewWebAuthnUserHandleDeriver(key []byte) (*WebAuthnUserHandleDeriver, error) {
	if len(key) == 0 {
		return nil, errors.New("identity: webauthn user handle deriver requires a non-empty key")
	}
	return &WebAuthnUserHandleDeriver{key: key}, nil
}

// NewWebAuthnUserHandleDeriverFromSecret derives a purpose-scoped HMAC
// key from secret via HMAC-SHA256(secret, purpose): a caller's session
// secret is typically shared by more than one signing/deriving consumer,
// and deriving a distinct subkey per purpose keeps each cryptographically
// independent even though they trace back to the same root secret.
func NewWebAuthnUserHandleDeriverFromSecret(secret []byte, purpose string) (*WebAuthnUserHandleDeriver, error) {
	if len(secret) == 0 {
		return nil, errors.New("identity: webauthn user handle deriver requires a non-empty secret")
	}
	if purpose == "" {
		return nil, errors.New("identity: webauthn user handle deriver requires a non-empty purpose label")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(purpose))
	return NewWebAuthnUserHandleDeriver(mac.Sum(nil))
}

// Derive returns memberID's stable, opaque WebAuthn user handle: a
// 32-byte HMAC-SHA256 digest, comfortably under the WebAuthn spec's
// 64-byte user handle maximum.
//
// The HMAC input is memberID's raw 16 UUID bytes, not its String() form
// deliberately: domain.MemberID is defined as uuid.UUID, so this is a
// direct, allocation-free array slice, not a format that could ever
// silently change shape.
func (d *WebAuthnUserHandleDeriver) Derive(memberID domain.MemberID) []byte {
	m := hmac.New(sha256.New, d.key)
	raw := uuid.UUID(memberID)
	m.Write(raw[:])
	return m.Sum(nil)
}
