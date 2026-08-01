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
