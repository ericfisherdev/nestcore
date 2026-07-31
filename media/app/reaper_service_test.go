package app_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	identity "github.com/ericfisherdev/nestcore/identity/domain"
	"github.com/ericfisherdev/nestcore/media/app"
	"github.com/ericfisherdev/nestcore/media/domain"
)

// testReaperClassA/testReaperClassB are two independent registered classes
// so cross-class reference protection can be exercised — a real deployment
// would register these for two different upload purposes (e.g. Nestova's
// album and chore-proof classes).
var (
	testReaperClassA = domain.RegisterPhotoClass("test_reaper_class_a")
	testReaperClassB = domain.RegisterPhotoClass("test_reaper_class_b")
)

// ListObjects satisfies domain.ObjectLister on fakePhotoStore (declared in
// photo_service_test.go): NewReaperService derives its lister from the
// store it is given via a type assertion (mirroring production, where
// S3PhotoStore implements both PhotoStore and ObjectLister on one value),
// so these tests configure listObjects/listErr on the SAME fakePhotoStore
// they pass as the store, rather than a separate lister type.
func (f *fakePhotoStore) ListObjects(_ context.Context, class domain.PhotoClass) ([]domain.ObjectInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listObjects[class], nil
}

const testGraceWindow = 24 * time.Hour

func newTestReaper(t *testing.T, store *fakePhotoStore, sources map[domain.PhotoClass]app.ReapableSource) *app.ReaperService {
	t.Helper()
	r, err := app.NewReaperService(store, domain.StorageBackendLocal, sources, testGraceWindow)
	if err != nil {
		t.Fatalf("NewReaperService: %v", err)
	}
	return r
}

// TestNewReaperServiceValidatesDependencies covers the nil-store,
// invalid-backend, empty-sources, and non-positive-graceWindow guards.
func TestNewReaperServiceValidatesDependencies(t *testing.T) {
	store := &fakePhotoStore{}
	sources := map[domain.PhotoClass]app.ReapableSource{testReaperClassA: newFakePhotoRepo()}

	cases := []struct {
		name    string
		store   domain.PhotoStore
		backend domain.StorageBackend
		sources map[domain.PhotoClass]app.ReapableSource
		grace   time.Duration
	}{
		{"nil store", nil, domain.StorageBackendLocal, sources, testGraceWindow},
		{"invalid backend", store, domain.StorageBackend("azure-blob"), sources, testGraceWindow},
		{"empty sources", store, domain.StorageBackendLocal, map[domain.PhotoClass]app.ReapableSource{}, testGraceWindow},
		{"nil source value", store, domain.StorageBackendLocal, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: nil}, testGraceWindow},
		{"zero grace window", store, domain.StorageBackendLocal, sources, 0},
		{"negative grace window", store, domain.StorageBackendLocal, sources, -time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.NewReaperService(tc.store, tc.backend, tc.sources, tc.grace); err == nil {
				t.Fatal("NewReaperService should have failed")
			}
		})
	}
}

// TestNewReaperServiceRejectsStoreWithoutObjectLister proves the
// constructor's type assertion actually gates construction: nonListingStore
// satisfies domain.PhotoStore but deliberately not domain.ObjectLister.
func TestNewReaperServiceRejectsStoreWithoutObjectLister(t *testing.T) {
	sources := map[domain.PhotoClass]app.ReapableSource{testReaperClassA: newFakePhotoRepo()}
	if _, err := app.NewReaperService(nonListingStore{}, domain.StorageBackendLocal, sources, testGraceWindow); err == nil {
		t.Fatal("NewReaperService with a store that does not implement ObjectLister = nil error, want error")
	}
}

// nonListingStore implements domain.PhotoStore's minimal surface only —
// deliberately NOT domain.ObjectLister (LocalPhotoStore's real-world
// shape) — so NewReaperService's type assertion has something real to
// fail against.
type nonListingStore struct{}

func (nonListingStore) Put(context.Context, identity.HouseholdID, domain.PhotoClass, io.Reader) (domain.PutResult, error) {
	return domain.PutResult{}, nil
}

func (nonListingStore) Open(context.Context, domain.StorageRef) (domain.PhotoReader, error) {
	return nil, domain.ErrPhotoNotFound
}
func (nonListingStore) Delete(context.Context, domain.StorageRef) error { return nil }
func (nonListingStore) URL(context.Context, domain.StorageRef, time.Duration) (string, error) {
	return "", nil
}
func (nonListingStore) SupportsDirectURL() bool { return false }

