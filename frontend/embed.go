// Package webui embeds the dropwarden frontend so it can be served by the Wails
// asset handler in a production build. During `wails3 dev` the live frontend
// directory is served instead (with hot reload), so this embed is used for
// packaged builds.
//
// After running `wails3 generate bindings`, add the generated bindings to the
// embed list (e.g. add "bindings" to the //go:embed directive) so the packaged
// build ships them too.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html app.js api.js styles.css
var assets embed.FS

// FS is the embedded frontend filesystem.
var FS fs.FS = assets

// Handler returns an http.Handler that serves the embedded frontend, with
// index.html served for "/".
func Handler() http.Handler {
	return http.FileServerFS(assets)
}
