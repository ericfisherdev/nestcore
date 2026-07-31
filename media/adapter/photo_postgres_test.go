package adapter_test

import (
	"errors"
	"testing"
	"time"

	identityadapter "github.com/ericfisherdev/nestcore/identity/adapter"
	identity "github.com/ericfisherdev/nestcore/identity/domain"
	"github.com/ericfisherdev/nestcore/media/adapter"
	"github.com/ericfisherdev/nestcore/media/domain"
)

// seedHouseholdAndMember creates a household and member against the SAME
// pool the photo repository under test uses, via nestcore/identity's own
// public adapter API — this IS the "second consumer, different schema"
// proof the package doc's canonical table shape promises: nothing here
// imports Nestova or Nestorage, and the "photo" table this harness built
// (harness_test.go) lives in a schema neither app owns.
func seedHouseholdAndMember(t *testing.T, pool *identityadapter.HouseholdRepository, memberRepo *identityadapter.MemberRepository) (identity.HouseholdID, identity.MemberID) {
	t.Helper()
	h := &identity.Household{ID: identity.NewHouseholdID(), Name: "Test Household"}
	if err := pool.CreateHousehold(testCtx(t), h); err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}
	m := &identity.Member{ID: identity.NewMemberID(), HouseholdID: h.ID, DisplayName: "Test Member", Role: identity.RoleAdult}
	if err := memberRepo.CreateMember(testCtx(t), m); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	return h.ID, m.ID
}

func newPhoto(householdID identity.HouseholdID, uploadedBy identity.MemberID, storageRef string) *domain.Photo {
	uploader := uploadedBy
	return &domain.Photo{
		ID:          domain.NewPhotoID(),
		HouseholdID: householdID,
		StorageRef:  domain.StorageRef(storageRef),
		ContentHash: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		SizeBytes:   1024,
		ContentType: domain.ContentTypeJPEG,
		UploadedBy:  &uploader,
	}
}

func TestPhotoRepositoryCreateAndGet(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	photo := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if photo.CreatedAt.IsZero() {
		t.Error("Create left CreatedAt zero")
	}
	if photo.StorageBackend != domain.StorageBackendLocal {
		t.Errorf("Create stamped StorageBackend = %q, want %q", photo.StorageBackend, domain.StorageBackendLocal)
	}

	got, err := repo.Get(testCtx(t), photo.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != photo.ID || got.HouseholdID != hhID || got.StorageRef != photo.StorageRef || got.ContentHash != photo.ContentHash {
		t.Errorf("Get = %+v, want it to match the created photo %+v", got, photo)
	}
	if got.UploadedBy == nil || *got.UploadedBy != memberID {
		t.Errorf("Get UploadedBy = %v, want %v", got.UploadedBy, memberID)
	}
}

func TestPhotoRepositoryGetNotFound(t *testing.T) {
	repo := adapter.NewPhotoRepository(newTestPool(t), domain.StorageBackendLocal)
	if _, err := repo.Get(testCtx(t), domain.NewPhotoID()); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoRepositoryCreateUnknownHouseholdMapsToIdentityError(t *testing.T) {
	repo := adapter.NewPhotoRepository(newTestPool(t), domain.StorageBackendLocal)
	photo := newPhoto(identity.NewHouseholdID(), identity.NewMemberID(), "households/nope/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); !errors.Is(err, identity.ErrHouseholdNotFound) {
		t.Errorf("Create(unknown household) error = %v, want identity.ErrHouseholdNotFound", err)
	}
}

func TestPhotoRepositoryCreateUnknownUploaderMapsToIdentityError(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	h := &identity.Household{ID: identity.NewHouseholdID(), Name: "Test Household"}
	if err := households.CreateHousehold(testCtx(t), h); err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}
	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	photo := newPhoto(h.ID, identity.NewMemberID(), "households/"+h.ID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); !errors.Is(err, identity.ErrMemberNotFound) {
		t.Errorf("Create(unknown uploader) error = %v, want identity.ErrMemberNotFound", err)
	}
}

func TestPhotoRepositoryCreateDuplicateContentHash(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	first := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), first); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/bb/two.jpg")
	second.ContentHash = first.ContentHash
	if err := repo.Create(testCtx(t), second); !errors.Is(err, domain.ErrDuplicatePhoto) {
		t.Errorf("Create(duplicate content hash) error = %v, want ErrDuplicatePhoto", err)
	}
}

