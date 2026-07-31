package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestcore/identity/domain"
	"github.com/ericfisherdev/nestcore/media/app"
	"github.com/ericfisherdev/nestcore/media/domain"
)

// testClass is a PhotoClass registered once for this test package's use —
// registration is process-global (see domain.RegisterPhotoClass), so a
// single package-level var, not one per test, is what a real consumer
// would do too.
var testClass = domain.RegisterPhotoClass("test_upload_class")

// --- fakes ---

// fakeStoreResolver fakes domain.PhotoStoreResolver: a fixed map of stores
// per backend, so a test can control exactly which backends this
// "deployment" has configured (the mixed-state case) without a real
// composition root.
type fakeStoreResolver struct {
	stores map[domain.StorageBackend]domain.PhotoStore
}

func newFakeStoreResolver(backend domain.StorageBackend, store domain.PhotoStore) *fakeStoreResolver {
	return &fakeStoreResolver{stores: map[domain.StorageBackend]domain.PhotoStore{backend: store}}
}

func (f *fakeStoreResolver) withStore(backend domain.StorageBackend, store domain.PhotoStore) *fakeStoreResolver {
	f.stores[backend] = store
	return f
}

func (f *fakeStoreResolver) Resolve(backend domain.StorageBackend) (domain.PhotoStore, error) {
	store, ok := f.stores[backend]
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrStoreNotConfigured, backend)
	}
	return store, nil
}

type fakePhotoStore struct {
	putErr    error
	openErr   error
	urlErr    error
	puts      int
	openCalls int
	// urlCalls counts URL invocations — asserted at 0 by ownership-rejection
	// tests (RawServe must reject a cross-household id BEFORE ever
	// consulting the store, on either the Open or the URL branch).
	urlCalls     int
	deleted      []domain.StorageRef
	lastPutClass domain.PhotoClass
	// directURL backs SupportsDirectURL — false (LocalPhotoStore-like)
	// unless a test opts into the S3-like redirect path.
	directURL bool
}

// Put hashes the bytes it's given and derives Ref from the hash — like the
// real content-addressed LocalPhotoStore — so identical content always
// produces the identical ref, letting a test detect an unsafe delete of a
// ref a still-valid photo row shares.
func (f *fakePhotoStore) Put(_ context.Context, _ identity.HouseholdID, class domain.PhotoClass, r io.Reader) (domain.PutResult, error) {
	f.lastPutClass = class
	if f.putErr != nil {
		return domain.PutResult{}, f.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return domain.PutResult{}, err
	}
	f.puts++
	hash := sha256Hex(string(data))
	return domain.PutResult{
		Ref:         refFor(hash),
		ContentHash: hash,
		SizeBytes:   int64(len(data)),
		ContentType: "image/jpeg",
	}, nil
}

// refFor mirrors LocalPhotoStore's content-addressed layout
// (<household>/<aa>/<hash>.<ext>), collapsed to a fixed household segment
// since these tests don't exercise cross-household path separation.
func refFor(hash string) domain.StorageRef {
	return domain.StorageRef(fmt.Sprintf("hh/%s/%s.jpg", hash[:2], hash))
}

func (f *fakePhotoStore) Open(context.Context, domain.StorageRef) (domain.PhotoReader, error) {
	f.openCalls++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return fakePhotoReader{bytes.NewReader(nil)}, nil
}

func (f *fakePhotoStore) Delete(_ context.Context, ref domain.StorageRef) error {
	f.deleted = append(f.deleted, ref)
	return nil
}

// URL mirrors LocalPhotoStore's contract closely enough for a unit test:
// ref itself, back as a stable locator, since nothing under test exercises
// a real URL/ttl semantic.
func (f *fakePhotoStore) URL(_ context.Context, ref domain.StorageRef, _ time.Duration) (string, error) {
	f.urlCalls++
	if f.urlErr != nil {
		return "", f.urlErr
	}
	return ref.String(), nil
}

