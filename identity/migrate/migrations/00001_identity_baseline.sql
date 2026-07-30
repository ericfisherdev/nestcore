-- +goose Up
-- Identity baseline (NSTR-120): household and member, the schema Nestova
-- and Nestorage both depend on for one shared login (epic NSTR-112).
--
-- Schema-qualified throughout — this migration set runs under EACH app's
-- own search_path, never relying on it to resolve `identity`. The
-- CREATE SCHEMA below is redundant with the Runner's own WithEnsureSchema
-- (identity/migrate.New), which already guarantees the schema exists before
-- goose's Provider is built; it stays here too so this migration set is
-- self-sufficient run standalone (e.g. a bare goose invocation, or the
-- gated tests in this package), not only when constructed through New.
--
-- Whichever role runs this first OWNS the identity schema. No GRANTs are
-- issued here, so both apps must connect as the same role (see
-- identity/migrate's package doc, Migration ownership).
--
-- ADOPTING NESTORAGE'S INTERIM SCHEMA: this set may run against a database
-- where Nestorage's own internal/platform/db/migrate/migrations/00017_identity_schema.sql
-- (merged, on Nestorage's main) already created identity.household,
-- identity.member, identity.sessions and identity_sessions_expiry_idx —
-- guarded with IF NOT EXISTS on purpose, per that migration's own header,
-- specifically so this set could adopt it without conflict. Every CREATE
-- below is therefore IF NOT EXISTS too, and identity.member is reconciled
-- immediately after in case it was Nestorage's shape rather than this
-- migration's own: Nestorage's interim member has email/password_hash as
-- NOT NULL with no member_credentials_complete CHECK, and neither
-- member_household_name_uniq nor member_household_id_id_uniq — both of
-- which 00002's member_mfa_member_fk and 00003's member_credential_member_fk
-- require as composite-FK targets. The ALTER/DROP+ADD statements below are
-- no-ops when this migration's own CREATE TABLE ran instead.
CREATE SCHEMA IF NOT EXISTS identity;

-- pgcrypto (gen_random_uuid, below) and citext (member.email) are installed
-- into `public`, schema-qualified explicitly rather than left to
-- search_path resolution. The unqualified uses of gen_random_uuid() and
-- citext further down rely on `public` being in every app's default
-- search_path — the same assumption Nestova's and Nestorage's own
-- migrations already make.
CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public;
CREATE EXTENSION IF NOT EXISTS citext SCHEMA public;

CREATE TABLE IF NOT EXISTS identity.household (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- role is the unified owner/adult/child vocabulary (NSTR-112) and is
-- Nestova's and only Nestova's: apps derive their own admin-vs-member
-- behavior from these values and never the reverse — an admin/member
-- vocabulary would flatten child into adult and lose that distinction
-- permanently. Apps own no roles of their own.
--
-- email and password_hash are nullable, paired with the
-- member_credentials_complete CHECK below: a household can contain members
-- (children) who cannot log in.
--
-- active is the IDENTITY-level deactivation switch (decided 2026-07-29):
-- deactivating a member cuts access to BOTH apps at once — session
-- validation, login, and Nestorage's device-token and API-key resolution
-- all read this flag. A member row is never deleted (many app migrations
-- FK-reference member — tasks, gamification, chore trades, tracking,
-- calendar, MFA, WebAuthn credentials, notification preferences, and
-- Nestorage's storage tables), so removal is modeled as deactivation only;
-- every piece of history keeps its attribution. See identity/migrate's
-- package doc for the full deactivation-not-deletion and
-- app-side-presentation policies this table falls under. The operations and
-- guards that consume this flag ship in NSTR-111, not here.
CREATE TABLE IF NOT EXISTS identity.member (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id  uuid        NOT NULL REFERENCES identity.household (id) ON DELETE CASCADE,
    display_name  text        NOT NULL,
    role          text        NOT NULL CHECK (role IN ('owner', 'adult', 'child')),
    email         citext,
    password_hash text,
    active        boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- Named explicitly (not an auto-generated name) so an adapter can map
    -- violations of this exact constraint to a domain error instead of
    -- parsing an error message.
    CONSTRAINT member_email_unique UNIQUE (email),
    CONSTRAINT member_credentials_complete CHECK (
        (email IS NULL AND password_hash IS NULL)
        OR (email IS NOT NULL AND password_hash IS NOT NULL)
    )
);

-- Reconcile an identity.member adopted from Nestorage's interim
-- 00017_identity_schema.sql, whose shape predates this set: credentials are
-- NOT NULL there and it carries neither uniqueness index below. Every
-- statement here no-ops on the freshly created table above.
ALTER TABLE identity.member
    ALTER COLUMN email         DROP NOT NULL,
    ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE identity.member
    DROP CONSTRAINT IF EXISTS member_credentials_complete;
ALTER TABLE identity.member
    ADD CONSTRAINT member_credentials_complete CHECK (
        (email IS NULL AND password_hash IS NULL)
        OR (email IS NOT NULL AND password_hash IS NOT NULL)
    );

-- Member display names are unique (case-insensitively) within a household.
CREATE UNIQUE INDEX IF NOT EXISTS member_household_name_uniq ON identity.member (household_id, lower(display_name));
-- Composite-FK target for tenant consistency: every table below that
-- references a member alongside its household (member_mfa,
-- member_credential, ...) checks the member actually belongs to that
-- household through this.
CREATE UNIQUE INDEX IF NOT EXISTS member_household_id_id_uniq ON identity.member (household_id, id);

-- +goose Down
DROP TABLE IF EXISTS identity.member;
DROP TABLE IF EXISTS identity.household;

-- Intentionally NOT dropping the identity schema itself: goose's own
-- version table (identity.goose_db_version, configured via
-- identity/migrate.New's WithVersionTable) lives inside it and is managed
-- by goose independently of any migration's Up/Down — goose never drops its
-- own version table, so a DROP SCHEMA here would always fail once that
-- table exists. Nor are the pgcrypto/citext extensions dropped: other
-- objects may come to depend on them, and dropping extensions needs
-- elevated privileges in many hosted environments (matching Nestova's and
-- Nestorage's own migrations).
