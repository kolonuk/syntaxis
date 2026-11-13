// SPDX-License-Identifier: GPL-2.0-or-later

// Package webui embeds the built frontend (web/ -> internal/webui/dist).
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the single-page apps. app is "user" or "admin". The admin
// SPA (dist/admin/index.html) is only ever mounted on the admin listener; the
// user app never links to it.
func Handler(app string) http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	index := "index.html"
	if app == "admin" {
		index = "admin/index.html"
	}
	serve := func(w http.ResponseWriter, r *http.Request, name string) bool {
		b, err := fs.ReadFile(root, name)
		if err != nil {
			return false
		}
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		switch {
		case name == "sw.js" || name == index:
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(name, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
		return true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if strings.HasPrefix(p, "api/") || p == "api" {
			http.NotFound(w, r)
			return
		}
		if p == "" || p == "index.html" {
			p = index
		}
		if app == "user" && strings.HasPrefix(p, "admin/") {
			p = index // the user listener never serves the admin app
		}
		if st, err := fs.Stat(root, p); err == nil && !st.IsDir() && serve(w, r, p) {
			return
		}
		if !serve(w, r, index) { // SPA fallback for client-side routes
			http.NotFound(w, r)
		}
	})
}