// SupportsDirectURL defaults to false (mirroring LocalPhotoStore) so
// existing tests that never set directURL keep exercising the
// Open-and-stream path; RawServe tests flip it to exercise the redirect
// path.
func (f *fakePhotoStore) SupportsDirectURL() bool { return f.directURL }

// fakePhotoReader adapts a *bytes.Reader (already Read+ReadAt+Seek) into a
// domain.PhotoReader with a no-op Close.
type fakePhotoReader struct{ *bytes.Reader }

func (fakePhotoReader) Close() error { return nil }

// sha256Hex mirrors what fakePhotoStore.Put (and the real LocalPhotoStore)
// computes for content s, so a test can seed a photo with the exact hash a
// later Upload of the same bytes will produce.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type fakeExif struct{ taken *time.Time }

func (f fakeExif) TakenAt(domain.RandomAccessReader) *time.Time { return f.taken }

type fakePhotoRepo struct {
	store     map[domain.PhotoID]*domain.Photo
	createErr error
	created   []*domain.Photo
	deleted   []domain.PhotoID

	// raceHash/raceWinner/raceFindCalls simulate a concurrent upload winning
	// the unique-hash race between PhotoService's pre-Create dedup check and
	// its retry after Create fails with ErrDuplicatePhoto.
	raceHash      string
	raceWinner    *domain.Photo
	raceFindCalls int

	existsOverride map[domain.StorageRef]bool
	existsCalls    []domain.StorageRef

	// backend is the StorageBackend Create stamps onto every row it
	// writes, defaulting to domain.StorageBackendLocal via
	// newFakePhotoRepo.
	backend domain.StorageBackend
}

func newFakePhotoRepo() *fakePhotoRepo {
	return &fakePhotoRepo{store: map[domain.PhotoID]*domain.Photo{}, backend: domain.StorageBackendLocal}
}

func (f *fakePhotoRepo) Create(_ context.Context, p *domain.Photo) error {
	if f.createErr != nil {
		return f.createErr
	}
	p.StorageBackend = f.backend
	f.store[p.ID] = p
	f.created = append(f.created, p)
	return nil
}

func (f *fakePhotoRepo) Get(_ context.Context, id domain.PhotoID) (*domain.Photo, error) {
	if p, ok := f.store[id]; ok {
		return p, nil
	}
	return nil, domain.ErrPhotoNotFound
}

func (f *fakePhotoRepo) FindByContentHash(_ context.Context, householdID identity.HouseholdID, hash string) (*domain.Photo, error) {
	if hash == "" {
		return nil, domain.ErrPhotoNotFound
	}
	if f.raceHash != "" && hash == f.raceHash {
		f.raceFindCalls++
		if f.raceFindCalls == 1 {
			return nil, domain.ErrPhotoNotFound
		}
		return f.raceWinner, nil
	}
	for _, p := range f.store {
		if p.HouseholdID == householdID && p.ContentHash == hash {
			return p, nil
		}
	}
	return nil, domain.ErrPhotoNotFound
}

func (f *fakePhotoRepo) ListByHousehold(context.Context, identity.HouseholdID) ([]*domain.Photo, error) {
	return nil, nil
}