func TestPhotoRepositoryFindByContentHash(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	photo := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.FindByContentHash(testCtx(t), hhID, photo.ContentHash)
	if err != nil {
		t.Fatalf("FindByContentHash: %v", err)
	}
	if got.ID != photo.ID {
		t.Errorf("FindByContentHash = %+v, want photo %s", got, photo.ID)
	}

	if _, err := repo.FindByContentHash(testCtx(t), hhID, ""); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("FindByContentHash(blank hash) error = %v, want ErrPhotoNotFound", err)
	}
	if _, err := repo.FindByContentHash(testCtx(t), identity.NewHouseholdID(), photo.ContentHash); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("FindByContentHash(other household) error = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoRepositoryListByHousehold(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	p1 := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), p1); err != nil {
		t.Fatalf("Create p1: %v", err)
	}
	p2 := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/bb/two.jpg")
	p2.ContentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := repo.Create(testCtx(t), p2); err != nil {
		t.Fatalf("Create p2: %v", err)
	}

	list, err := repo.ListByHousehold(testCtx(t), hhID)
	if err != nil {
		t.Fatalf("ListByHousehold: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByHousehold returned %d photos, want 2", len(list))
	}

	other, err := repo.ListByHousehold(testCtx(t), identity.NewHouseholdID())
	if err != nil {
		t.Fatalf("ListByHousehold(other): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("ListByHousehold(other household) = %d photos, want 0", len(other))
	}
}

func TestPhotoRepositoryDelete(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	photo := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Delete(testCtx(t), photo.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(testCtx(t), photo.ID); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("Get after Delete error = %v, want ErrPhotoNotFound", err)
	}
	if err := repo.Delete(testCtx(t), photo.ID); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Errorf("Delete(already deleted) error = %v, want ErrPhotoNotFound", err)
	}
}

func TestPhotoRepositoryListAllStorageRefsAndExistsByStorageRef(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	photo := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); err != nil {
		t.Fatalf("Create: %v", err)
	}

	refs, err := repo.ListAllStorageRefs(testCtx(t), domain.StorageBackendLocal)
	if err != nil {
		t.Fatalf("ListAllStorageRefs: %v", err)
	}
	found := false
	for _, ref := range refs {
		if ref == photo.StorageRef {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListAllStorageRefs = %v, want it to include %s", refs, photo.StorageRef)
	}

	s3Refs, err := repo.ListAllStorageRefs(testCtx(t), domain.StorageBackendS3)
	if err != nil {
		t.Fatalf("ListAllStorageRefs(s3): %v", err)
	}
	if len(s3Refs) != 0 {
		t.Fatalf("ListAllStorageRefs(s3) = %v, want none (photo is local-backed)", s3Refs)
	}

	exists, err := repo.ExistsByStorageRef(testCtx(t), photo.StorageRef, domain.StorageBackendLocal)
	if err != nil {
		t.Fatalf("ExistsByStorageRef: %v", err)
	}
	if !exists {
		t.Error("ExistsByStorageRef = false, want true")
	}
	if exists, err := repo.ExistsByStorageRef(testCtx(t), photo.StorageRef, domain.StorageBackendS3); err != nil || exists {
		t.Errorf("ExistsByStorageRef(wrong backend) = %v, %v, want false, nil", exists, err)
	}
}

