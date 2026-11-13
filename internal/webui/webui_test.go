// SPDX-License-Identifier: GPL-2.0-or-later

package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(h http.Handler, p string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
	return rr
}

func TestNeverServesAPIPaths(t *testing.T) {
	for _, app := range []string{"user", "admin"} {
		if rr := get(Handler(app), "/api/anything"); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", app, rr.Code)
		}
	}
}

// These run against the real embedded build when it exists (CI builds the
// frontend before `go test`); with only the placeholder they are skipped.
func built(t *testing.T) {
	t.Helper()
	if rr := get(Handler("user"), "/"); rr.Code != 200 {
		t.Skip("frontend not built")
	}
}

func TestUserAppServesShellAndFallsBackForRoutes(t *testing.T) {
	built(t)
	h := Handler("user")
	root := get(h, "/")
	if !strings.Contains(root.Body.String(), "Syntaxis") || root.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("%s", root.Body.String())
	}
	if rr := get(h, "/reset-password?token=x"); rr.Code != 200 || rr.Body.String() != root.Body.String() {
		t.Fatal("client-side routes must fall back to the SPA shell")
	}
	if rr := get(h, "/manifest.webmanifest"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"display": "standalone"`) {
		t.Fatalf("manifest: %d", rr.Code)
	}
	if rr := get(h, "/sw.js"); rr.Code != 200 || rr.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("service worker must not be cached")
	}
}

func TestUserListenerNeverServesAdminApp(t *testing.T) {
	built(t)
	user := get(Handler("user"), "/")
	if rr := get(Handler("user"), "/admin/index.html"); rr.Body.String() != user.Body.String() {
		t.Fatal("the user listener served the admin SPA")
	}
	admin := get(Handler("admin"), "/")
	if admin.Body.String() == user.Body.String() || !strings.Contains(admin.Body.String(), "Syntaxis admin") {
		t.Fatal("admin listener must serve the admin SPA")
	}
}
