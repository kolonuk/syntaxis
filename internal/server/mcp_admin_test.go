// SPDX-License-Identifier: GPL-2.0-or-later

package server

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/kolonuk/syntaxis/internal/mcp"
	"github.com/kolonuk/syntaxis/internal/testenv"
)

type mcpSearchResult struct {
	Servers []struct {
		Name     string `json:"name"`
		Packages []struct {
			Index   int `json:"index"`
			Package struct {
				RegistryType string `json:"registryType"`
				Identifier   string `json:"identifier"`
				Version      string `json:"version"`
				Env          []struct {
					Name     string `json:"name"`
					Required bool   `json:"required"`
				} `json:"env"`
			} `json:"package"`
			Review struct {
				Pinnable bool `json:"pinnable"`
			} `json:"review"`
		} `json:"packages"`
	} `json:"servers"`
}

// An admin adds an MCP server the way the screen does: find it in the real registry, review it,
// approve it, and a chat user then sees it and can switch it on once they have supplied their own
// credentials. No instance-wide credential is ever handed to a server.
func TestAdminAddsAnMCPServerAndAUserSwitchesItOn(t *testing.T) {
	testenv.Network(t, "registry.modelcontextprotocol.io:443")
	e := newEnv(t)
	adm := e.makeAdminSession()
	usr := e.userSession("mcp-user@example.com")

	// the recommended list the screen shows names servers that exist in the registry
	_, sug, _ := adm.do("GET", "/api/mcp/suggestions", nil)
	list, _ := sug["suggestions"].([]any)
	if len(list) == 0 {
		t.Fatalf("no recommended servers: %v", sug)
	}
	first := list[0].(map[string]any)
	if first["description"] == "" || first["title"] == "" {
		t.Fatalf("a recommended server needs a title and a description for the screen: %v", first)
	}
	name := first["search"].(string)
	mapping := map[string]string{}
	for k, v := range first["env_secrets"].(map[string]any) {
		mapping[k] = v.(string)
	}

	code, _, raw := adm.do("GET", "/api/mcp/search?q="+name, nil)
	if code != 200 {
		testenv.NotTested(t, "the MCP registry did not answer: "+string(raw))
	}
	var found mcpSearchResult
	if err := json.Unmarshal(raw, &found); err != nil {
		t.Fatal(err)
	}
	idx := -1
	for _, s := range found.Servers {
		if s.Name == name && len(s.Packages) > 0 && s.Packages[0].Review.Pinnable {
			idx = s.Packages[0].Index
		}
	}
	if idx < 0 {
		testenv.NotTested(t, name+" is not in the registry with a pinned package right now")
	}

	// a credential name that is not a plain name is refused: credentials are names in each user's own vault
	if code, m, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": name, "package": idx, "env_secrets": map[string]string{"X": "../NO_SUCH"}}); code != 400 {
		t.Fatalf("a bad credential name: %d %v", code, m)
	}
	// nothing is approved by a refused request
	if _, _, raw := adm.do("GET", "/api/mcp/approved", nil); strings.Contains(string(raw), name) {
		t.Fatal("a refused approval was recorded")
	}

	// approve it with the mapping the screen offers
	if code, m, raw := adm.do("POST", "/api/mcp/approve", map[string]any{"name": name, "package": idx, "env_secrets": mapping}); code != 200 {
		t.Fatalf("approve: %d %v %s", code, m, raw)
	}
	_, _, raw = adm.do("GET", "/api/mcp/approved", nil)
	var approved []map[string]any
	_ = json.Unmarshal(raw, &approved)
	if len(approved) != 1 || approved[0]["name"] != name || approved[0]["approved_by"] == "" {
		t.Fatalf("approved list: %s", raw)
	}
	for env, want := range mapping {
		if approved[0]["env_secrets"].(map[string]any)[env] != want {
			t.Fatalf("the credential mapping was not kept: %s", raw)
		}
	}

	// the chat user sees it, switched off, and not ready until their own connection exists
	_, _, raw = usr.do("GET", "/api/mcp", nil)
	var mine []map[string]any
	_ = json.Unmarshal(raw, &mine)
	if len(mine) != 1 || mine[0]["enabled"] != false {
		t.Fatalf("the user's list: %s", raw)
	}
	if len(mapping) > 0 && mine[0]["ready"] != false {
		t.Fatalf("a server that needs the user's own credential is not ready without it: %v", mine[0])
	}
	// it cannot be switched on until the user has their own credentials; then it can
	if len(mapping) > 0 {
		if code, _, _ := usr.do("PUT", "/api/mcp", map[string]any{"name": name, "enabled": true}); code != 409 {
			t.Fatalf("switched on without the user's own credentials: %d", code)
		}
		for _, cred := range mapping {
			if code, _, _ := usr.do("PUT", "/api/secrets/"+cred, map[string]string{"value": "the-users-own-value"}); code != 200 {
				t.Fatalf("the user adds %s: %d", cred, code)
			}
		}
	}
	if code, _, _ := usr.do("PUT", "/api/mcp", map[string]any{"name": name, "enabled": true}); code != 200 {
		t.Fatalf("the user switches it on: %d", code)
	}
	_, _, raw = usr.do("GET", "/api/mcp", nil)
	_ = json.Unmarshal(raw, &mine)
	if mine[0]["enabled"] != true {
		t.Fatalf("not enabled: %s", raw)
	}
	// a user cannot switch on something that is not approved
	if code, _, _ := usr.do("PUT", "/api/mcp", map[string]any{"name": "io.example/other", "enabled": true}); code != 404 {
		t.Fatalf("an unapproved server: %d", code)
	}

	// revoking takes it away from the user at once
	if code, _, _ := adm.do("DELETE", "/api/mcp/approved?name="+name, nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	_, _, raw = usr.do("GET", "/api/mcp", nil)
	_ = json.Unmarshal(raw, &mine)
	if len(mine) != 0 {
		t.Fatalf("a revoked server is still offered: %s", raw)
	}
	// and everything was audited
	es, _ := e.st.ListAudit(t.Context(), 0)
	var seen []string
	for _, x := range es {
		if strings.HasPrefix(x.Action, "mcp.") {
			seen = append(seen, x.Action)
		}
	}
	if len(seen) < 2 {
		t.Fatalf("approving and revoking must be audited: %v", seen)
	}
}

