package domain

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	identity "github.com/ericfisherdev/nestcore/identity/domain"
)

// Accepted image content types — the upload accept-list. Both Photo.Validate
// and the adapter's storage-extension mapping key off these constants, so
// there is a single source of truth for "what image type does the upload
// path accept."
const (
	ContentTypeJPEG = "image/jpeg"
	ContentTypePNG  = "image/png"
	ContentTypeWebP = "image/webp"
)

// acceptedContentTypes is the set Photo.Validate checks ContentType against.
var acceptedContentTypes = map[string]struct{}{
	ContentTypeJPEG: {},
	ContentTypePNG:  {},
	ContentTypeWebP: {},
}

// contentHashPattern matches a hex-encoded sha256 sum: exactly 64 lowercase
// hex characters, the shape PhotoStore.Put always produces.
var contentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Photo errors.
var (
	// ErrPhotoNotFound is returned when a photo does not exist (or belongs
	// to another household).
	ErrPhotoNotFound = errors.New("media: photo not found")
	// ErrInvalidPhoto is returned by Photo.Validate for a malformed photo,
	// and by the upload-validation path for bytes that fail to decode as
	// their sniffed content type.
	ErrInvalidPhoto = errors.New("media: invalid photo")
	// ErrUnsupportedMediaType is returned when an upload's content type is
	// not an accepted image format. Callers typically map it to 415.
	ErrUnsupportedMediaType = errors.New("media: unsupported media type")
	// ErrPhotoTooLarge is returned when an upload exceeds the configured
	// size limit. Callers typically map it to 413.
	ErrPhotoTooLarge = errors.New("media: photo exceeds the maximum size")
	// ErrDuplicatePhoto is returned by PhotoRepository.Create when a photo
	// with the same content hash already exists for the household (the
	// canonical photo_household_content_hash_uniq index — see the adapter
	// package doc for the expected table shape). PhotoService resolves it
	// by fetching and returning the existing photo instead of surfacing an
	// error, so callers never see it directly.
	ErrDuplicatePhoto = errors.New("media: duplicate photo content")
)

// StorageRef is an opaque key identifying a photo's bytes in the PhotoStore.
// The bytes are never stored in the database.
type StorageRef string

// String returns the ref's string value.
func (r StorageRef) String() string { return string(r) }

// Photo is one household image. StorageRef points at the bytes behind the
// PhotoStore; ContentHash is the hex sha256 of those bytes (computed once,
// while streaming the upload to storage) and is what content-hash dedup
// keys on. SizeBytes and ContentType are the other server-verified upload
// facts. TakenAt is the EXIF capture time (UTC) when the upload carried
// one. UploadedBy is the member who added it, nilled (not deleted) if that
// member is removed so the photo survives. StorageBackend is populated by
// PhotoRepository.Create from the repository's own configured backend,
// never by the caller — see that method's doc — so it is the zero value on
// a Photo the caller is still building, exactly like CreatedAt.
type Photo struct {
	ID             PhotoID
	HouseholdID    identity.HouseholdID
	StorageRef     StorageRef
	ContentHash    string
	SizeBytes      int64
	ContentType    string
	TakenAt        *time.Time
	UploadedBy     *identity.MemberID
	CreatedAt      time.Time
	StorageBackend StorageBackend
}

// Validate reports whether the photo is well-formed, wrapping
// ErrInvalidPhoto. ContentHash, SizeBytes, and ContentType are all required
// in their canonical server-verified shape because every photo PhotoStore.
// Put produces has them — a violation here signals a PhotoStore
// implementation bug, not a legitimate legacy photo.
func (p Photo) Validate() error {
	if strings.TrimSpace(p.StorageRef.String()) == "" {
		return fmt.Errorf("%w: storage ref must not be blank", ErrInvalidPhoto)
	}
	if !contentHashPattern.MatchString(p.ContentHash) {
		return fmt.Errorf("%w: content hash must be a 64-character lowercase hex sha256, got %q", ErrInvalidPhoto, p.ContentHash)
	}
	if p.SizeBytes <= 0 {
		return fmt.Errorf("%w: size bytes must be positive, got %d", ErrInvalidPhoto, p.SizeBytes)
	}
	if _, ok := acceptedContentTypes[p.ContentType]; !ok {
		return fmt.Errorf("%w: content type %q is not accepted", ErrInvalidPhoto, p.ContentType)
	}
	return nil
}

