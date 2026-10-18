// Package web embeds the viewer page.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

func Files() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
