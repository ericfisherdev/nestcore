-- +goose Up
-- Server-side session store shared by Nestova and Nestorage (epic
-- NSTR-112) — matches the exact layout github.com/alexedwards/scs/pgxstore
-- requires. This is what gives the two apps real SSO: a session written by
-- one is readable by the other, since both point their session stores at
-- this one, schema-qualified table rather than each keeping their own.
--
-- IF NOT EXISTS: this set may run against a database where Nestorage's own
-- interim 00017_identity_schema.sql already created this exact table and
-- index (see 00001_identity_baseline.sql's header for the full adoption
-- rationale). Its shape matches this one exactly, so no reconciliation is
-- needed beyond the IF NOT EXISTS guards.
CREATE TABLE IF NOT EXISTS identity.sessions (
    token  text        PRIMARY KEY,
    data   bytea       NOT NULL,
    expiry timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS identity_sessions_expiry_idx ON identity.sessions (expiry);

-- +goose Down
DROP TABLE IF EXISTS identity.sessions;
