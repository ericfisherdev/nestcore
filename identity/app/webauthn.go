package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/ericfisherdev/nestcore/identity/domain"
)

// webauthnRegistrationResidentKey is the discoverable-credential
// preference BeginRegistration requests for every registration:
// "preferred" asks the authenticator to create a client-side
// discoverable (passkey) credential when it can, without hard-failing
// registration on an authenticator that cannot.
const webauthnRegistrationResidentKey = protocol.ResidentKeyRequirementPreferred

// defaultCredentialNickname is used when a member submits a blank
// nickname.
const defaultCredentialNickname = "Passkey"

// maxCredentialNicknameLen bounds the member-supplied device nickname
// (Rename and FinishRegistration): unbounded input would otherwise be
// stored and later rendered verbatim in a "Your devices" list.
const maxCredentialNicknameLen = 64

// normalizeNickname trims nickname, defaults it to
// defaultCredentialNickname when blank, and truncates it to
// maxCredentialNicknameLen runes.
func normalizeNickname(nickname string) string {
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		return defaultCredentialNickname
	}
	if runes := []rune(nickname); len(runes) > maxCredentialNicknameLen {
		return string(runes[:maxCredentialNicknameLen])
	}
	return nickname
}

// WebAuthnService orchestrates passkey registration, device management,
// and passkey login ceremonies. Login/step-up freshness policy (e.g.
// requiring a recent step-up before a security-sensitive action) is an
// app-level concern composed on top of this service, not implemented
// here — mirroring MFAService's own scope boundary.
type WebAuthnService struct {
	repo    domain.WebAuthnCredentialRepository
	wa      *webauthn.WebAuthn
	handles *WebAuthnUserHandleDeriver
	logger  *slog.Logger
}

// NewWebAuthnService constructs the service with injected dependencies.
// All four are required. wa is built once, in the caller's composition
// root, from that app's own public base URL/relying-party configuration
// — a *webauthn.WebAuthn instance holds only static Relying Party
// configuration and is safe to share across every request.
func NewWebAuthnService(repo domain.WebAuthnCredentialRepository, wa *webauthn.WebAuthn, handles *WebAuthnUserHandleDeriver, logger *slog.Logger) (*WebAuthnService, error) {
	if repo == nil {
		return nil, errors.New("identity: NewWebAuthnService requires a non-nil WebAuthnCredentialRepository")
	}
	if wa == nil {
		return nil, errors.New("identity: NewWebAuthnService requires a non-nil *webauthn.WebAuthn")
	}
	if handles == nil {
		return nil, errors.New("identity: NewWebAuthnService requires a non-nil WebAuthnUserHandleDeriver")
	}
	if logger == nil {
		return nil, errors.New("identity: NewWebAuthnService requires a non-nil logger")
	}
	return &WebAuthnService{repo: repo, wa: wa, handles: handles, logger: logger}, nil
}

// ListDevices returns memberID's registered credentials, oldest first,
// for a settings page's "Your devices" list.
func (s *WebAuthnService) ListDevices(ctx context.Context, memberID domain.MemberID) ([]domain.WebAuthnCredential, error) {
	creds, err := s.repo.ListByMember(ctx, memberID)
	if err != nil {
		return nil, fmt.Errorf("webauthn: list devices: %w", err)
	}
	return creds, nil
}

// BeginRegistration starts a new passkey registration ceremony for
// memberID, returning the credential creation options to send to the
// client (JSON, for the browser's navigator.credentials.create()) and
// the SessionData the caller MUST persist (e.g. under a dedicated
// session key) until FinishRegistration — and discard afterward
// regardless of outcome; a SessionData/challenge must never be usable
// twice.
//
// The registration excludes every credential memberID has already
// registered (WithExclusions), so a browser will not let the SAME
// physical authenticator be registered a second time as a confusing
// duplicate.
func (s *WebAuthnService) BeginRegistration(ctx context.Context, memberID domain.MemberID, displayName string) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	existing, err := s.repo.ListByMember(ctx, memberID)
	if err != nil {
		return nil, nil, fmt.Errorf("webauthn: begin registration: list existing credentials: %w", err)
	}
	user := s.newUser(memberID, displayName, existing)

	creation, session, err := s.wa.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(webauthnRegistrationResidentKey),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("webauthn: begin registration: %w", err)
	}
	return creation, session, nil
}