func (f *fakePhotoRepo) Delete(_ context.Context, id domain.PhotoID) error {
	if _, ok := f.store[id]; !ok {
		return domain.ErrPhotoNotFound
	}
	delete(f.store, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakePhotoRepo) ListAllStorageRefs(_ context.Context, backend domain.StorageBackend) ([]domain.StorageRef, error) {
	refs := make([]domain.StorageRef, 0, len(f.store))
	for _, p := range f.store {
		if p.StorageBackend == backend {
			refs = append(refs, p.StorageRef)
		}
	}
	return refs, nil
}

func (f *fakePhotoRepo) ExistsByStorageRef(_ context.Context, ref domain.StorageRef, backend domain.StorageBackend) (bool, error) {
	f.existsCalls = append(f.existsCalls, ref)
	if v, ok := f.existsOverride[ref]; ok {
		return v, nil
	}
	for _, p := range f.store {
		if p.StorageRef == ref && p.StorageBackend == backend {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakePhotoRepo) ListByBackend(_ context.Context, backend domain.StorageBackend, afterID domain.PhotoID, limit int) ([]*domain.Photo, error) {
	matches := make([]*domain.Photo, 0, len(f.store))
	for _, p := range f.store {
		if p.StorageBackend == backend && p.ID.String() > afterID.String() {
			matches = append(matches, p)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID.String() < matches[j].ID.String() })
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (f *fakePhotoRepo) MigrateStorageBackend(_ context.Context, id domain.PhotoID, newRef domain.StorageRef, newBackend domain.StorageBackend, contentHash string) (bool, error) {
	p, ok := f.store[id]
	if !ok || p.StorageBackend != domain.StorageBackendLocal {
		return false, nil
	}
	p.StorageRef = newRef
	p.StorageBackend = newBackend
	if p.ContentHash == "" {
		p.ContentHash = contentHash
	}
	return true, nil
}

// --- PhotoService ---

func TestPhotoServiceUpload(t *testing.T) {
	store := &fakePhotoStore{}
	taken := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := newFakePhotoRepo()
	svc, err := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{taken: &taken}, repo)
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	hh := identity.NewHouseholdID()
	uploader := identity.NewMemberID()

	result, err := svc.Upload(context.Background(), hh, uploader, bytes.NewReader([]byte("imgbytes")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if result.Duplicate {
		t.Fatal("first upload of new content must not be a duplicate")
	}
	photo := result.Photo
	if photo.StorageRef != refFor(sha256Hex("imgbytes")) || photo.TakenAt == nil || !photo.TakenAt.Equal(taken) {
		t.Fatalf("uploaded photo = %+v", photo)
	}
	if photo.ContentHash == "" {
		t.Fatal("uploaded photo must carry the content hash PhotoStore.Put computed")
	}
	if photo.UploadedBy == nil || *photo.UploadedBy != uploader || photo.HouseholdID != hh {
		t.Fatalf("attribution wrong: %+v", photo)
	}
	if len(repo.created) != 1 {
		t.Fatalf("created %d photos, want 1", len(repo.created))
	}
	if store.lastPutClass != testClass {
		t.Fatalf("Upload called Put with class %v, want %v", store.lastPutClass, testClass)
	}
}

// TestPhotoServiceUploadDeduplicatesByContentHash covers dedup: uploading
// the same bytes twice for a household creates exactly one photo row, and
// the second Upload reports Duplicate instead of erroring.
func TestPhotoServiceUploadDeduplicatesByContentHash(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)
	hh := identity.NewHouseholdID()
	uploader := identity.NewMemberID()

	first, err := svc.Upload(context.Background(), hh, uploader, bytes.NewReader([]byte("same-bytes")))
	if err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	if first.Duplicate {
		t.Fatal("first upload must not be reported as a duplicate")
	}

	second, err := svc.Upload(context.Background(), hh, uploader, bytes.NewReader([]byte("same-bytes")))
	if err != nil {
		t.Fatalf("second Upload: %v", err)
	}
	if !second.Duplicate {
		t.Fatal("re-uploading identical bytes must be reported as a duplicate")
	}
	if second.Photo.ID != first.Photo.ID {
		t.Fatalf("duplicate upload returned a different photo: got %s, want %s", second.Photo.ID, first.Photo.ID)
	}
	if len(repo.created) != 1 {
		t.Fatalf("created %d photo rows, want 1 (dedup must not create a second row)", len(repo.created))
	}
}

// TestPhotoServiceUploadResolvesConcurrentDuplicate covers the race where
// two uploads of the same bytes both pass the pre-check and only one wins
// the unique-index insert.
func TestPhotoServiceUploadResolvesConcurrentDuplicate(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	hh := identity.NewHouseholdID()
	hash := sha256Hex("raced-bytes")
	winner := &domain.Photo{
		ID: domain.NewPhotoID(), HouseholdID: hh,
		StorageRef: refFor(hash), ContentHash: hash,
	}
	repo.raceHash = hash
	repo.raceWinner = winner
	repo.createErr = domain.ErrDuplicatePhoto
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)

	result, err := svc.Upload(context.Background(), hh, identity.NewMemberID(), bytes.NewReader([]byte("raced-bytes")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !result.Duplicate || result.Photo.ID != winner.ID {
		t.Fatalf("Upload = %+v, want the pre-existing winner reported as a duplicate", result)
	}
	if repo.raceFindCalls != 2 {
		t.Fatalf("FindByContentHash called %d times, want 2 (pre-check miss, then a hit after Create's ErrDuplicatePhoto)", repo.raceFindCalls)
	}
}

func TestPhotoServiceUploadStoreErrorPropagates(t *testing.T) {
	store := &fakePhotoStore{putErr: domain.ErrUnsupportedMediaType}
	repo := newFakePhotoRepo()
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)
	if _, err := svc.Upload(context.Background(), identity.NewHouseholdID(), identity.NewMemberID(), bytes.NewReader([]byte("x"))); !errors.Is(err, domain.ErrUnsupportedMediaType) {
		t.Fatalf("Upload error = %v, want ErrUnsupportedMediaType", err)
	}
	if len(repo.created) != 0 {
		t.Fatal("store error must not persist a photo")
	}
}

// TestPhotoServiceUploadDoesNotCleanUpOnCreateError covers the invariant a
// failure after Put must not delete stored bytes.
func TestPhotoServiceUploadDoesNotCleanUpOnCreateError(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	repo.createErr = errors.New("db down")
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)
	if _, err := svc.Upload(context.Background(), identity.NewHouseholdID(), identity.NewMemberID(), bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Upload should fail when Create fails")
	}
	if len(store.deleted) != 0 {
		t.Fatalf("Upload must not delete stored bytes on a Create failure, deleted=%v", store.deleted)
	}
}

// TestPhotoServiceUploadDoesNotCleanUpOnExifReopenError covers the same
// no-synchronous-delete invariant for the failure path where
// PhotoStore.Open (used to feed the ExifReader) errors after Put already
// succeeded.
func TestPhotoServiceUploadDoesNotCleanUpOnExifReopenError(t *testing.T) {
	store := &fakePhotoStore{openErr: errors.New("disk hiccup")}
	repo := newFakePhotoRepo()
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)
	if _, err := svc.Upload(context.Background(), identity.NewHouseholdID(), identity.NewMemberID(), bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("Upload should fail when the exif reopen fails")
	}
	if len(store.deleted) != 0 {
		t.Fatalf("Upload must not delete stored bytes on an exif reopen failure, deleted=%v", store.deleted)
	}
	if len(repo.created) != 0 {
		t.Fatal("exif reopen error must not persist a photo")
	}
}

func TestPhotoServiceDeleteRejectsOtherHousehold(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	other := identity.NewHouseholdID()
	id := domain.NewPhotoID()
	repo.store[id] = &domain.Photo{ID: id, HouseholdID: other, StorageRef: "x/y/z.jpg"}
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)

	if err := svc.Delete(context.Background(), identity.NewHouseholdID(), id); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Fatalf("cross-household Delete = %v, want ErrPhotoNotFound", err)
	}
	if len(repo.deleted) != 0 || len(store.deleted) != 0 {
		t.Fatal("cross-household Delete must not remove anything")
	}
}