// PhotoRepository persists photo metadata (not the bytes) against the
// canonical "photo" table every consumer's own migration provides (see the
// adapter package doc for the exact shape). Get returns ErrPhotoNotFound for
// an unknown id; a Create with an unknown HouseholdID returns
// identity.ErrHouseholdNotFound, an unknown UploadedBy returns
// identity.ErrMemberNotFound, and a content hash that collides with another
// household photo returns ErrDuplicatePhoto (all mapped from the
// tenant/unique constraint violations by the adapter). ListByHousehold
// returns an empty slice (not an error) when none match. FindByContentHash
// returns ErrPhotoNotFound when no household photo carries that hash — the
// expected "not a duplicate" outcome, not an exceptional one.
//
// Every implementation is constructed bound to ONE StorageBackend, matching
// the composition root's single-backend-per-deployment selection — Create
// stamps that value onto photo.StorageBackend, ignoring whatever the caller
// may have left on the struct, so the column always reflects which backend
// genuinely wrote the bytes, never the column's DEFAULT by omission.
//
// Compatibility: this port is shared API consumed by independently
// versioned binaries — additive-only. New capabilities arrive as new,
// narrowly-scoped interfaces (ISP) callers type-assert for (see
// ObjectLister, ObjectExister, RawObjectWriter), never by widening this
// interface itself.
type PhotoRepository interface {
	Create(ctx context.Context, photo *Photo) error
	Get(ctx context.Context, id PhotoID) (*Photo, error)
	FindByContentHash(ctx context.Context, householdID identity.HouseholdID, hash string) (*Photo, error)
	ListByHousehold(ctx context.Context, householdID identity.HouseholdID) ([]*Photo, error)
	Delete(ctx context.Context, id PhotoID) error

	// ListAllStorageRefs returns the StorageRef of every photo row stamped
	// with backend, across every household — a storage reaper's source of
	// truth for "which objects of THIS backend are still referenced."
	// backend is explicit, not implicitly bound to the repository's own
	// configured write backend: a repository instance serves reads across
	// every backend rows may be stamped with, mid-migration mixed state
	// included. Returns an empty slice (not an error) when there are no
	// matching photos.
	ListAllStorageRefs(ctx context.Context, backend StorageBackend) ([]StorageRef, error)

	// ExistsByStorageRef reports whether any photo row STAMPED WITH backend
	// currently references ref, across every household — a targeted,
	// single-ref query a storage reaper runs immediately before deleting an
	// apparently-orphaned object, narrowing the TOCTOU window between a bulk
	// ListAllStorageRefs snapshot and the delete.
	ExistsByStorageRef(ctx context.Context, ref StorageRef, backend StorageBackend) (bool, error)

	// ListByBackend returns up to limit rows stamped with backend, ordered
	// by id ascending, whose id is strictly greater than afterID — a
	// storage migrator's keyset-paginated batch source for "every
	// local-backend photo still to move." Pass the zero PhotoID (
	// ParsePhotoID never produces one) to fetch the first page. Returns an
	// empty (not nil) slice once no more rows match.
	ListByBackend(ctx context.Context, backend StorageBackend, afterID PhotoID, limit int) ([]*Photo, error)

	// MigrateStorageBackend flips ONE local-backend row onto newBackend,
	// writing newRef as the row's new StorageRef and, ONLY when the row's
	// content_sha256 is currently NULL, backfilling it with contentHash.
	// The update is conditioned on the row's CURRENT storage_backend being
	// 'local' — an idempotency guard: done reports whether a row actually
	// matched and was flipped.
	MigrateStorageBackend(ctx context.Context, id PhotoID, newRef StorageRef, newBackend StorageBackend, contentHash string) (done bool, err error)
}
