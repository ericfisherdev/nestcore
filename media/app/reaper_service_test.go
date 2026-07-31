package app_test

import (
	"context"
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

// fakeObjectLister fakes domain.ObjectLister: a fixed set of objects per
// class, so a test can shape exactly what "the bucket contains" without a
// real object store.
type fakeObjectLister struct {
	objects map[domain.PhotoClass][]domain.ObjectInfo
}

func (f *fakeObjectLister) ListObjects(_ context.Context, class domain.PhotoClass) ([]domain.ObjectInfo, error) {
	return f.objects[class], nil
}

const testGraceWindow = 24 * time.Hour

func newTestReaper(t *testing.T, lister *fakeObjectLister, store *fakePhotoStore, sources map[domain.PhotoClass]app.ReapableSource) *app.ReaperService {
	t.Helper()
	r, err := app.NewReaperService(lister, store, domain.StorageBackendLocal, sources, testGraceWindow)
	if err != nil {
		t.Fatalf("NewReaperService: %v", err)
	}
	return r
}

// TestNewReaperServiceValidatesDependencies covers the nil-dependency,
// invalid-backend, empty-sources, and non-positive-graceWindow guards.
func TestNewReaperServiceValidatesDependencies(t *testing.T) {
	lister := &fakeObjectLister{}
	store := &fakePhotoStore{}
	sources := map[domain.PhotoClass]app.ReapableSource{testReaperClassA: newFakePhotoRepo()}

	cases := []struct {
		name    string
		lister  domain.ObjectLister
		store   domain.PhotoStore
		backend domain.StorageBackend
		sources map[domain.PhotoClass]app.ReapableSource
		grace   time.Duration
	}{
		{"nil lister", nil, store, domain.StorageBackendLocal, sources, testGraceWindow},
		{"nil store", lister, nil, domain.StorageBackendLocal, sources, testGraceWindow},
		{"invalid backend", lister, store, domain.StorageBackend("azure-blob"), sources, testGraceWindow},
		{"empty sources", lister, store, domain.StorageBackendLocal, map[domain.PhotoClass]app.ReapableSource{}, testGraceWindow},
		{"nil source value", lister, store, domain.StorageBackendLocal, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: nil}, testGraceWindow},
		{"zero grace window", lister, store, domain.StorageBackendLocal, sources, 0},
		{"negative grace window", lister, store, domain.StorageBackendLocal, sources, -time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.NewReaperService(tc.lister, tc.store, tc.backend, tc.sources, tc.grace); err == nil {
				t.Fatal("NewReaperService should have failed")
			}
		})
	}
}

// TestNewReaperServiceRejectsUnregisteredClassKey covers the guard that
// every map key must be a class actually obtained from
// domain.RegisterPhotoClass.
func TestNewReaperServiceRejectsUnregisteredClassKey(t *testing.T) {
	var zero domain.PhotoClass
	sources := map[domain.PhotoClass]app.ReapableSource{zero: newFakePhotoRepo()}
	if _, err := app.NewReaperService(&fakeObjectLister{}, &fakePhotoStore{}, domain.StorageBackendLocal, sources, testGraceWindow); err == nil {
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

	lister := &fakeObjectLister{objects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {
			{Key: referenced.StorageRef, LastModified: old},
			{Key: domain.StorageRef("households/" + hh.String() + "/test_reaper_class_a/bb/orphan.jpg"), LastModified: old},
		},
	}}
	store := &fakePhotoStore{}

	r := newTestReaper(t, lister, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
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
	lister := &fakeObjectLister{objects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {
			{Key: domain.StorageRef("households/hh/test_reaper_class_a/bb/fresh-orphan.jpg"), LastModified: recent},
		},
	}}
	store := &fakePhotoStore{}

	r := newTestReaper(t, lister, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
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

	lister := &fakeObjectLister{objects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: crossRef, LastModified: old}},
	}}
	store := &fakePhotoStore{}

	r := newTestReaper(t, lister, store, map[domain.PhotoClass]app.ReapableSource{
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

	lister := &fakeObjectLister{objects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: ref, LastModified: old}},
	}}
	store := &fakePhotoStore{}

	r := newTestReaper(t, lister, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
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
	lister := &fakeObjectLister{objects: map[domain.PhotoClass][]domain.ObjectInfo{
		testReaperClassA: {{Key: orphan, LastModified: old}},
	}}
	store := &fakePhotoStore{}

	r := newTestReaper(t, lister, store, map[domain.PhotoClass]app.ReapableSource{testReaperClassA: photos})
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