// FinishRegistration completes a passkey registration ceremony: verifies
// parsedResponse (the browser's attestation response, already parsed by
// the caller — see protocol.ParseCredentialCreationResponseBody, kept
// out of this layer so WebAuthnService never depends on net/http)
// against session (the value BeginRegistration returned), and — on
// success — persists a new credential row under nickname (defaulted to
// defaultCredentialNickname when blank).
//
// Returns domain.ErrWebAuthnVerificationFailed when verification fails
// for any reason (challenge mismatch, expired challenge, replayed
// challenge, RP ID/origin mismatch, signature invalid) — deliberately
// undifferentiated; see that sentinel's own doc.
func (s *WebAuthnService) FinishRegistration(ctx context.Context, memberID domain.MemberID, householdID domain.HouseholdID, displayName, nickname string, session webauthn.SessionData, parsedResponse *protocol.ParsedCredentialCreationData) error {
	existing, err := s.repo.ListByMember(ctx, memberID)
	if err != nil {
		return fmt.Errorf("webauthn: finish registration: list existing credentials: %w", err)
	}
	user := s.newUser(memberID, displayName, existing)

	cred, err := s.wa.CreateCredential(user, session, parsedResponse)
	if err != nil {
		// The returned sentinel is deliberately undifferentiated (see its
		// own doc), but the underlying cause is still worth a server-side
		// record: without it, an operator cannot tell an expired
		// challenge from an RP ID/origin misconfiguration.
		s.logger.DebugContext(ctx, "webauthn registration verification failed", "member_id", memberID.String(), "error", err)
		return fmt.Errorf("%w: %v", domain.ErrWebAuthnVerificationFailed, err)
	}

	nickname = normalizeNickname(nickname)
	stored := &domain.WebAuthnCredential{
		ID:           domain.NewWebAuthnCredentialID(),
		MemberID:     memberID,
		HouseholdID:  householdID,
		CredentialID: cred.ID,
		PublicKey:    cred.PublicKey,
		SignCount:    cred.Authenticator.SignCount,
		Transports:   transportsToStrings(cred.Transport),
		AAGUID:       aaguidToUUID(cred.Authenticator.AAGUID),
		Nickname:     nickname,
		UserHandle:   user.WebAuthnID(),
	}
	if err := s.repo.Create(ctx, householdID, stored); err != nil {
		return fmt.Errorf("webauthn: finish registration: store credential: %w", err)
	}
	s.logger.InfoContext(ctx, "webauthn credential registered", "member_id", memberID.String())
	return nil
}

// Rename updates the nickname on memberID's credential id.
func (s *WebAuthnService) Rename(ctx context.Context, householdID domain.HouseholdID, memberID domain.MemberID, id domain.WebAuthnCredentialID, nickname string) error {
	nickname = normalizeNickname(nickname)
	if err := s.repo.Rename(ctx, householdID, memberID, id, nickname); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "webauthn credential renamed", "member_id", memberID.String())
	return nil
}

// Revoke removes memberID's credential id immediately.
func (s *WebAuthnService) Revoke(ctx context.Context, householdID domain.HouseholdID, memberID domain.MemberID, id domain.WebAuthnCredentialID) error {
	if err := s.repo.Delete(ctx, householdID, memberID, id); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "webauthn credential revoked", "member_id", memberID.String())
	return nil
}

// BeginLogin starts a new usernameless ("Sign in with passkey") login
// ceremony: an empty allowCredentials list, so the browser offers any of
// its discoverable credentials registered for this Relying Party rather
// than a specific member's — the server does not yet know who is
// authenticating. The caller MUST persist the returned SessionData
// until FinishLogin, and discard it afterward regardless of outcome,
// mirroring BeginRegistration's single-use challenge contract.
func (s *WebAuthnService) BeginLogin(_ context.Context) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	assertion, session, err := s.wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, nil, fmt.Errorf("webauthn: begin login: %w", err)
	}
	return assertion, session, nil
}

