// Package webfs carries the administration UI inside the binary.
//
// The UI used to be served from the working directory, which made the screen a
// separate artefact from the program: a container with a volume mounted over
// the application directory lost the UI entirely, and a deployment that kept an
// older checked-out web directory served an older screen than the binary it ran
// — the version badge in the header is the first thing to disappear that way.
// Embedding removes the whole class of mismatch: the screen is the build.
package webfs

import "embed"

// Assets holds every file the browser loads.
//
//go:embed *.html *.css *.js *.svg
var Assets embed.FS

// Directory is the environment variable that overrides the embedded assets with
// a directory on disk, for editing the UI without rebuilding.
const Directory = "GIT_CTX_WEB_DIR"