// TestNewReaperServiceRejectsUnregisteredClassKey covers the guard that
// every map key must be a class actually obtained from
// domain.RegisterPhotoClass.
func TestNewReaperServiceRejectsUnregisteredClassKey(t *testing.T) {
	var zero domain.PhotoClass
	sources := map[domain.PhotoClass]app.ReapableSource{zero: newFakePhotoRepo()}
	if _, err := app.NewReaperService(&fakePhotoStore{}, domain.StorageBackendLocal, sources, testGraceWindow); err == nil {
		t.Fatal("NewReaperService with an unregistered class key = nil error, want error")
	}
}

// TestReaperDeletesUnreferencedObjectsPastGraceWindow covers the core
// contract: an object with no referencing row, older than the grace
// window, is deleted; a referenced object never is, regardless of age.
func TestReaperDeletesUnreferencedObjectsPastGraceWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * testGraceWindow)

	hh := identity.NewHouseholdID()
	referenced := &domain.Photo{ID: domain.NewPhotoID(), HouseholdID: hh, StorageRef: domain.StorageRef("households/" + hh.String() + "/test_reaper_class_a/aa/referenced.jpg"), StorageBackend: domain.StorageBackendLocal}
	photos := newFakePhotoRepo()
	photos.store[referenced.ID] = referenced

	store := &fakePhotoStore{listObjects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {
			{Key: referenced.StorageRef, LastModified: old},
			{Key: domain.StorageRef("households/" + hh.String() + "/test_reaper_class_a/bb/orphan.jpg"), LastModified: old},
		},
	}}

	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
	result, err := r.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OrphansDeleted[testReaperClassA] != 1 {
		t.Fatalf("OrphansDeleted[classA] = %d, want 1", result.OrphansDeleted[testReaperClassA])
	}
	if len(store.deleted) != 1 || store.deleted[0] != domain.StorageRef("households/"+hh.String()+"/test_reaper_class_a/bb/orphan.jpg") {
		t.Fatalf("store.deleted = %v, want exactly the orphan ref", store.deleted)
	}
}

// TestReaperSkipsObjectsWithinGraceWindow covers the other half of the
// contract: an unreferenced object younger than the grace window must not
// be deleted yet, since it might be a concurrent, not-yet-committed
// upload.
func TestReaperSkipsObjectsWithinGraceWindow(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-testGraceWindow / 2)

	photos := newFakePhotoRepo()
	store := &fakePhotoStore{listObjects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {
			{Key: domain.StorageRef("households/hh/test_reaper_class_a/bb/fresh-orphan.jpg"), LastModified: recent},
		},
	}}

	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
	result, err := r.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OrphansDeleted[testReaperClassA] != 0 {
		t.Fatalf("OrphansDeleted[classA] = %d, want 0 (object is within the grace window)", result.OrphansDeleted[testReaperClassA])
	}
	if len(store.deleted) != 0 {
		t.Fatalf("store.deleted = %v, want none", store.deleted)
	}
}

// TestReaperCrossClassReferenceProtectsObject covers why referencedRefs is
// computed across EVERY registered source, not just the source owning the
// class an object was listed under: a row persisted under classB's source
// can reference an object filed under classA's own key prefix (e.g. a
// cross-prefix migration artifact), and that reference must still protect
// the object.
func TestReaperCrossClassReferenceProtectsObject(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * testGraceWindow)

	hh := identity.NewHouseholdID()
	crossRef := domain.StorageRef("households/" + hh.String() + "/test_reaper_class_a/aa/cross.jpg")
	// Persisted under classB's repo, but its key lives under classA's
	// prefix.
	crossRow := &domain.Photo{ID: domain.NewPhotoID(), HouseholdID: hh, StorageRef: crossRef, StorageBackend: domain.StorageBackendLocal}
	classBRepo := newFakePhotoRepo()
	classBRepo.store[crossRow.ID] = crossRow
	classARepo := newFakePhotoRepo()

	store := &fakePhotoStore{listObjects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: crossRef, LastModified: old}},
	}}

	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{
		testReaperClassA: classARepo,
		testReaperClassB: classBRepo,
	})
	result, err := r.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OrphansDeleted[testReaperClassA] != 0 {
		t.Fatalf("OrphansDeleted[classA] = %d, want 0 (object is referenced by classB's source)", result.OrphansDeleted[testReaperClassA])
	}
	if len(store.deleted) != 0 {
		t.Fatalf("store.deleted = %v, want none", store.deleted)
	}
}