// FinishLogin completes a usernameless login ceremony: verifies
// parsedResponse against session, resolving WHICH member is
// authenticating from the assertion's own reported user handle via
// s.repo.FindByUserHandle — the server has no other way to know, since
// this is a usernameless ceremony. On success it returns the resolved
// member id and applies the same post-assertion bookkeeping as any
// other assertion (see applyAssertionResult).
//
// Returns domain.ErrWebAuthnVerificationFailed when verification fails
// for any reason (challenge mismatch, expired challenge, wrong RP ID/
// origin, unresolvable or unknown user handle, signature invalid) —
// deliberately undifferentiated, mirroring FinishRegistration's own
// no-oracle convention.
func (s *WebAuthnService) FinishLogin(ctx context.Context, session webauthn.SessionData, parsedResponse *protocol.ParsedCredentialAssertionData) (domain.MemberID, error) {
	var (
		resolvedMemberID  domain.MemberID
		storedCredentials []domain.WebAuthnCredential
	)
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		memberID, creds, err := s.repo.FindByUserHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		resolvedMemberID = memberID
		storedCredentials = creds
		return s.newUser(memberID, "", creds), nil
	}

	if _, _, err := s.wa.ValidatePasskeyLogin(handler, session, parsedResponse); err != nil {
		// See FinishRegistration's identical logging: the returned
		// sentinel stays undifferentiated, but the underlying cause
		// (which may be an infrastructure failure from the handler
		// above, not merely an unknown handle) is still worth a
		// server-side record.
		s.logger.DebugContext(ctx, "webauthn login verification failed", "error", err)
		return domain.MemberID{}, fmt.Errorf("%w: %v", domain.ErrWebAuthnVerificationFailed, err)
	}

	if err := s.applyAssertionResult(ctx, resolvedMemberID, storedCredentials, parsedResponse); err != nil {
		return domain.MemberID{}, err
	}
	s.logger.InfoContext(ctx, "webauthn login verified", "member_id", resolvedMemberID.String())
	return resolvedMemberID, nil
}

// signCountSuspicious reports whether newCount — the authenticator's
// signature counter as freshly reported in an assertion — signals a
// possible cloned authenticator, given storedCount, the value on file
// before this assertion.
//
// Suspicious ONLY when newCount is nonzero AND less than storedCount. A
// synced passkey (iCloud Keychain, Google Password Manager) permanently
// reports a counter of 0 — by design, since a synced credential has no
// single physical counter to track — so newCount == 0 must never
// false-positive regardless of storedCount.
func signCountSuspicious(newCount, storedCount uint32) bool {
	return newCount != 0 && newCount < storedCount
}

// applyAssertionResult persists the authenticator's new signature
// counter and last-used timestamp for the asserted credential
// (identified within credentials by matching parsedResponse.RawID) and,
// when signCountSuspicious flags the new count, logs a warning — a
// caller that wants to notify the member (an app-level concern; see the
// package doc's scope boundary) can layer that on the login/step-up
// call site itself.
func (s *WebAuthnService) applyAssertionResult(ctx context.Context, memberID domain.MemberID, credentials []domain.WebAuthnCredential, parsedResponse *protocol.ParsedCredentialAssertionData) error {
	newCount := parsedResponse.Response.AuthenticatorData.Counter

	var (
		storedCount uint32
		found       bool
	)
	for _, c := range credentials {
		if bytes.Equal(c.CredentialID, parsedResponse.RawID) {
			storedCount, found = c.SignCount, true
			break
		}
	}
	if !found {
		// After a successful ValidatePasskeyLogin/ValidateLogin, the
		// asserted credential should always be among the member's own —
		// a miss here means credentials was stale or mismatched with
		// parsedResponse.RawID, a defect worth a server-side record
		// rather than a silently-disabled clone check (storedCount's
		// zero value would otherwise make signCountSuspicious never
		// flag, indistinguishable from a genuinely first-ever
		// assertion).
		s.logger.WarnContext(ctx, "webauthn: asserted credential missing from the member's stored set", "member_id", memberID.String())
	}

	if err := s.repo.UpdateAfterAssertion(ctx, parsedResponse.RawID, newCount, time.Now()); err != nil {
		return fmt.Errorf("webauthn: update sign count: %w", err)
	}

	if signCountSuspicious(newCount, storedCount) {
		s.logger.WarnContext(ctx, "webauthn: passkey sign count decreased unexpectedly", "member_id", memberID.String())
	}
	return nil
}