// TestPhotoServiceDeleteIsRowsOnly covers the invariant documented on
// Delete: a successful delete removes the metadata row but never touches
// the stored bytes.
func TestPhotoServiceDeleteIsRowsOnly(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	hh := identity.NewHouseholdID()
	id := domain.NewPhotoID()
	repo.store[id] = &domain.Photo{ID: id, HouseholdID: hh, StorageRef: "hh/aa/x.jpg"}
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)

	if err := svc.Delete(context.Background(), hh, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != id {
		t.Fatalf("Delete did not remove the metadata row: deleted=%v", repo.deleted)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("Delete must never remove stored bytes, got deleted=%v", store.deleted)
	}
}

// TestPhotoServiceRawServeStreamsWhenBackendLacksDirectURL covers
// RawServe's local-backend branch: SupportsDirectURL false means RawServe
// opens and returns a Body to stream, never a RedirectURL.
func TestPhotoServiceRawServeStreamsWhenBackendLacksDirectURL(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	hh := identity.NewHouseholdID()
	id := domain.NewPhotoID()
	repo.store[id] = &domain.Photo{ID: id, HouseholdID: hh, StorageRef: "hh/aa/x.jpg", StorageBackend: domain.StorageBackendLocal}
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)

	result, err := svc.RawServe(context.Background(), hh, id)
	if err != nil {
		t.Fatalf("RawServe: %v", err)
	}
	if result.RedirectURL != "" {
		t.Fatalf("RedirectURL = %q, want empty for a local-like backend", result.RedirectURL)
	}
	if result.Body == nil {
		t.Fatal("Body is nil, want a stream for a local-like backend")
	}
	_ = result.Body.Close()
	if store.openCalls != 1 {
		t.Fatalf("Open was called %d times, want 1", store.openCalls)
	}
}

