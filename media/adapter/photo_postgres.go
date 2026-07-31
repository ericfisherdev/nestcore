package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"
	identity "github.com/ericfisherdev/nestcore/identity/domain"
	"github.com/ericfisherdev/nestcore/media/domain"
)

// PhotoRepository is the pgx-backed domain.PhotoRepository. Every query
// names the "photo" table UNQUALIFIED, resolved through the caller's own
// connection search_path — see the package doc for the canonical table
// shape this requires each consumer's own migration to provide. UUIDs are
// passed and scanned as text, matching nestcore/identity's adapter
// convention (no pgx UUID codec registration).
type PhotoRepository struct {
	dbtx db.TX
	// backend is the StorageBackend Create stamps onto every row it
	// writes — see NewPhotoRepository's doc.
	backend domain.StorageBackend
}

var _ domain.PhotoRepository = (*PhotoRepository)(nil)

// NewPhotoRepository constructs the repository with an injected query
// executor, bound to backend — the SAME domain.StorageBackend the
// composition root selected for the running domain.PhotoStore: Create
// writes backend into every row's storage_backend column itself, never
// relying on the column's DEFAULT, so the column always reflects which
// backend genuinely wrote the bytes. Panics on a nil dbtx or an invalid
// backend.
func NewPhotoRepository(dbtx db.TX, backend domain.StorageBackend) *PhotoRepository {
	if dbtx == nil {
		panic("media/adapter: NewPhotoRepository requires a non-nil db.TX")
	}
	if !backend.Valid() {
		panic(fmt.Sprintf("media/adapter: NewPhotoRepository requires a valid StorageBackend, got %q", backend))
	}
	return &PhotoRepository{dbtx: dbtx, backend: backend}
}

const photoColumns = `SELECT id, household_id, storage_ref, storage_backend, content_sha256, size_bytes, content_type, taken_at, uploaded_by, created_at FROM photo`

// Create inserts a photo and populates its created_at, mapping an unknown
// household to identity.ErrHouseholdNotFound, an unknown uploader to
// identity.ErrMemberNotFound, and a content hash that collides with another
// household photo to domain.ErrDuplicatePhoto.
//
// storage_backend is written from r.backend (the repository's own
// configured backend — see NewPhotoRepository's doc), NOT from
// photo.StorageBackend: which backend actually wrote the bytes is a fact
// this repository instance already knows, not something the caller
// supplies, so Create also stamps the value back onto photo.StorageBackend
// on success (mirroring how it populates photo.CreatedAt).
func (r *PhotoRepository) Create(ctx context.Context, photo *domain.Photo) error {
	if photo == nil {
		return errors.New("media/adapter: create photo: nil photo")
	}
	const q = `
		INSERT INTO photo (id, household_id, storage_ref, storage_backend, content_sha256, size_bytes, content_type, taken_at, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at`
	err := r.dbtx.QueryRow(ctx, q,
		photo.ID.String(), photo.HouseholdID.String(), photo.StorageRef.String(), r.backend.String(),
		nullableText(photo.ContentHash), photo.SizeBytes, photo.ContentType,
		photo.TakenAt, memberArg(photo.UploadedBy),
	).Scan(&photo.CreatedAt)
	if err != nil {
		if isUniqueViolation(err, photoHouseholdContentHashUniq) {
			return domain.ErrDuplicatePhoto
		}
		if mapped := mapFKViolation(err); mapped != nil {
			return mapped
		}
		return fmt.Errorf("create photo: %w", err)
	}
	photo.StorageBackend = r.backend
	return nil
}

// Get returns the photo, or domain.ErrPhotoNotFound.
func (r *PhotoRepository) Get(ctx context.Context, id domain.PhotoID) (*domain.Photo, error) {
	photo, err := scanPhoto(r.dbtx.QueryRow(ctx, photoColumns+` WHERE id = $1`, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPhotoNotFound
		}
		return nil, fmt.Errorf("get photo: %w", err)
	}
	return photo, nil
}

// FindByContentHash returns the household's photo carrying the given
// content hash, or domain.ErrPhotoNotFound when none matches — the expected
// "not a duplicate" outcome for a genuinely new upload, not an exceptional
// one. hash must be non-blank; a blank hash can never match (a stored
// content_sha256 is always a 64-character lowercase hex sha256), so this
// short-circuits to ErrPhotoNotFound rather than issuing a query.
func (r *PhotoRepository) FindByContentHash(ctx context.Context, householdID identity.HouseholdID, hash string) (*domain.Photo, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, domain.ErrPhotoNotFound
	}
	photo, err := scanPhoto(r.dbtx.QueryRow(ctx, photoColumns+` WHERE household_id = $1 AND content_sha256 = $2`, householdID.String(), hash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrPhotoNotFound
		}
		return nil, fmt.Errorf("find photo by content hash: %w", err)
	}
	return photo, nil
}

// ListByHousehold returns the household's photos ordered by creation time
// (id as the tiebreaker for rows sharing an identical created_at), or an
// empty slice when none exist.
func (r *PhotoRepository) ListByHousehold(ctx context.Context, householdID identity.HouseholdID) ([]*domain.Photo, error) {
	rows, err := r.dbtx.Query(ctx, photoColumns+` WHERE household_id = $1 ORDER BY created_at, id`, householdID.String())
	if err != nil {
		return nil, fmt.Errorf("list photos: %w", err)
	}
	defer rows.Close()
	return scanPhotos(rows)
}

