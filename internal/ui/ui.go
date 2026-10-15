// Package ui embeds the LaraMonster dashboard (plain HTML/CSS/JS + Alpine).
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets
var assets embed.FS

// FS is the dashboard's static files.
func FS() fs.FS {
	sub, _ := fs.Sub(assets, "assets")
	return sub
}

// Handler serves the dashboard. Unknown paths fall back to index.html.
func Handler() http.Handler {
	files := http.FileServerFS(FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" || p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if _, err := fs.Stat(FS(), p); err != nil {
			r.URL.Path = "/"
		} else if strings.HasPrefix(p, "fonts/") || strings.HasPrefix(p, "img/") || strings.HasPrefix(p, "vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			// app.js and app.css change with every release: a browser must
			// not keep using an old copy after an update.
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