// A server shipped as a container image only runs if the image is on the Docker host, and Syntaxis
// cannot download images. So approving one says so and tells the admin the command, and works once
// the image is there. Uses the real Docker engine and the real image check.
func TestImageServerIsApprovedOnlyOnceItsImageIsOnTheDockerHost(t *testing.T) {
	e := newEnv(t)
	e.app.Sandbox = realSandbox(t, e)
	const ref = "syntaxis-test-mcp-image:7.7.7"
	_ = exec.Command("docker", "rmi", ref).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", ref).Run() })
	e.app.MCPSource = mcp.Local{Entries: []mcp.Entry{{
		Name: "io.example/image-server", Description: "an image", Repository: "https://example.com/r", Status: "active",
		Packages: []mcp.Package{{RegistryType: "oci", Identifier: ref, Transport: "stdio"}},
	}}}
	adm := e.makeAdminSession()

	// the review says the image is not there and gives the command
	_, _, raw := adm.do("GET", "/api/mcp/search?q=image", nil)
	var found mcpSearchResult
	_ = json.Unmarshal(raw, &found)
	var rev struct {
		Servers []struct {
			Packages []struct {
				ImagePresent *bool `json:"image_present"`
				Review       struct {
					Pinnable    bool   `json:"pinnable"`
					PullCommand string `json:"pull_command"`
				} `json:"review"`
			} `json:"packages"`
		} `json:"servers"`
	}
	_ = json.Unmarshal(raw, &rev)
	if len(rev.Servers) != 1 || len(rev.Servers[0].Packages) != 1 {
		t.Fatalf("search: %s", raw)
	}
	p := rev.Servers[0].Packages[0]
	if !p.Review.Pinnable || p.Review.PullCommand != "docker pull "+ref || p.ImagePresent == nil || *p.ImagePresent {
		t.Fatalf("the review must say the image is missing and how to get it: %s", raw)
	}
	// approving without the image is refused with the command
	code, m, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/image-server", "package": 0, "env_secrets": map[string]string{}})
	if code != 409 || !strings.Contains(m["error"].(string), "docker pull "+ref) {
		t.Fatalf("approve without the image: %d %v", code, m)
	}
	// put the image on the host (a local tag of the sandbox image stands for a pulled one)
	if out, err := exec.Command("docker", "tag", e.app.Sandbox.Cfg.Image, ref).CombinedOutput(); err != nil {
		t.Skipf("NOT TESTED: could not tag a local image: %s", out)
	}
	_, _, raw = adm.do("GET", "/api/mcp/search?q=image", nil)
	_ = json.Unmarshal(raw, &rev)
	if rev.Servers[0].Packages[0].ImagePresent == nil || !*rev.Servers[0].Packages[0].ImagePresent {
		t.Fatalf("the review must notice the image is now there: %s", raw)
	}
	if code, m, _ := adm.do("POST", "/api/mcp/approve", map[string]any{"name": "io.example/image-server", "package": 0, "env_secrets": map[string]string{}}); code != 200 {
		t.Fatalf("approve with the image present: %d %v", code, m)
	}
	_, _, raw = adm.do("GET", "/api/mcp/approved", nil)
	if !strings.Contains(string(raw), `"package":"`+ref+`"`) {
		t.Fatalf("an approved image is listed by its reference: %s", raw)
	}
}