// newUser builds the webauthn.User adapter for memberID, deriving its
// stable handle via s.handles and carrying existing so
// WebAuthnCredentials() (used for both the exclude-list at registration
// and CreateCredential's internal verification) reflects the member's
// full, current credential set.
func (s *WebAuthnService) newUser(memberID domain.MemberID, displayName string, existing []domain.WebAuthnCredential) *webAuthnUser {
	return &webAuthnUser{
		id:          s.handles.Derive(memberID),
		displayName: displayName,
		credentials: existing,
	}
}

// webAuthnUser adapts a domain.MemberID (identified only by its derived
// handle and display name — no other member fields are needed by any
// webauthn.User method) plus its existing credentials to satisfy
// webauthn.User.
type webAuthnUser struct {
	id          []byte
	displayName string
	credentials []domain.WebAuthnCredential
}

// WebAuthnID returns the member's stable, HMAC-derived handle — never
// the raw member UUID.
func (u *webAuthnUser) WebAuthnID() []byte { return u.id }

// WebAuthnName and WebAuthnDisplayName both return the member's display
// name: members are not guaranteed to have an email, so there is no
// separate "name" vs. "display name" distinction to make here.
func (u *webAuthnUser) WebAuthnName() string        { return u.displayName }
func (u *webAuthnUser) WebAuthnDisplayName() string { return u.displayName }

// WebAuthnCredentials converts the member's stored credentials into the
// shape the go-webauthn library operates on.
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	creds := make([]webauthn.Credential, 0, len(u.credentials))
	for _, c := range u.credentials {
		creds = append(creds, toWebAuthnCredential(c))
	}
	return creds
}

// toWebAuthnCredential converts a stored domain.WebAuthnCredential back
// into the go-webauthn library's own Credential shape — the inverse of
// the field mapping FinishRegistration performs when persisting one.
func toWebAuthnCredential(c domain.WebAuthnCredential) webauthn.Credential {
	wc := webauthn.Credential{
		ID:        c.CredentialID,
		PublicKey: c.PublicKey,
		Authenticator: webauthn.Authenticator{
			SignCount: c.SignCount,
		},
	}
	if len(c.Transports) > 0 {
		wc.Transport = make([]protocol.AuthenticatorTransport, 0, len(c.Transports))
		for _, t := range c.Transports {
			wc.Transport = append(wc.Transport, protocol.AuthenticatorTransport(t))
		}
	}
	if c.AAGUID != nil {
		if raw, err := c.AAGUID.MarshalBinary(); err == nil {
			wc.Authenticator.AAGUID = raw
		}
	}
	return wc
}

// transportsToStrings converts the go-webauthn library's transport hint
// type to the plain strings member_credential.transports stores.
func transportsToStrings(transports []protocol.AuthenticatorTransport) []string {
	if len(transports) == 0 {
		return nil
	}
	out := make([]string, 0, len(transports))
	for _, t := range transports {
		out = append(out, string(t))
	}
	return out
}

// aaguidToUUID converts the go-webauthn library's raw AAGUID bytes to a
// *uuid.UUID, returning nil both when the authenticator reported no
// AAGUID at all (a length other than 16) and when it reported the
// all-zero AAGUID (uuid.Nil) some authenticator models use to mean the
// same thing — both are "unknown model", not two different states worth
// distinguishing in storage.
func aaguidToUUID(raw []byte) *uuid.UUID {
	if len(raw) != 16 {
		return nil
	}
	id, err := uuid.FromBytes(raw)
	if err != nil || id == uuid.Nil {
		return nil
	}
	return &id
}
