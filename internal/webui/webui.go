// Package webui embeds the built web app from web/.
//
// `make web` copies the SvelteKit build into dist/. Without that step dist/ holds only
// .keep, and the server reports that the web UI isn't built.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Shell is the app's fallback page. The server returns it for every path that isn't a
// file, and the client-side router takes over from there.
const Shell = "200.html"

// FS returns the built web app, rooted at its top directory.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub fails only for an invalid directory name, which "dist" is not.
		panic(err)
	}
	return sub
}
