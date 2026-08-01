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
func Handler() http.Handler {
	return http.FileServerFS(assetsFS)
}
