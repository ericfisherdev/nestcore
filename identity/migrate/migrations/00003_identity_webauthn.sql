-- +goose Up
-- WebAuthn passkey registrations (ports Nestova's member_credential,
-- 00034_member_webauthn_credential.sql, into identity).
--
-- member_credential is a 1:many extension of identity.member (a member may
-- register multiple passkeys — phone, laptop, security key), mirroring
-- member_mfa's tenant-isolation pattern: household_id lets
-- member_credential_member_fk verify the member actually belongs to that
-- household without a second join at query time.
CREATE TABLE identity.member_credential (
    id            uuid        PRIMARY KEY,
    household_id  uuid        NOT NULL REFERENCES identity.household (id) ON DELETE CASCADE,
    member_id     uuid        NOT NULL,
    -- The WebAuthn credential id the authenticator itself generates,
    -- globally unique by construction; the UNIQUE constraint here is
    -- defense-in-depth, not the primary replay guard.
    credential_id bytea       NOT NULL UNIQUE,
    -- CBOR-encoded public key. Unlike member_mfa's totp_secret_enc, this is
    -- NOT encrypted at rest: a public key is not a secret.
    public_key    bytea       NOT NULL,
    -- The authenticator's signature counter as of the last successful
    -- ceremony (0 at registration, never NULL).
    sign_count    bigint      NOT NULL DEFAULT 0,
    -- Transport hints reported at registration — advisory only.
    transports    text[],
    -- The authenticator model's AAGUID, when reported.
    aaguid        uuid,
    -- Member-chosen label so a member with several passkeys can tell them
    -- apart when revoking one.
    nickname      text        NOT NULL,
    -- The HMAC-derived WebAuthn user handle, stored redundantly per-row (not
    -- normalized onto member) so a usernameless login lookup is a single
    -- indexed equality query against this table alone.
    user_handle   bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    CONSTRAINT member_credential_member_fk FOREIGN KEY (household_id, member_id)
        REFERENCES identity.member (household_id, id) ON DELETE CASCADE
);
-- Backs a settings page's "Your devices" list and rename/revoke's
-- tenant-scoped lookups.
CREATE INDEX member_credential_member_idx ON identity.member_credential (member_id);
-- Backs a usernameless login lookup: the authenticator returns a user
-- handle (not a member id) during a discoverable-credential login, and that
-- handle must resolve back to a member via a single indexed equality query.
CREATE INDEX member_credential_user_handle_idx ON identity.member_credential (user_handle);

-- +goose Down
DROP TABLE IF EXISTS identity.member_credential;
