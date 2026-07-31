// Package adapter contains the media bounded context's outbound adapters:
// PhotoRepository (Postgres), LocalPhotoStore and S3PhotoStore (the two
// domain.PhotoStore implementations), StoreResolver, and the shared
// upload-validation/EXIF helpers every PhotoStore backend uses.
//
// # The canonical "photo" table shape
//
// PhotoRepository's SQL is schema-parameterized the same way Nestorage's own
// consolidation (NSTR-119) parameterizes every app-owned table: every
// identifier is UNQUALIFIED — "photo", never "<app>.photo" — and resolved
// through the caller's own connection search_path (set via nestcore/db's
// WithSearchPath). This is the opposite convention identity/adapter uses
// (which schema-qualifies "identity." explicitly, because the identity
// schema's NAME is fixed and known at compile time); a "photo" table's
// schema is NOT known here, since it lives in whichever application's own
// schema is calling — Nestova's, Nestorage's, or any future consumer's.
//
// Every consumer therefore owns and migrates its OWN "photo" table, built to
// this EXACT shape (column names, types, and constraint names), so
// PhotoRepository's queries and constraint-violation mapping work
// identically regardless of which schema they run against:
//
//	CREATE TABLE photo (
//	    id               uuid        PRIMARY KEY,
//	    household_id     uuid        NOT NULL REFERENCES identity.household(id),
//	    storage_ref      text        NOT NULL,
//	    storage_backend  text        NOT NULL,
//	    content_sha256   text,
//	    size_bytes       bigint      NOT NULL,
//	    content_type     text        NOT NULL,
//	    taken_at         timestamptz,
//	    uploaded_by      uuid        REFERENCES identity.member(id),
//	    created_at       timestamptz NOT NULL DEFAULT now()
//	);
//	CREATE UNIQUE INDEX photo_household_content_hash_uniq
//	    ON photo (household_id, content_sha256)
//	    WHERE content_sha256 IS NOT NULL;
//	CREATE INDEX photo_household_id_created_at_idx ON photo (household_id, created_at);
//	CREATE INDEX photo_storage_backend_id_idx ON photo (storage_backend, id);
//
// The two plain (non-unique) indexes above match this package's own query
// shapes — ListByHousehold's household_id-filtered, created_at-ordered scan
// and ListByBackend/ListAllStorageRefs' storage_backend-filtered,
// id-ordered scan — so every consumer's migration should include them,
// though PhotoRepository has no way to enforce that the way it enforces the
// unique index (a missing plain index only costs query performance, never
// correctness).
//
// The household_id and uploaded_by foreign keys must be declared inline
// (unnamed) so Postgres auto-names them photo_household_id_fkey and
// photo_uploaded_by_fkey — the names PhotoRepository's constraint mapping
// matches against (see constraints.go). content_sha256 is nullable (a
// legacy photo predating content-hash dedup never matches a duplicate
// check); the partial unique index's WHERE clause is what makes that safe.
// An application is free to add its OWN extra columns (e.g. a
// presentation-layer caption or an app-specific foreign key) beyond this
// shape — PhotoRepository's queries name every column explicitly and never
// SELECT *, so additional columns never interfere.
//
// # Compatibility
//
// PhotoStore and PhotoRepository (media/domain) are shared API consumed by
// independently versioned binaries — additive-only, mirroring identity's
// own rule (see identity/migrate's package doc). A new capability arrives
// as a new, narrowly-scoped interface (ObjectLister, ObjectExister,
// RawObjectWriter) a caller type-asserts for, never as a widened method set
// on PhotoStore or PhotoRepository themselves. The canonical table shape
// above is likewise additive-only from this package's side: a future
// column this package starts reading would be a breaking change for every
// consumer's existing migration, so any such change ships as a new,
// separately-typed capability instead.
package adapter
