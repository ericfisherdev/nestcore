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
//	    household_id     uuid        NOT NULL REFERENCES identity.household(id) ON DELETE CASCADE,
//	    storage_ref      text        NOT NULL,
//	    storage_backend  text        NOT NULL
//	        CONSTRAINT photo_storage_backend_check CHECK (storage_backend IN ('local', 's3')),
//	    content_sha256   text
//	        CONSTRAINT photo_content_sha256_format
//	        CHECK (content_sha256 IS NULL OR content_sha256 ~ '^[0-9a-f]{64}$'),
//	    size_bytes       bigint      NOT NULL,
//	    content_type     text        NOT NULL,
//	    taken_at         timestamptz,
//	    uploaded_by      uuid,
//	    created_at       timestamptz NOT NULL DEFAULT now(),
//	    -- Tenant consistency: an uploader must belong to the photo's OWN
//	    -- household, never another one identity itself would happily
//	    -- allow a bare uploaded_by REFERENCES identity.member(id) to miss.
//	    -- Targets identity.member's member_household_id_id_uniq (see that
//	    -- migration's own doc for the composite-FK pattern this mirrors);
//	    -- MATCH SIMPLE (Postgres's default) skips the check entirely when
//	    -- uploaded_by IS NULL, so an anonymous/system upload is unaffected.
//	    CONSTRAINT photo_uploaded_by_fkey FOREIGN KEY (household_id, uploaded_by)
//	        REFERENCES identity.member (household_id, id) ON DELETE SET NULL (uploaded_by)
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
// household_id's foreign key must be declared inline (unnamed) so Postgres
// auto-names it photo_household_id_fkey — the name PhotoRepository's
// constraint mapping matches against (see constraints.go) — and cascades
// deletes, mirroring identity.member's own cascade from identity.household
// (a household delete must not be blocked by surviving photo rows).
// uploaded_by's foreign key, by contrast, is EXPLICITLY named
// photo_uploaded_by_fkey (constraints.go matches the same name either way)
// because it must be the composite (household_id, uploaded_by) form above,
// not a plain single-column reference — Postgres does not auto-name a
// multi-column FK usefully, so this one has to be spelled out. It also sets
// NULL only, not CASCADE, on delete: Photo.UploadedBy is documented as
// "nilled (not deleted) if that member is removed so the photo survives",
// and ON DELETE SET NULL (uploaded_by) is what makes that true — a plain
// CASCADE on this column would delete the photo along with its uploader,
// which is exactly the outcome that doc line rules out.
//
// storage_backend's CHECK restricts it to domain.StorageBackend's own
// known values, and content_sha256's CHECK enforces the same 64-character
// lowercase-hex-sha256 shape domain.Photo.Validate and
// PhotoRepository.MigrateStorageBackend's own argument validation expect —
// both mirror Nestova's original photo_storage_backend_check and
// photo_content_sha256_format constraints, so a row that violates either
// can only originate from a bypass of this package's own write paths, not
// from an ordinarily-configured deployment. content_sha256 is nullable (a
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
