// Package web embeds the built dashboard the huddle server serves.
package web

import (
	"embed"
	"io/fs"
)

// Files holds the dashboard as built: index.html, app.js, app.css and the fonts. The embed directive fails
// the build when dist is missing, so Files is never nil or empty in a binary that exists.
//
//go:embed all:dist
var Files embed.FS

// FS returns the dashboard rooted at dist/, so names are served exactly as built. Sub fails only on an
// invalid root or a nil FS, and neither can happen here.
func FS() fs.FS {
	sub, _ := fs.Sub(Files, "dist")
	return sub
}
