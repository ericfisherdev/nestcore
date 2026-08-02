package static_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ericfisherdev/nestcore/ui/static"
)

func TestFS_ContainsExpectedAssets(t *testing.T) {
	for _, path := range []string{
		"fonts/hanken-grotesk.woff2",
		"fonts/space-mono.woff2",
		"js/htmx.min.js",
		"js/alpine.min.js",
	} {
		info, err := fs.Stat(static.FS(), path)
		if err != nil {
			t.Errorf("stat %q: %v", path, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%q is empty", path)
		}
	}
}

func TestHandler_ServesAssetsUnderMountPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(static.MountPath, http.StripPrefix(static.MountPath, static.Handler()))

	req := httptest.NewRequest(http.MethodGet, static.MountPath+"js/htmx.min.js", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", req.URL.Path, rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("response body is empty")
	}
}

func TestHandler_MissingAssetIs404(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(static.MountPath, http.StripPrefix(static.MountPath, static.Handler()))

	req := httptest.NewRequest(http.MethodGet, static.MountPath+"js/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET %s: status %d, want 404", req.URL.Path, rec.Code)
	}
}

// TestHandler_MissingAssetHasNoCacheControl guards against a 404 for a
// missing asset getting cached immutable for a year — a client that
// requested a not-yet-shipped asset would then never see it once a later
// release actually adds it.
func TestHandler_MissingAssetHasNoCacheControl(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(static.MountPath, http.StripPrefix(static.MountPath, static.Handler()))

	req := httptest.NewRequest(http.MethodGet, static.MountPath+"js/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control = %q on a 404 response, want unset", got)
	}
}

// TestHandler_SetsImmutableCacheControl guards against the shell's ~145 KB
// of fonts/JS shipping with no freshness or revalidation signal at all —
// embed.FS's zero ModTime means net/http never emits a Last-Modified either,
// so without an explicit Cache-Control every asset is fetched fresh on every
// page load.
func TestHandler_SetsImmutableCacheControl(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(static.MountPath, http.StripPrefix(static.MountPath, static.Handler()))

	req := httptest.NewRequest(http.MethodGet, static.MountPath+"js/htmx.min.js", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	want := "public, max-age=31536000, immutable"
	if got := rec.Header().Get("Cache-Control"); got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
}

// TestHandler_DirectoryRequestsAreNotFound guards against http.FileServerFS's
// default directory-listing behavior exposing the embedded fonts/js tree
// layout. The two paths differ after StripPrefix: MountPath itself arrives
// as an empty path, a subdirectory keeps its trailing slash.
func TestHandler_DirectoryRequestsAreNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(static.MountPath, http.StripPrefix(static.MountPath, static.Handler()))

	for _, path := range []string{static.MountPath, static.MountPath + "js/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404 (directory listing should not be served)", path, rec.Code)
		}
	}
}