func TestPhotoRepositoryMigrateStorageBackend(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	photo := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/one.jpg")
	photo.ContentHash = ""
	if err := repo.Create(testCtx(t), photo); err != nil {
		t.Fatalf("Create: %v", err)
	}

	newRef := domain.StorageRef("households/" + hhID.String() + "/test/aa/migrated.jpg")
	done, err := repo.MigrateStorageBackend(testCtx(t), photo.ID, newRef, domain.StorageBackendS3, "backfilledhash0000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("MigrateStorageBackend: %v", err)
	}
	if !done {
		t.Fatal("MigrateStorageBackend done = false, want true")
	}

	got, err := repo.Get(testCtx(t), photo.ID)
	if err != nil {
		t.Fatalf("Get after migrate: %v", err)
	}
	if got.StorageRef != newRef || got.StorageBackend != domain.StorageBackendS3 {
		t.Fatalf("Get after migrate = %+v, want ref %s backend %s", got, newRef, domain.StorageBackendS3)
	}

	// A second migrate attempt on an already-migrated (no longer local) row
	// is a no-op.
	done, err = repo.MigrateStorageBackend(testCtx(t), photo.ID, "other-ref", domain.StorageBackendS3, "x")
	if err != nil {
		t.Fatalf("second MigrateStorageBackend: %v", err)
	}
	if done {
		t.Fatal("second MigrateStorageBackend done = true, want false (row is no longer local-backend)")
	}
}

func TestPhotoRepositoryListByBackend(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendLocal)
	var created []*domain.Photo
	for i := 0; i < 3; i++ {
		p := newPhoto(hhID, memberID, "households/"+hhID.String()+"/test/aa/photo"+string(rune('a'+i))+".jpg")
		p.ContentHash = ""
		if err := repo.Create(testCtx(t), p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		created = append(created, p)
	}

	page, err := repo.ListByBackend(testCtx(t), domain.StorageBackendLocal, domain.PhotoID{}, 2)
	if err != nil {
		t.Fatalf("ListByBackend: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("ListByBackend page size = %d, want 2", len(page))
	}

	rest, err := repo.ListByBackend(testCtx(t), domain.StorageBackendLocal, page[len(page)-1].ID, 10)
	if err != nil {
		t.Fatalf("ListByBackend (second page): %v", err)
	}
	if len(rest) != len(created)-2 {
		t.Fatalf("ListByBackend second page = %d, want %d", len(rest), len(created)-2)
	}
}

// TestPhotoRepositorySecondConsumerDifferentSchema is the AC3 proof
// directly: it persists and reads a photo using ONLY this package's
// exported API, against the schema harness_test.go built (the test
// database's default "public" schema, via an unqualified "photo" table) —
// a schema neither Nestova nor Nestorage owns. If this test compiles and
// passes, media/adapter's PhotoRepository works for a consumer this
// package has never heard of.
func TestPhotoRepositorySecondConsumerDifferentSchema(t *testing.T) {
	pool := newTestPool(t)
	households := identityadapter.NewHouseholdRepository(pool)
	members := identityadapter.NewMemberRepository(pool)
	hhID, memberID := seedHouseholdAndMember(t, households, members)

	repo := adapter.NewPhotoRepository(pool, domain.StorageBackendS3)
	photo := newPhoto(hhID, memberID, "households/"+hhID.String()+"/second_consumer/aa/one.jpg")
	if err := repo.Create(testCtx(t), photo); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if photo.StorageBackend != domain.StorageBackendS3 {
		t.Fatalf("StorageBackend = %q, want %q", photo.StorageBackend, domain.StorageBackendS3)
	}

	got, err := repo.Get(testCtx(t), photo.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != photo.ID || got.TakenAt != nil {
		t.Fatalf("Get = %+v, want a matching row with no TakenAt", got)
	}

	taken := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	photo2 := newPhoto(hhID, memberID, "households/"+hhID.String()+"/second_consumer/bb/two.jpg")
	photo2.ContentHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	photo2.TakenAt = &taken
	if err := repo.Create(testCtx(t), photo2); err != nil {
		t.Fatalf("Create photo2: %v", err)
	}
	got2, err := repo.Get(testCtx(t), photo2.ID)
	if err != nil {
		t.Fatalf("Get photo2: %v", err)
	}
	if got2.TakenAt == nil || !got2.TakenAt.Equal(taken) {
		t.Fatalf("Get photo2 TakenAt = %v, want %v", got2.TakenAt, taken)
	}
}
