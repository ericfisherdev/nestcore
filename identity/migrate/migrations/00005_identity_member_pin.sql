-- +goose Up
-- Per-member PIN credential (NES-165): a per-member PIN with enrolment,
-- argon2id storage, and strike-limited verification, gating task actions in
-- the follow-up ticket "require a member PIN to complete or skip a chore"
-- (which this migration only prepares for — nothing reads this table yet).
--
-- Purely additive, mirroring 00002_identity_mfa.sql's member_mfa shape
-- exactly: member_id is both the primary key and, via the composite FK
-- below, tenant-checked against household_id, so a query can verify the
-- PIN's member belongs to a given household without a second join. No
-- ALTER of any existing identity table, so an older Nestorage binary keeps
-- booting against the shared schema.
CREATE TABLE identity.member_pin (
    member_id    uuid        PRIMARY KEY,
    household_id uuid        NOT NULL REFERENCES identity.household (id) ON DELETE CASCADE,
    -- argon2id PHC-encoded hash; the raw PIN is never stored or logged.
    pin_hash     text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT member_pin_member_fk FOREIGN KEY (household_id, member_id)
        REFERENCES identity.member (household_id, id) ON DELETE CASCADE
);
-- Backs "which members in this household have a PIN enrolled" (settings'
-- member list, and the follow-up ticket's gate).
CREATE INDEX member_pin_household_idx ON identity.member_pin (household_id);

-- +goose Down
DROP TABLE IF EXISTS identity.member_pin;
