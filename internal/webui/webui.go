// SPDX-License-Identifier: GPL-2.0-or-later

// Package webui embeds the built frontend (web/ -> internal/webui/dist).
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the single-page apps. app is "user" or "admin"; the admin
// build lives in dist/admin and is only ever mounted on the admin listener.
func Handler(app string) http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	if app == "admin" {
		if sub, err := fs.Sub(root, "admin"); err == nil {
			root = sub
		}
	}
	fsrv := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "api/") {
			http.NotFound(w, r)
			return
		}
		if p == "" {
			p = "index.html"
		}
		if f, err := root.Open(p); err == nil {
			f.Close()
			if p == "sw.js" || p == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			} else if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fsrv.ServeHTTP(w, r)
			return
		}
		// SPA fallback for client-side routes
		if idx, err := root.Open("index.html"); err == nil {
			idx.Close()
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fsrv.ServeHTTP(w, r2)
			return
		}
		http.NotFound(w, r)
	})
}
