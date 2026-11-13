// SPDX-License-Identifier: GPL-2.0-or-later

package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlerNeverServesAPIPaths(t *testing.T) {
	h := Handler("user")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/anything", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("%d", rr.Code)
	}
}

func TestAdminHandlerBuilds(t *testing.T) {
	if Handler("admin") == nil {
		t.Fatal("nil")
	}
}
