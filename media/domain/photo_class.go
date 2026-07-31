package domain

import (
	"fmt"
	"regexp"
	"sync"
)

// PhotoClass partitions the PhotoStore's key namespace so bytes uploaded for
// one purpose (e.g. a chore-completion proof photo) can never collide with,
// or be reinterpreted as, another purpose's bytes (e.g. a household album
// photo) — every PhotoStore.Put call takes a PhotoClass, and the resulting
// StorageRef is namespaced by it (see the adapter package's buildStorageKey).
//
// PhotoClass is an APP-REGISTERED value type, not a fixed enum (OCP): each
// consuming application calls RegisterPhotoClass, typically once per class
// in a package-level var, to mint the values it uploads under — Nestova
// registers its album and chore-proof classes, Nestorage registers its
// item-photo class — without ever needing to modify this package. The
// registered name becomes the class's own storage-key prefix (see
// classKeyPrefix in the adapter package), so two applications' classes can
// never collide as long as each registers a distinct name.
type PhotoClass struct {
	name string
}

// classIdentifier matches the same lowercase-identifier shape nestcore's
// config.SchemaConfig validates schema names against: a name that is always
// safe to use as a storage-key path segment with no further escaping.
var classIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var (
	classMu    sync.Mutex
	registered = map[string]struct{}{}
)

// RegisterPhotoClass registers and returns a new PhotoClass under name.
// Called at most once per name, normally from a package-level var in the
// registering application's composition root — a duplicate or malformed
// name is a programming error, not a runtime condition a caller can recover
// from, so both panic rather than returning an error. name must be lowercase
// ASCII letters, digits, or underscores, starting with a letter.
func RegisterPhotoClass(name string) PhotoClass {
	classMu.Lock()
	defer classMu.Unlock()
	if !classIdentifier.MatchString(name) {
		panic(fmt.Sprintf("media: photo class name %q must start with a lowercase letter and contain only lowercase letters, digits, or underscores", name))
	}
	if _, exists := registered[name]; exists {
		panic(fmt.Sprintf("media: photo class %q is already registered", name))
	}
	registered[name] = struct{}{}
	return PhotoClass{name: name}
}

// Valid reports whether c is a class actually registered via
// RegisterPhotoClass — the zero value and any hand-built PhotoClass are
// invalid, since a class this package never registered was never assigned a
// storage-key prefix.
func (c PhotoClass) Valid() bool {
	if c.name == "" {
		return false
	}
	classMu.Lock()
	defer classMu.Unlock()
	_, ok := registered[c.name]
	return ok
}

// String returns the registered name, or "unspecified" for the zero value —
// used in error messages and as the class's storage-key prefix (see
// classKeyPrefix), never as a persisted database value (nothing serializes
// a PhotoClass across a boundary today).
func (c PhotoClass) String() string {
	if c.name == "" {
		return "unspecified"
	}
	return c.name
}