// TestPhotoServiceRawServeRedirectsWhenBackendSupportsDirectURL covers
// RawServe's S3-like backend branch: SupportsDirectURL true means RawServe
// calls URL and returns a RedirectURL, never opening/streaming a body.
func TestPhotoServiceRawServeRedirectsWhenBackendSupportsDirectURL(t *testing.T) {
	store := &fakePhotoStore{directURL: true}
	repo := newFakePhotoRepo()
	hh := identity.NewHouseholdID()
	id := domain.NewPhotoID()
	repo.store[id] = &domain.Photo{ID: id, HouseholdID: hh, StorageRef: "households/hh/test/aa/x.jpg", StorageBackend: domain.StorageBackendS3}
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendS3, store), domain.StorageBackendS3, fakeExif{}, repo)

	result, err := svc.RawServe(context.Background(), hh, id)
	if err != nil {
		t.Fatalf("RawServe: %v", err)
	}
	if result.RedirectURL != "households/hh/test/aa/x.jpg" {
		t.Fatalf("RedirectURL = %q, want the fake store's URL() result", result.RedirectURL)
	}
	if result.Body != nil {
		t.Fatal("Body is non-nil, want none for an S3-like backend redirect")
	}
	if store.openCalls != 0 {
		t.Fatalf("Open was called %d times, want 0 (redirect must never open/stream)", store.openCalls)
	}
}

