// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/testenv"
)

// The admin app maps over the lists of a catalogue entry (packages, their environment variables,
// the review's warnings and capabilities). A null instead of an empty list used to blank the whole
// admin app when an admin pressed Review on a server with no environment variables. Against the
// real registry, which has many such servers.
func TestMCPSearchNeverSendsNullLists(t *testing.T) {
	testenv.Network(t, "registry.modelcontextprotocol.io:443")
	e := newEnv(t)
	adm := e.makeAdminSession()
	for _, q := range []string{"github", "filesystem", "azure"} {
		code, _, raw := adm.do("GET", "/api/mcp/search?q="+q, nil)
		if code != 200 {
			t.Skipf("NOT TESTED: the registry did not answer (%d %s)", code, raw)
		}
		for _, field := range []string{`"env":null`, `"warnings":null`, `"capabilities":null`, `"needs_credentials":null`, `"packages":null`} {
			if strings.Contains(string(raw), field) {
				t.Errorf("search %q returned %s; the admin app cannot map over null", q, field)
			}
		}
		if !strings.Contains(string(raw), `"packages":[`) {
			t.Errorf("search %q returned no package lists at all: %.200s", q, raw)
		}
	}
}
