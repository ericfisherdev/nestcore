// Package static embeds the Hearth shell's self-hosted fonts and vendored JS
// (Hanken Grotesk, Space Mono, HTMX, Alpine) so both apps ship them from the
// nestcore binary rather than each keeping its own copy. Embedding — not a
// CDN link — is load-bearing: the appliance must render with the internet
// down (see housedev's top-level CLAUDE.md, "No page may request an external
// host").
package static

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed fonts js
var assetsFS embed.FS

// MountPath is the path the consuming app's mux must mount [Handler] under.
// ui/css/base.css and ui/components's Layout hardcode asset URLs against this
// exact prefix, so an app that mounts the handler elsewhere breaks both.
const MountPath = "/hearth-static/"

// FS returns the embedded assets rooted so that, e.g., "fonts/hanken-grotesk.woff2"
// resolves to ui/static/fonts/hanken-grotesk.woff2.
func FS() fs.FS {
	return assetsFS
}

// Handler serves the embedded assets. Mount it at [MountPath]:
//
//	mux.Handle(static.MountPath, http.StripPrefix(static.MountPath, static.Handler()))
//
// Responses carry a long-lived immutable Cache-Control header — the assets
// are baked into the binary at build time and never change under a running
// process — and directory requests 404 rather than listing the embedded
// fonts/js trees.
func Handler() http.Handler {
	files := http.FileServerFS(assetsFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// StripPrefix leaves "" for MountPath itself and a trailing slash
		// for subdirectories; neither should render a directory index.
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		// Stat first so Cache-Control is only ever set on a response that
		// will actually succeed — setting it unconditionally would have a
		// 404 for a missing asset cached immutable for a year too, so a
		// client that requested it before the asset existed would never
		// see it added in a later release.
		info, err := fs.Stat(assetsFS, strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}
