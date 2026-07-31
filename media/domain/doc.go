// Package domain holds the media bounded context's entities, value objects,
// and ports: Photo, PhotoClass, StorageBackend, and the PhotoStore/
// PhotoRepository/ObjectLister/ExifReader ports adapters implement.
//
// This package is keyed on nestcore/identity's HouseholdID/MemberID, not on
// either app's own household package — see the household/adapter/doc.go
// precedent this mirrors. It imports nothing from either app: PhotoClass is
// an app-registered value type (see photo_class.go) rather than a fixed
// enum, precisely so a new consumer never needs to modify this package to
// add its own upload purpose.
//
// # What stays app-side
//
// A photo ALBUM (Nestova) and a chore-completion PROOF photo (Nestova) are
// business domains built on top of this package's ports, not part of it;
// likewise Nestorage's item-to-photo join. This package owns only the
// shared plumbing: upload validation, EXIF handling, storage backends, and
// the photo row itself (id, household, storage location, and the
// server-verified upload facts) — see adapter's package doc for the exact
// table shape every consumer's own migration must provide.
package domain
