-- +goose Up
-- TOTP MFA (ports Nestova's member_mfa/member_recovery_code — its own
-- 00031_member_mfa.sql plus 00033_member_mfa_login.sql — into identity).
--
-- member_mfa is a 1:1 extension of identity.member: member_id is both the
-- primary key and, via the composite FK below, tenant-checked against
-- household_id, so a query can verify the enrollment's member belongs to a
-- given household without a second join.
--
-- last_totp_step is the RFC 6238 step (floor(unix_time / 30s)) of the most
-- recently ACCEPTED login TOTP code, or NULL if the member has never
-- completed login MFA verification — folded in here from what was a later
-- migration in Nestova (00033) rather than shipped as a second one, since
-- this is a fresh baseline with no existing rows to backfill.
CREATE TABLE identity.member_mfa (
    member_id       uuid        PRIMARY KEY,
    household_id    uuid        NOT NULL REFERENCES identity.household (id) ON DELETE CASCADE,
    -- AES-256-GCM ciphertext; never stored or logged in plaintext.
    totp_secret_enc bytea       NOT NULL CHECK (length(totp_secret_enc) > 0),
    confirmed_at    timestamptz,
    last_totp_step  bigint,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT member_mfa_member_fk FOREIGN KEY (household_id, member_id)
        REFERENCES identity.member (household_id, id) ON DELETE CASCADE
);
-- Backs an owner's admin reset flow, which needs to find/verify a target
-- member's enrollment scoped to the acting owner's own household.
CREATE INDEX member_mfa_household_idx ON identity.member_mfa (household_id);

-- Ten single-use recovery codes, generated once immediately after
-- confirmation and displayed exactly once; only the hash is persisted.
-- Regenerating replaces the full set atomically; disenrolling or an owner
-- reset removes the member_mfa row, cascading here.
CREATE TABLE identity.member_recovery_code (
    id         uuid        PRIMARY KEY,
    member_id  uuid        NOT NULL REFERENCES identity.member_mfa (member_id) ON DELETE CASCADE,
    code_hash  text        NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- Serves both "verify a submitted code against a member's unused set" and
-- "delete a member's full set to regenerate".
CREATE INDEX member_recovery_code_member_idx ON identity.member_recovery_code (member_id);

-- +goose Down
-- Drop in reverse dependency order so the FK is not violated.
DROP TABLE IF EXISTS identity.member_recovery_code;
DROP TABLE IF EXISTS identity.member_mfa;