// TestReaperTOCTOURecheckProtectsLateCommit covers the TOCTOU note: a row
// that "commits" (becomes visible via ExistsByStorageRef) between the bulk
// referencedRefs snapshot and Run's per-candidate delete must still be
// protected — this is what makes restoring a DB backup mid-reap safe.
func TestReaperTOCTOURecheckProtectsLateCommit(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * testGraceWindow)
	ref := domain.StorageRef("households/hh/test_reaper_class_a/aa/late-commit.jpg")

	photos := newFakePhotoRepo()
	// Not present in the bulk ListAllStorageRefs snapshot (the store map is
	// empty), but existsOverride makes the per-candidate recheck report it
	// as referenced — simulating a row that committed after the snapshot.
	photos.existsOverride = map[domain.StorageRef]bool{ref: true}

	store := &fakePhotoStore{listObjects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: ref, LastModified: old}},
	}}

	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
	result, err := r.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OrphansDeleted[testReaperClassA] != 0 {
		t.Fatalf("OrphansDeleted[classA] = %d, want 0 (recheck must catch the late commit)", result.OrphansDeleted[testReaperClassA])
	}
	if len(store.deleted) != 0 {
		t.Fatalf("store.deleted = %v, want none", store.deleted)
	}
	if len(photos.existsCalls) != 1 || photos.existsCalls[0] != ref {
		t.Fatalf("existsCalls = %v, want exactly one recheck of %s", photos.existsCalls, ref)
	}
}

// TestReaperDryRunPreviewsWithoutDeleting proves DryRun reports the exact
// same candidates Run would delete, without deleting anything or
// performing Run's per-candidate recheck.
func TestReaperDryRunPreviewsWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * testGraceWindow)
	orphan := domain.StorageRef("households/hh/test_reaper_class_a/bb/orphan.jpg")

	photos := newFakePhotoRepo()
	store := &fakePhotoStore{listObjects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: orphan, LastModified: old}},
	}}

	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
	result, err := r.DryRun(context.Background(), now)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if refs := result.OrphansWouldDelete[testReaperClassA]; len(refs) != 1 || refs[0] != orphan {
		t.Fatalf("OrphansWouldDelete[classA] = %v, want [%s]", refs, orphan)
	}
	if len(store.deleted) != 0 {
		t.Fatal("DryRun must never delete anything")
	}
	if len(photos.existsCalls) != 0 {
		t.Fatal("DryRun must not perform Run's per-candidate recheck")
	}
}

// TestReaperDeletesExactlyAtGraceWindowBoundary covers the boundary itself:
// orphanCandidates' cutoff check is obj.LastModified.After(cutoff), so an
// object whose LastModified equals the cutoff exactly (neither strictly
// after nor before it) is NOT "after" and therefore IS eligible for
// deletion — the grace window is inclusive of its own edge, not exclusive.
func TestReaperDeletesExactlyAtGraceWindowBoundary(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-testGraceWindow)
	ref := domain.StorageRef("households/hh/test_reaper_class_a/aa/boundary.jpg")

	photos := newFakePhotoRepo()
	store := &fakePhotoStore{listObjects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: ref, LastModified: cutoff}},
	}}

	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
	result, err := r.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.OrphansDeleted[testReaperClassA] != 1 {
		t.Fatalf("OrphansDeleted[classA] = %d, want 1 (an object exactly at the grace-window cutoff is eligible)", result.OrphansDeleted[testReaperClassA])
	}
	if len(store.deleted) != 1 || store.deleted[0] != ref {
		t.Fatalf("store.deleted = %v, want exactly [%s]", store.deleted, ref)
	}
}

// TestReaperRunPropagatesListerError and
// TestReaperDryRunPropagatesListerError cover the lister-failure path: an
// ObjectLister error must propagate as an error, never be silently
// swallowed into an empty/zero result.
func TestReaperRunPropagatesListerError(t *testing.T) {
	store := &fakePhotoStore{listErr: errors.New("bucket unreachable")}
	photos := newFakePhotoRepo()
	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})

	if _, err := r.Run(context.Background(), time.Now()); err == nil {
		t.Fatal("Run should fail when the lister errors")
	}
}

func TestReaperDryRunPropagatesListerError(t *testing.T) {
	store := &fakePhotoStore{listErr: errors.New("bucket unreachable")}
	photos := newFakePhotoRepo()
	r := newTestReaper(t, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})

	if _, err := r.DryRun(context.Background(), time.Now()); err == nil {
		t.Fatal("DryRun should fail when the lister errors")
	}
}
