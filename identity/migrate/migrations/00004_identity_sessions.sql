-- +goose Up
-- Server-side session store shared by Nestova and Nestorage (epic
-- NSTR-112) — matches the exact layout github.com/alexedwards/scs/pgxstore
-- requires. This is what gives the two apps real SSO: a session written by
-- one is readable by the other, since both point their session stores at
-- this one, schema-qualified table rather than each keeping their own.
CREATE TABLE identity.sessions (
    token  text        PRIMARY KEY,
    data   bytea       NOT NULL,
    expiry timestamptz NOT NULL
);
CREATE INDEX identity_sessions_expiry_idx ON identity.sessions (expiry);

-- +goose Down
DROP TABLE IF EXISTS identity.sessions;
