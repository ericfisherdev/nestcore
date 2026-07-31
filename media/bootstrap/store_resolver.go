// Package bootstrap builds the media bounded context's composition-root-only
// dependency: the PhotoStore resolver/write-backend pair every application
// consuming media/domain.PhotoStoreResolver needs, constructed identically
// from one plain ResolverConfig rather than each application maintaining
// its own copy of the local/S3 selection logic. It is deliberately its own
// package, not folded into media/adapter: adapter's own S3Params doc states
// that package never depends on any application's own config package, so a
// config-consuming builder belongs in a peer package instead. This package
// takes ResolverConfig (its OWN plain struct), never an application's
// config type directly — the application composition root translates its
// own configuration into ResolverConfig, keeping this package equally
// reusable by Nestova, Nestorage, or any future consumer (DIP).
package bootstrap

import (
	"context"
	"fmt"

	mediaadapter "github.com/ericfisherdev/nestcore/media/adapter"
	mediadomain "github.com/ericfisherdev/nestcore/media/domain"
)

// ResolverConfig is the plain, application-agnostic configuration
// NewPhotoStoreResolver needs to build a PhotoStoreResolver: which backend
// new uploads write to, the local store's root/size cap (always
// constructed — see NewPhotoStoreResolver's doc), and the S3 store's
// parameters (read only when Backend is StorageBackendS3).
type ResolverConfig struct {
	// Backend is the StorageBackend Upload writes new photos to.
	Backend mediadomain.StorageBackend
	// LocalRoot is the local filesystem root LocalPhotoStore is always
	// constructed against.
	LocalRoot string
	// LocalMaxUploadBytes caps a single upload written to the local store
	// only; the S3 store's own cap is configured independently via
	// S3.MaxUploadBytes below (the two are set to the same operator-
	// configured limit by convention, not by any propagation this
	// function performs).
	LocalMaxUploadBytes int64
	// S3 configures the S3 store, read only when Backend is
	// StorageBackendS3.
	S3 mediaadapter.S3Params
}

// NewPhotoStoreResolver builds the domain.PhotoStoreResolver every media
// service reads through, plus the write-target StorageBackend Upload writes
// new photos to (the mixed-state fix — see domain.PhotoStoreResolver's doc
// for the full rationale). This is the one place in a composition root that
// knows both concrete PhotoStore adapters exist; every other consumer
// depends on the resolver/domain.PhotoStore interfaces alone.
//
// The LOCAL store is ALWAYS constructed, regardless of cfg.Backend: it has
// a safe, zero-required-config default, and constructing it unconditionally
// is what keeps historical local-backed rows readable after an operator
// switches to s3 — the resolver can still route THOSE rows' reads to it,
// and any storage migrate/verify tooling needs direct access to it
// regardless of which backend is currently configured for new writes. The
// S3 store is constructed ONLY when s3 is actually selected (cfg.Backend ==
// StorageBackendS3), since it requires real bucket/credential configuration
// a local-only deployment should never be forced to provide.
//
// ctx bounds the S3 backend's startup HeadBucket reachability check — the
// caller is expected to derive it with a bounded timeout so a
// network-unreachable S3 endpoint fails the boot promptly instead of
// hanging indefinitely; the local store's construction has no meaningful
// cancellation point and ignores ctx entirely.
func NewPhotoStoreResolver(ctx context.Context, cfg ResolverConfig) (mediadomain.PhotoStoreResolver, mediadomain.StorageBackend, error) {
	if !cfg.Backend.Valid() {
		return nil, "", fmt.Errorf("media/bootstrap: invalid write-target StorageBackend %q", cfg.Backend)
	}
	localStore, err := mediaadapter.NewLocalPhotoStore(cfg.LocalRoot, cfg.LocalMaxUploadBytes)
	if err != nil {
		return nil, "", fmt.Errorf("create local photo store: %w", err)
	}
	stores := map[mediadomain.StorageBackend]mediadomain.PhotoStore{
		mediadomain.StorageBackendLocal: localStore,
	}

	if cfg.Backend == mediadomain.StorageBackendS3 {
		s3Store, err := mediaadapter.NewS3PhotoStore(ctx, cfg.S3)
		if err != nil {
			return nil, "", fmt.Errorf("create s3 photo store: %w", err)
		}
		stores[mediadomain.StorageBackendS3] = s3Store
	}

	return mediaadapter.NewStoreResolver(stores), cfg.Backend, nil
}
