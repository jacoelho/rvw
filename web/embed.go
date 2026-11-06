// Package web embeds the built review UI (web/dist). Rebuild it with `make web` after changing
// web/src.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Assets returns the built UI rooted at dist.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; Sub fails only for an invalid literal path
	}
	return sub
}
