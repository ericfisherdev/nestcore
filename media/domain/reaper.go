package domain

import (
	"context"
	"time"
)

// ObjectInfo is one stored object as ObjectLister reports it.
type ObjectInfo struct {
	// Key is the object's storage key — directly comparable to a
	// PhotoRepository row's StorageRef, since both are opaque strings drawn
	// from the same key scheme.
	Key StorageRef
	// LastModified is when the backend last wrote this object — the grace
	// window a storage reaper checks before treating an unreferenced object
	// as safe to delete: an object younger than the grace window might be
	// mid-upload (Put has written the bytes but PhotoRepository.Create has
	// not yet committed the referencing row) rather than genuinely
	// orphaned.
	LastModified time.Time
}

// ObjectLister lists a PhotoStore backend's raw stored objects, class by
// class, for a storage reaper: only an object-store backend (S3PhotoStore)
// implements it. LocalPhotoStore does not — a local filesystem's own
// orphaned files are not a reaper's target for now, since only an object
// store can be listed cheaply without walking a filesystem tree the caller
// may not even have direct access to.
//
// A PhotoStore backend that also implements ObjectLister opts into being
// reapable; app.ReaperService type-asserts for it rather than this being
// folded into the base PhotoStore interface, keeping PhotoStore itself
// minimal (ISP) for the common case of a caller that only ever needs
// Put/Open/Delete/URL.
type ObjectLister interface {
	// ListObjects returns every stored object under class's namespace,
	// across every household — the reaper operates bucket-wide, not
	// household-scoped, since orphan reclamation is a storage-housekeeping
	// concern, not a tenant-facing one. Returns an empty slice (not an
	// error) when the class has no stored objects yet.
	ListObjects(ctx context.Context, class PhotoClass) ([]ObjectInfo, error)
}