// TestPhotoServiceMixedStateReadsResolveByRowBackend covers the core
// mixed-state fix directly: with BOTH a local and an s3 store registered
// in the resolver, a row stamped 'local' resolves to the local store and a
// row stamped 's3' resolves to the s3 store — in the SAME service
// instance, regardless of which backend is currently configured for new
// writes.
func TestPhotoServiceMixedStateReadsResolveByRowBackend(t *testing.T) {
	localStore := &fakePhotoStore{}
	s3Store := &fakePhotoStore{directURL: true}
	resolver := newFakeStoreResolver(domain.StorageBackendLocal, localStore).withStore(domain.StorageBackendS3, s3Store)
	repo := newFakePhotoRepo()
	hh := identity.NewHouseholdID()

	localID := domain.NewPhotoID()
	repo.store[localID] = &domain.Photo{ID: localID, HouseholdID: hh, StorageRef: "households/hh/test/aa/local.jpg", StorageBackend: domain.StorageBackendLocal}
	s3ID := domain.NewPhotoID()
	repo.store[s3ID] = &domain.Photo{ID: s3ID, HouseholdID: hh, StorageRef: "households/hh/test/bb/s3.jpg", StorageBackend: domain.StorageBackendS3}

	// writeBackend is s3 here specifically — proving reads for the OLDER
	// local row still work even though this "deployment" now writes new
	// photos to s3.
	svc, err := app.NewPhotoService(testClass, resolver, domain.StorageBackendS3, fakeExif{}, repo)
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	localResult, err := svc.RawServe(context.Background(), hh, localID)
	if err != nil {
		t.Fatalf("RawServe(local row): %v", err)
	}
	if localResult.Body == nil || localResult.RedirectURL != "" {
		t.Fatalf("RawServe(local row) = %+v, want a streamed Body (local store has no direct URL)", localResult)
	}
	if localStore.openCalls != 1 {
		t.Fatalf("local store Open calls = %d, want 1", localStore.openCalls)
	}
	if s3Store.openCalls != 0 || s3Store.urlCalls != 0 {
		t.Fatal("the local row's RawServe must never touch the s3 store")
	}

	s3Result, err := svc.RawServe(context.Background(), hh, s3ID)
	if err != nil {
		t.Fatalf("RawServe(s3 row): %v", err)
	}
	if s3Result.RedirectURL != "households/hh/test/bb/s3.jpg" || s3Result.Body != nil {
		t.Fatalf("RawServe(s3 row) = %+v, want a RedirectURL (s3 store supports direct URLs)", s3Result)
	}
	if s3Store.urlCalls != 1 {
		t.Fatalf("s3 store URL calls = %d, want 1", s3Store.urlCalls)
	}
	if localStore.openCalls != 1 {
		t.Fatal("the s3 row's RawServe must never touch the local store beyond the earlier call")
	}
}

// TestPhotoServiceRawServeReturnsErrStoreNotConfiguredForMissingBackend
// covers the missing-store error path: a row stamped with a backend this
// deployment never constructed a store for must fail with a wrapped
// domain.ErrStoreNotConfigured, not panic or silently resolve to the
// wrong store.
func TestPhotoServiceRawServeReturnsErrStoreNotConfiguredForMissingBackend(t *testing.T) {
	localStore := &fakePhotoStore{}
	// Only 'local' is registered — mirrors a local-only deployment.
	resolver := newFakeStoreResolver(domain.StorageBackendLocal, localStore)
	repo := newFakePhotoRepo()
	hh := identity.NewHouseholdID()

	id := domain.NewPhotoID()
	repo.store[id] = &domain.Photo{ID: id, HouseholdID: hh, StorageRef: "households/hh/test/aa/s3-only.jpg", StorageBackend: domain.StorageBackendS3}
	svc, err := app.NewPhotoService(testClass, resolver, domain.StorageBackendLocal, fakeExif{}, repo)
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	if _, err := svc.RawServe(context.Background(), hh, id); !errors.Is(err, domain.ErrStoreNotConfigured) {
		t.Fatalf("RawServe(s3-stamped row, no s3 store configured) = %v, want ErrStoreNotConfigured", err)
	}
	if _, _, err := svc.OpenBytes(context.Background(), hh, id); !errors.Is(err, domain.ErrStoreNotConfigured) {
		t.Fatalf("OpenBytes(s3-stamped row, no s3 store configured) = %v, want ErrStoreNotConfigured", err)
	}
}

