// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/mcp"
)

// Any user can search the catalogue and ask for a server; the admin sees the request, and approving
// the server clears it. A user sees and withdraws only their own requests.
func TestUsersSearchAndRequestMCPServers(t *testing.T) {
	e := newEnv(t)
	e.app.MCPSource = mcp.Local{Entries: []mcp.Entry{
		{Name: "io.example/notes-server", Title: "Notes", Description: "keeps notes", Status: "active", Packages: []mcp.Package{{RegistryType: "npm", Identifier: "notes-mcp", Version: "1.0.0", Transport: "stdio"}}},
		{Name: "io.example/other", Description: "something else", Status: "active", Packages: []mcp.Package{{RegistryType: "npm", Identifier: "other-mcp", Version: "1.0.0", Transport: "stdio"}}},
	}}
	ann := e.userSession("ann-mcp@example.com")
	bob := e.userSession("bob-mcp@example.com")
	adm := e.makeAdminSession()

	// the search needs a sign-in, shows what a server is, and not the admin's review details
	if code, _, _ := e.client(e.user.URL).do("GET", "/api/mcp/search?q=notes", nil); code != 401 {
		t.Fatalf("anonymous search: %d", code)
	}
	code, res, raw := ann.do("GET", "/api/mcp/search?q=notes", nil)
	servers, _ := res["servers"].([]any)
	if code != 200 || len(servers) != 1 {
		t.Fatalf("search: %d %s", code, raw)
	}
	first := servers[0].(map[string]any)
	if first["name"] != "io.example/notes-server" || first["requested"] != false || first["approved"] != false || first["packages"] != nil {
		t.Fatalf("entry: %v", first)
	}

	// ask, and the search then says so; asking again changes the reason instead of adding another
	if code, _, raw := ann.do("POST", "/api/mcp/requests", map[string]string{"name": "io.example/notes-server", "title": "Notes", "reason": "to keep meeting notes"}); code != 201 {
		t.Fatalf("request: %d %s", code, raw)
	}
	if code, _, _ := ann.do("POST", "/api/mcp/requests", map[string]string{"name": "io.example/notes-server", "reason": "better reason"}); code != 200 {
		t.Fatalf("second request: %d", code)
	}
	_, res, _ = ann.do("GET", "/api/mcp/search?q=notes", nil)
	if res["servers"].([]any)[0].(map[string]any)["requested"] != true {
		t.Fatalf("not marked as requested: %v", res)
	}
	if code, _, _ := ann.do("POST", "/api/mcp/requests", map[string]string{"name": ""}); code != 400 {
		t.Fatalf("empty name: %d", code)
	}

	// each user sees only their own; the admin sees all of them, with the email
	_, _, raw = bob.do("GET", "/api/mcp/requests", nil)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("bob sees %s", raw)
	}
	code, _, raw = ann.do("GET", "/api/mcp/requests", nil)
	if code != 200 || !strings.Contains(string(raw), "better reason") || strings.Contains(string(raw), "meeting notes") {
		t.Fatalf("ann's requests: %s", raw)
	}
	if code, _, raw = adm.do("GET", "/api/mcp/requests", nil); code != 200 || !strings.Contains(string(raw), "ann-mcp@example.com") {
		t.Fatalf("admin's list: %d %s", code, raw)
	}
	// the admin list is not on the user's listener, and a user cannot withdraw someone else's request
	if code, _, _ := e.client(e.admin.URL).do("GET", "/api/mcp/requests", nil); code != 401 && code != 403 {
		t.Fatalf("an anonymous client read the admin list: %d", code)
	}
	list := e.app.loadMCPRequests(t.Context())
	if len(list) != 1 {
		t.Fatalf("stored: %v", list)
	}
	if code, _, _ := bob.do("DELETE", "/api/mcp/requests/"+list[0].ID, nil); code != 404 {
		t.Fatalf("bob withdrew ann's request: %d", code)
	}

	// approving the server clears the request
	if code, _, raw := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/notes-server", "package": 0, "env_secrets": map[string]string{}}); code != 200 {
		t.Fatalf("approve: %d %s", code, raw)
	}
	if left := e.app.loadMCPRequests(t.Context()); len(left) != 0 {
		t.Fatalf("request left after approval: %v", left)
	}
	// asking for an approved server is refused, and the search shows it as approved
	if code, _, _ := ann.do("POST", "/api/mcp/requests", map[string]string{"name": "io.example/notes-server"}); code != 409 {
		t.Fatalf("request for an approved server: %d", code)
	}

	// a user withdraws their own request
	if code, _, _ := ann.do("POST", "/api/mcp/requests", map[string]string{"name": "io.example/other"}); code != 201 {
		t.Fatal("request")
	}
	list = e.app.loadMCPRequests(t.Context())
	if code, _, _ := ann.do("DELETE", "/api/mcp/requests/"+list[0].ID, nil); code != 200 {
		t.Fatalf("withdraw: %d", code)
	}
	// the admin can dismiss one without approving
	_, _, _ = bob.do("POST", "/api/mcp/requests", map[string]string{"name": "io.example/other"})
	list = e.app.loadMCPRequests(t.Context())
	if code, _, _ := adm.do("DELETE", "/api/mcp/requests/"+list[0].ID, nil); code != 200 {
		t.Fatalf("dismiss: %d", code)
	}
}