// Delete removes the photo, returning domain.ErrPhotoNotFound when the id is
// unknown.
func (r *PhotoRepository) Delete(ctx context.Context, id domain.PhotoID) error {
	tag, err := r.dbtx.Exec(ctx, `DELETE FROM photo WHERE id = $1`, id.String())
	if err != nil {
		return fmt.Errorf("delete photo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrPhotoNotFound
	}
	return nil
}

// ListAllStorageRefs returns the StorageRef of every photo row stamped with
// backend, across every household, or an empty slice when there are none.
func (r *PhotoRepository) ListAllStorageRefs(ctx context.Context, backend domain.StorageBackend) ([]domain.StorageRef, error) {
	rows, err := r.dbtx.Query(ctx, `SELECT storage_ref FROM photo WHERE storage_backend = $1`, backend.String())
	if err != nil {
		return nil, fmt.Errorf("list all photo storage refs: %w", err)
	}
	defer rows.Close()
	refs := make([]domain.StorageRef, 0)
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("scan photo storage ref: %w", err)
		}
		refs = append(refs, domain.StorageRef(ref))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan photo storage refs: %w", err)
	}
	return refs, nil
}

// ExistsByStorageRef reports whether any photo row STAMPED WITH backend
// currently references ref.
func (r *PhotoRepository) ExistsByStorageRef(ctx context.Context, ref domain.StorageRef, backend domain.StorageBackend) (bool, error) {
	var exists bool
	if err := r.dbtx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM photo WHERE storage_ref = $1 AND storage_backend = $2)`, ref.String(), backend.String()).Scan(&exists); err != nil {
		return false, fmt.Errorf("check photo storage ref exists: %w", err)
	}
	return exists, nil
}

// ListByBackend returns up to limit photo rows stamped with backend,
// ordered by id ascending, whose id is strictly greater than afterID.
// Returns an error for a non-positive limit rather than issuing a query
// LIMIT 0 (silently empty) or a negative LIMIT (a Postgres error the
// caller would otherwise have to decode).
func (r *PhotoRepository) ListByBackend(ctx context.Context, backend domain.StorageBackend, afterID domain.PhotoID, limit int) ([]*domain.Photo, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("media/adapter: list photos by backend: limit must be positive, got %d", limit)
	}
	rows, err := r.dbtx.Query(ctx, photoColumns+` WHERE storage_backend = $1 AND id > $2 ORDER BY id LIMIT $3`,
		backend.String(), afterID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("list photos by backend: %w", err)
	}
	defer rows.Close()
	return scanPhotos(rows)
}

// MigrateStorageBackend flips a local-backend photo row onto newBackend,
// writing newRef as its new storage_ref and, ONLY when the row's
// content_sha256 is currently NULL, backfilling it with contentHash.
func (r *PhotoRepository) MigrateStorageBackend(ctx context.Context, id domain.PhotoID, newRef domain.StorageRef, newBackend domain.StorageBackend, contentHash string) (bool, error) {
	const q = `
		UPDATE photo
		   SET storage_ref = $2, storage_backend = $3, content_sha256 = COALESCE(content_sha256, $4)
		 WHERE id = $1 AND storage_backend = $5`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), newRef.String(), newBackend.String(), nullableText(contentHash), domain.StorageBackendLocal.String())
	if err != nil {
		return false, fmt.Errorf("migrate photo storage backend: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// memberArg renders an optional member id as a nullable text query
// argument.
func memberArg(id *identity.MemberID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func scanPhotos(rows pgx.Rows) ([]*domain.Photo, error) {
	photos := make([]*domain.Photo, 0)
	for rows.Next() {
		photo, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("scan photo: %w", err)
		}
		photos = append(photos, photo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan photos: %w", err)
	}
	return photos, nil
}

func scanPhoto(r row) (*domain.Photo, error) {
	var (
		photo          domain.Photo
		idStr, hhStr   string
		storageRef     string
		storageBackend string
		contentHashPtr *string
		takenAt        *time.Time
		uploadedByStr  *string
	)
	if err := r.Scan(&idStr, &hhStr, &storageRef, &storageBackend, &contentHashPtr, &photo.SizeBytes, &photo.ContentType, &takenAt, &uploadedByStr, &photo.CreatedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParsePhotoID(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse photo id: %w", err)
	}
	hh, err := identity.ParseHouseholdID(hhStr)
	if err != nil {
		return nil, fmt.Errorf("parse household id: %w", err)
	}
	backend, err := domain.ParseStorageBackend(storageBackend)
	if err != nil {
		return nil, fmt.Errorf("parse storage backend: %w", err)
	}
	photo.ID = id
	photo.HouseholdID = hh
	photo.StorageRef = domain.StorageRef(storageRef)
	photo.StorageBackend = backend
	if contentHashPtr != nil {
		photo.ContentHash = *contentHashPtr
	}
	photo.TakenAt = takenAt
	if uploadedByStr != nil {
		memberID, err := identity.ParseMemberID(*uploadedByStr)
		if err != nil {
			return nil, fmt.Errorf("parse uploader id: %w", err)
		}
		photo.UploadedBy = &memberID
	}
	return &photo, nil
}