// TestPhotoServiceUploadAlwaysWritesToConfiguredBackend covers the write
// side of the mixed-state fix: Upload writes new photos to writeBackend —
// the CONFIGURED backend — never to some other registered store, even
// when the resolver holds more than one.
func TestPhotoServiceUploadAlwaysWritesToConfiguredBackend(t *testing.T) {
	localStore := &fakePhotoStore{}
	s3Store := &fakePhotoStore{}
	resolver := newFakeStoreResolver(domain.StorageBackendLocal, localStore).withStore(domain.StorageBackendS3, s3Store)
	repo := newFakePhotoRepo()
	repo.backend = domain.StorageBackendS3 // mirrors the real repo also being configured for s3
	hh := identity.NewHouseholdID()

	svc, err := app.NewPhotoService(testClass, resolver, domain.StorageBackendS3, fakeExif{}, repo)
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	result, err := svc.Upload(context.Background(), hh, identity.NewMemberID(), bytes.NewReader([]byte("upload-bytes")))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if s3Store.puts != 1 {
		t.Fatalf("s3 store Put calls = %d, want 1", s3Store.puts)
	}
	if localStore.puts != 0 {
		t.Fatal("Upload must never write to a backend other than writeBackend")
	}
	if result.Photo.StorageBackend != domain.StorageBackendS3 {
		t.Fatalf("created photo StorageBackend = %q, want %q", result.Photo.StorageBackend, domain.StorageBackendS3)
	}
}

// TestPhotoServiceUploadWriteStoreNotConfigured covers the write-side
// mirror of TestPhotoServiceRawServeReturnsErrStoreNotConfiguredForMissingBackend:
// a resolver that never registered writeBackend's store must fail Upload
// with a wrapped domain.ErrStoreNotConfigured, before ever calling Create.
func TestPhotoServiceUploadWriteStoreNotConfigured(t *testing.T) {
	// Only 'local' is registered, but the service is configured to WRITE to s3.
	resolver := newFakeStoreResolver(domain.StorageBackendLocal, &fakePhotoStore{})
	repo := newFakePhotoRepo()
	svc, err := app.NewPhotoService(testClass, resolver, domain.StorageBackendS3, fakeExif{}, repo)
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}

	if _, err := svc.Upload(context.Background(), identity.NewHouseholdID(), identity.NewMemberID(), bytes.NewReader([]byte("x"))); !errors.Is(err, domain.ErrStoreNotConfigured) {
		t.Fatalf("Upload error = %v, want ErrStoreNotConfigured", err)
	}
	if len(repo.created) != 0 {
		t.Fatal("Upload must not create a photo row when the write store cannot be resolved")
	}
}

// TestPhotoServiceRawServeRejectsOtherHousehold mirrors
// TestPhotoServiceDeleteRejectsOtherHousehold: RawServe must enforce
// ownership BEFORE consulting the store at all, regardless of backend.
func TestPhotoServiceRawServeRejectsOtherHousehold(t *testing.T) {
	store := &fakePhotoStore{directURL: true}
	repo := newFakePhotoRepo()
	other := identity.NewHouseholdID()
	id := domain.NewPhotoID()
	repo.store[id] = &domain.Photo{ID: id, HouseholdID: other, StorageRef: "x/y/z.jpg"}
	svc, _ := app.NewPhotoService(testClass, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo)

	if _, err := svc.RawServe(context.Background(), identity.NewHouseholdID(), id); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Fatalf("cross-household RawServe = %v, want ErrPhotoNotFound", err)
	}
	if store.openCalls != 0 {
		t.Fatal("cross-household RawServe must never touch the store")
	}
	// directURL is true on this fake specifically so a bug that checked
	// ownership AFTER branching on SupportsDirectURL would still be
	// caught: URL must never be called either.
	if store.urlCalls != 0 {
		t.Fatal("cross-household RawServe must never call URL")
	}
}

func TestNewPhotoServiceRejectsUnregisteredClass(t *testing.T) {
	store := &fakePhotoStore{}
	repo := newFakePhotoRepo()
	var zero domain.PhotoClass
	if _, err := app.NewPhotoService(zero, newFakeStoreResolver(domain.StorageBackendLocal, store), domain.StorageBackendLocal, fakeExif{}, repo); err == nil {
		t.Fatal("NewPhotoService with an unregistered class = nil error, want error")
	}
}
