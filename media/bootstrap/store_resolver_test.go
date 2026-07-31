package bootstrap_test

import (
	"context"
	"strings"
	"testing"
	"time"

	mediaadapter "github.com/ericfisherdev/nestcore/media/adapter"
	"github.com/ericfisherdev/nestcore/media/bootstrap"
	"github.com/ericfisherdev/nestcore/media/domain"
)

func TestNewPhotoStoreResolverRejectsInvalidBackend(t *testing.T) {
	cfg := bootstrap.ResolverConfig{
		Backend:             domain.StorageBackend("azure-blob"),
		LocalRoot:           t.TempDir(),
		LocalMaxUploadBytes: 1 << 20,
	}
	if _, _, err := bootstrap.NewPhotoStoreResolver(context.Background(), cfg); err == nil {
		t.Fatal("NewPhotoStoreResolver with an invalid backend = nil error, want error")
	}
}

func TestNewPhotoStoreResolverLocalOnly(t *testing.T) {
	cfg := bootstrap.ResolverConfig{
		Backend:             domain.StorageBackendLocal,
		LocalRoot:           t.TempDir(),
		LocalMaxUploadBytes: 1 << 20,
	}
	resolver, backend, err := bootstrap.NewPhotoStoreResolver(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewPhotoStoreResolver: %v", err)
	}
	if backend != domain.StorageBackendLocal {
		t.Fatalf("backend = %q, want %q", backend, domain.StorageBackendLocal)
	}
	store, err := resolver.Resolve(domain.StorageBackendLocal)
	if err != nil || store == nil {
		t.Fatalf("Resolve(local) = %v, %v, want a store and no error", store, err)
	}
	if _, err := resolver.Resolve(domain.StorageBackendS3); err == nil {
		t.Fatal("Resolve(s3) on a local-only resolver = nil error, want ErrStoreNotConfigured")
	}
}

// TestNewPhotoStoreResolverS3SelectionPropagatesConstructionError covers the
// s3-selected path invoking S3 store construction: with no reachable
// bucket, construction must fail and that failure must propagate rather
// than being silently swallowed. This does not require real S3
// connectivity to observe — NewS3PhotoStore's own parameter validation
// (blank bucket) fails before any network call.
func TestNewPhotoStoreResolverS3SelectionPropagatesConstructionError(t *testing.T) {
	cfg := bootstrap.ResolverConfig{
		Backend:             domain.StorageBackendS3,
		LocalRoot:           t.TempDir(),
		LocalMaxUploadBytes: 1 << 20,
		S3:                  mediaAdapterS3Params(t),
	}
	if _, _, err := bootstrap.NewPhotoStoreResolver(context.Background(), cfg); err == nil {
		t.Fatal("NewPhotoStoreResolver(s3, blank bucket) = nil error, want a propagated construction error")
	} else if !strings.Contains(err.Error(), "create s3 photo store") {
		t.Fatalf("error = %v, want it to name the s3 store construction step", err)
	}
}

// mediaAdapterS3Params returns S3Params with every field blank except what
// NewS3PhotoStore's own validation requires it to check before Bucket —
// Bucket itself is left blank deliberately, since that is the first
// validation to fail.
func mediaAdapterS3Params(t *testing.T) mediaadapter.S3Params {
	t.Helper()
	return mediaadapter.S3Params{
		Region:         "us-east-1",
		PresignTTL:     time.Minute,
		MaxUploadBytes: 1 << 20,
	}
}
