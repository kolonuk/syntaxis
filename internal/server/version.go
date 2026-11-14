// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"net/http"

	"github.com/kolonuk/syntaxis/internal/buildinfo"
)

// handleVersion says which build is running. It is public and holds nothing sensitive,
// so a bug report (or a script) can check whether a fix has been deployed.
func handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildinfo.Get())
}
